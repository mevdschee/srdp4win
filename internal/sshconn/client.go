package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrClosed is returned by Err after Close was called.
var ErrClosed = errors.New("connection closed")

// preferredCiphers are the hardware-accelerated AEADs, fastest first.
var preferredCiphers = []string{
	"aes128-gcm@openssh.com",
	"aes256-gcm@openssh.com",
	"chacha20-poly1305@openssh.com",
}

// Client is one SSH connection. It is safe for concurrent use; channels are
// multiplexed over the single connection.
type Client struct {
	ssh   *ssh.Client
	agent io.Closer

	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

// Dial connects, logs in and verifies the host key.
func Dial(ctx context.Context, cfg Config) (c *Client, err error) {
	slog.Info("dial", "addr", cfg.addr(), "user", cfg.User, "auth", cfg.Auth, "keyFile", cfg.KeyFile)
	defer func() {
		if err != nil {
			slog.Error("dial failed", "addr", cfg.addr(), "err", err)
		}
	}()
	// Prompts wait for the user, so they stop the handshake clock.
	hs := &handshake{timeout: cfg.timeout()}
	cfg = hs.wrap(cfg)

	auth, agentConn, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	closeAgent := func() {
		if agentConn != nil {
			agentConn.Close()
		}
	}

	hostKey, err := hostKeyCallback(cfg)
	if err != nil {
		closeAgent()
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		Config:          ssh.Config{Ciphers: ciphers()},
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hostKey,
		Timeout:         cfg.timeout(),
	}

	dialer := net.Dialer{Timeout: cfg.timeout()}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.addr())
	if err != nil {
		closeAgent()
		return nil, fmt.Errorf("connect %s: %w", cfg.addr(), err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	hs.start(conn)
	// A cancelled context aborts the handshake.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cc, chans, reqs, err := ssh.NewClientConn(conn, cfg.addr(), sshCfg)
	stop()
	if err != nil {
		conn.Close()
		closeAgent()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	hs.stop()
	sshClient := ssh.NewClient(cc, chans, reqs)
	slog.Info("connected", "addr", cfg.addr(), "server", string(cc.ServerVersion()))

	c = &Client{
		ssh:   sshClient,
		agent: agentConn,
		done:  make(chan struct{}),
	}
	go func() {
		err := sshClient.Wait()
		if err == nil {
			err = io.EOF
		}
		c.fail(fmt.Errorf("connection lost: %w", err))
	}()
	if ka := cfg.keepAlive(); ka > 0 {
		go c.keepAlive(ka)
	}
	return c, nil
}

// handshake bounds the time the SSH handshake may take, not counting the
// time the user spends answering a prompt.
type handshake struct {
	timeout time.Duration
	mu      sync.Mutex
	conn    net.Conn
	prompts int
}

func (h *handshake) start(conn net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conn = conn
	h.arm()
}

func (h *handshake) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != nil {
		_ = h.conn.SetDeadline(time.Time{})
	}
	h.conn = nil
}

// arm sets the deadline, or clears it while a prompt is open.
func (h *handshake) arm() {
	if h.conn == nil {
		return
	}
	if h.prompts > 0 {
		_ = h.conn.SetDeadline(time.Time{})
		return
	}
	_ = h.conn.SetDeadline(time.Now().Add(h.timeout))
}

func (h *handshake) prompt(delta int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompts += delta
	h.arm()
}

// wrap returns cfg with its prompts pausing the clock.
func (h *handshake) wrap(cfg Config) Config {
	if f := cfg.Password; f != nil {
		cfg.Password = func(prompt string) (string, error) {
			h.prompt(1)
			defer h.prompt(-1)
			return f(prompt)
		}
	}
	if f := cfg.Passphrase; f != nil {
		cfg.Passphrase = func(keyFile string) ([]byte, error) {
			h.prompt(1)
			defer h.prompt(-1)
			return f(keyFile)
		}
	}
	if f := cfg.TrustHost; f != nil {
		cfg.TrustHost = func(hostport string, key ssh.PublicKey) bool {
			h.prompt(1)
			defer h.prompt(-1)
			return f(hostport, key)
		}
	}
	return cfg
}

// ciphers lists the preferred ciphers first, then the library defaults.
func ciphers() []string {
	var def ssh.Config
	def.SetDefaults()
	list := append([]string(nil), preferredCiphers...)
next:
	for _, c := range def.Ciphers {
		for _, p := range preferredCiphers {
			if c == p {
				continue next
			}
		}
		list = append(list, c)
	}
	return list
}

// DialTCP opens a direct-tcpip channel to addr ("host:port" as seen from the
// SSH host). Cancelling ctx aborts the open; it does not close an open
// connection.
func (c *Client) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	conn, err := c.ssh.DialContext(ctx, "tcp", addr)
	if err != nil {
		if e := c.Err(); e != nil && ctx.Err() == nil {
			return nil, e
		}
		return nil, fmt.Errorf("forward to %s: %w", addr, err)
	}
	return conn, nil
}

// Done is closed when the connection is closed or lost.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended, or nil while it is up.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close closes the SSH connection and every channel on it.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Client) fail(err error) {
	c.closeOnce.Do(func() {
		if errors.Is(err, ErrClosed) {
			slog.Debug("connection closed")
		} else {
			slog.Warn("connection ended", "err", err)
		}
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		c.ssh.Close()
		if c.agent != nil {
			c.agent.Close()
		}
		close(c.done)
	})
}

// keepAlive pings the server. A ping that fails or is not answered within
// one interval means the link is dead, and the client is closed.
func (c *Client) keepAlive(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		reply := make(chan error, 1)
		go func() {
			_, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil)
			reply <- err
		}()
		select {
		case <-c.done:
			return
		case err := <-reply:
			if err != nil {
				c.fail(fmt.Errorf("keepalive failed: %w", err))
				return
			}
		case <-time.After(interval):
			c.fail(errors.New("keepalive timed out"))
			return
		}
	}
}

// DefaultKnownHosts is the user's OpenSSH known_hosts file.
func DefaultKnownHosts() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

// Fingerprint is the SHA256 fingerprint of a key, as OpenSSH prints it.
func Fingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }
