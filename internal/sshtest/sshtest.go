// Package sshtest runs an in-process SSH server for tests. It accepts public
// key and password logins and forwards direct-tcpip channels, like an
// OpenSSH server with AllowTcpForwarding.
package sshtest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/mevdschee/srdp4win/internal/sshconn"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Options configure the test server.
type Options struct {
	// Password enables password authentication with this password.
	Password string
	// KeyboardInteractive enables keyboard-interactive authentication with
	// one hidden "Password:" question, answered with Password.
	KeyboardInteractive bool
	// ExtraKeys are accepted in addition to the generated client key.
	ExtraKeys []ssh.PublicKey
	// NoForwarding refuses direct-tcpip channels.
	NoForwarding bool
}

// Server is a running test server.
type Server struct {
	Addr    string
	HostKey ssh.Signer

	t         testing.TB
	keyPEM    []byte
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	forwarded int
}

// NewKey generates an ed25519 key and returns it as signer and PEM.
func NewKey(t testing.TB) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return signer, pem.EncodeToMemory(block)
}

// Start starts a server that accepts one generated client key.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	hostKey, _ := NewKey(t)
	clientKey, keyPEM := NewKey(t)
	s := &Server{HostKey: hostKey, t: t, keyPEM: keyPEM, conns: map[net.Conn]struct{}{}}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range append([]ssh.PublicKey{clientKey.PublicKey()}, opts.ExtraKeys...) {
				if bytes.Equal(key.Marshal(), k.Marshal()) {
					return nil, nil
				}
			}
			return nil, os.ErrPermission
		},
	}
	if opts.Password != "" && !opts.KeyboardInteractive {
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == opts.Password {
				return nil, nil
			}
			return nil, os.ErrPermission
		}
	}
	if opts.KeyboardInteractive {
		cfg.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := ask("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) == 1 && answers[0] == opts.Password {
				return nil, nil
			}
			return nil, os.ErrPermission
		}
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ln.Close()
		s.KillClients()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveConn(conn, cfg, opts)
		}
	}()
	s.Addr = ln.Addr().String()
	return s
}

// Config returns a client config for this server that trusts its host key and
// uses the accepted client key, stored in a fresh temp dir.
func (s *Server) Config() sshconn.Config {
	s.t.Helper()
	local := s.t.TempDir()
	keyFile := filepath.Join(local, "id_ed25519")
	if err := os.WriteFile(keyFile, s.keyPEM, 0o600); err != nil {
		s.t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(s.Addr)
	portNum, _ := strconv.Atoi(port)
	return sshconn.Config{
		Host:           host,
		Port:           portNum,
		User:           "team",
		Auth:           sshconn.AuthKeyFile,
		KeyFile:        keyFile,
		KnownHostsFile: filepath.Join(local, "known_hosts"),
		TrustHost:      func(string, ssh.PublicKey) bool { return true },
		KeepAlive:      -1,
	}
}

// Dial connects a new client (closed at test end).
func (s *Server) Dial() *sshconn.Client {
	s.t.Helper()
	c, err := sshconn.Dial(context.Background(), s.Config())
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { c.Close() })
	return c
}

// KillClients cuts the TCP connection of every connected client, like a
// network drop.
func (s *Server) KillClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}

// Clients is the number of connected clients.
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Forwarded is the number of direct-tcpip channels opened so far.
func (s *Server) Forwarded() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forwarded
}

// KnownHostsLine formats a known_hosts line.
func KnownHostsLine(addr string, key ssh.PublicKey) string {
	return knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
}

// Echo starts a TCP server that sends back what it receives: the tiny "RDP
// server" of the tests. It returns its address.
func Echo(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func (s *Server) serveConn(conn net.Conn, cfg *ssh.ServerConfig, opts Options) {
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	// Answers keep-alives with a failure reply, which is all a client needs.
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		if opts.NoForwarding {
			nc.Reject(ssh.Prohibited, "forwarding is off")
			continue
		}
		go s.forward(nc)
	}
}

// forward serves one direct-tcpip channel (RFC 4254, 7.2).
func (s *Server) forward(nc ssh.NewChannel) {
	var req struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &req); err != nil {
		nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))))
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer target.Close()
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	s.mu.Lock()
	s.forwarded++
	s.mu.Unlock()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(target, ch)
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(ch, target)
		ch.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
}
