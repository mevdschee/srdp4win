package sshconn_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/mevdschee/srdp4win/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func TestHostKeyTrustOnFirstUse(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	cfg := srv.Config()
	var seen string
	cfg.TrustHost = func(hostport string, key ssh.PublicKey) bool {
		seen = sshconn.Fingerprint(key)
		return true
	}
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
	if want := sshconn.Fingerprint(srv.HostKey.PublicKey()); seen != want || !strings.HasPrefix(seen, "SHA256:") {
		t.Fatalf("fingerprint %q, want %q", seen, want)
	}
	// The key is stored now: no question the second time.
	cfg.TrustHost = func(string, ssh.PublicKey) bool { t.Error("known host must not be asked again"); return false }
	if err := dial(cfg); err != nil {
		t.Fatalf("second dial with stored key: %v", err)
	}
}

func TestHostKeyUnknownRejected(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	cfg := srv.Config()
	cfg.TrustHost = nil
	if err := dial(cfg); !errors.Is(err, sshconn.ErrHostUnknown) {
		t.Fatalf("want ErrHostUnknown, got %v", err)
	}
	cfg.TrustHost = func(string, ssh.PublicKey) bool { return false }
	if err := dial(cfg); !errors.Is(err, sshconn.ErrHostUnknown) {
		t.Fatalf("want ErrHostUnknown, got %v", err)
	}
	if data, _ := os.ReadFile(cfg.KnownHostsFile); len(data) != 0 {
		t.Fatalf("untrusted key was stored: %q", data)
	}
}

func TestHostKeyMismatch(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	// A different server on the same address would present another key;
	// simulate by writing a wrong key for this host into known_hosts.
	other, _ := sshtest.NewKey(t)
	cfg := srv.Config()
	cfg.KnownHostsFile = filepath.Join(t.TempDir(), "known_hosts")
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	line := sshtest.KnownHostsLine(addr, other.PublicKey())
	if err := os.WriteFile(cfg.KnownHostsFile, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.TrustHost = func(string, ssh.PublicKey) bool { t.Error("a changed key must not be offered for trust"); return true }
	if err := dial(cfg); !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Fatalf("want ErrHostKeyMismatch, got %v", err)
	}
}

func TestDialTCP(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	c := srv.Dial()
	conn, err := c.DialTCP(context.Background(), sshtest.Echo(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := []byte("hello through the tunnel")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("got %q", got)
	}
}

func TestDialTCPRefused(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	c := srv.Dial()
	// A port nobody listens on.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if _, err := c.DialTCP(context.Background(), addr); err == nil {
		t.Fatal("dial to a closed port must fail")
	}
	// Forwarding switched off on the server.
	srv = sshtest.Start(t, sshtest.Options{NoForwarding: true})
	if _, err := srv.Dial().DialTCP(context.Background(), sshtest.Echo(t)); err == nil {
		t.Fatal("dial must fail when the server refuses forwarding")
	}
}

func TestDialTCPCancelled(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	c := srv.Dial()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.DialTCP(ctx, sshtest.Echo(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestCloseEndsConnection(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	c := srv.Dial()
	if c.Err() != nil {
		t.Fatalf("live connection has error %v", c.Err())
	}
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after Close")
	}
	if !errors.Is(c.Err(), sshconn.ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", c.Err())
	}
	if _, err := c.DialTCP(context.Background(), sshtest.Echo(t)); err == nil {
		t.Fatal("DialTCP on a closed client must fail")
	}
}

func TestDropIsNoticed(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	c := srv.Dial()
	srv.KillClients()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("drop not noticed")
	}
	if err := c.Err(); err == nil || errors.Is(err, sshconn.ErrClosed) {
		t.Fatalf("want a connection lost error, got %v", err)
	}
}

// A server that stops answering (no FIN, no RST) is found by the keep-alive.
func TestKeepAliveFindsDeadLink(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	// A proxy in between that can go silent.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	silent := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", srv.Addr)
		if err != nil {
			conn.Close()
			return
		}
		pipe := func(dst, src net.Conn) {
			buf := make([]byte, 4096)
			for {
				n, err := src.Read(buf)
				select {
				case <-silent:
					<-make(chan struct{}) // hold the bytes forever
				default:
				}
				if err != nil {
					return
				}
				dst.Write(buf[:n])
			}
		}
		go pipe(up, conn)
		go pipe(conn, up)
	}()

	cfg := srv.Config()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg.Host = host
	cfg.Port, _ = strconv.Atoi(port)
	cfg.KeepAlive = 100 * time.Millisecond
	c, err := sshconn.Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(350 * time.Millisecond) // some keep-alives pass
	if c.Err() != nil {
		t.Fatalf("keep-alive closed a live connection: %v", c.Err())
	}
	close(silent)
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("dead link not noticed")
	}
}

func TestDialCancelled(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sshconn.Dial(ctx, srv.Config()); err == nil {
		t.Fatal("dial with a cancelled context must fail")
	}
}

// A slow answer to a prompt must not run into the handshake timeout.
func TestPromptStopsHandshakeClock(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	cfg := srv.Config()
	cfg.Auth = sshconn.AuthPassword
	cfg.Timeout = 300 * time.Millisecond
	cfg.Password = func(string) (string, error) {
		time.Sleep(time.Second)
		return "pw", nil
	}
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultKnownHosts(t *testing.T) {
	home := fakeHome(t)
	if got, want := sshconn.DefaultKnownHosts(), filepath.Join(home, ".ssh", "known_hosts"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
