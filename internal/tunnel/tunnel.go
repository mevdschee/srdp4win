// Package tunnel forwards a loopback TCP port through an SSH connection to
// the RDP host.
//
// The tunnel is the only code in the data path of a session: a plain byte
// copy in both directions, nothing buffered and nothing inspected.
package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// loopback is the address a tunnel without an identity listens on.
const loopback = "127.0.0.1"

// rdpPort is never used: mstscax refuses a loopback connection to it, as it
// looks like the local console.
const rdpPort = 3389

// bufSize is the copy buffer: the SSH packet size.
const bufSize = 32 * 1024

// Dialer opens a connection to addr as seen from the SSH host.
// *sshconn.Client is one.
type Dialer interface {
	DialTCP(ctx context.Context, addr string) (net.Conn, error)
}

// Stats counts what went through a tunnel.
type Stats struct {
	Conns    int    // connections accepted so far
	Active   int    // connections open now
	BytesIn  uint64 // from the RDP host
	BytesOut uint64 // to the RDP host
	Refused  int    // connections refused (not accepting, busy, wrong process)
}

// Options change how a tunnel is opened; see OpenWith.
type Options struct {
	// AnyProcess lets every local program connect, not only this process.
	// The command-line tool sets it, since mstsc or xfreerdp connect there.
	AnyProcess bool
	// Identity names the far end of the tunnel; see the function Identity.
	// With it the tunnel listens on a loopback address and port of its own,
	// derived from the name and so the same every time. An RDP client
	// remembers the certificate it trusted by the address it connects to:
	// behind 127.0.0.1 every server looks like the same computer whose
	// certificate keeps changing, behind an address of its own each server
	// is remembered by itself. Empty is 127.0.0.1 and a random port.
	Identity string
}

// Identity is the name of the far end of a tunnel: the SSH host ("host:port")
// and the target behind it. The target alone does not say which machine it
// is, localhost is another one behind every SSH host.
func Identity(sshAddr, target string) string {
	return strings.ToLower(sshAddr + " " + target)
}

// checkPeer reports whether the accepted connection local<-peer was made by
// our own process. Tests swap it.
var checkPeer = ownProcess

// Tunnel is a loopback listener that forwards to one target.
type Tunnel struct {
	ln     *net.TCPListener
	host   string
	port   int
	target string
	opts   Options
	opened time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	bytesIn  atomic.Uint64
	bytesOut atomic.Uint64

	mu        sync.Mutex
	dialer    Dialer
	accepting bool
	closed    bool
	conns     int
	refused   int
	active    map[net.Conn]struct{} // local and remote ends of open connections
	busy      int                   // connections open or being opened
}

// Open listens on a loopback port (never 3389) and forwards accepted
// connections through d to target ("host:port" as seen from the SSH host).
// A new tunnel accepts; see SetAccepting.
func Open(d Dialer, target string) (*Tunnel, error) {
	return OpenWith(d, target, Options{})
}

// OpenWith is Open with options.
func OpenWith(d Dialer, target string, o Options) (*Tunnel, error) {
	if d == nil {
		return nil, errors.New("tunnel: no SSH connection")
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, fmt.Errorf("tunnel target %q: %w", target, err)
	}
	ln, err := listen(o.Identity)
	if err != nil {
		return nil, fmt.Errorf("tunnel listen: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tunnel{
		ln:        ln,
		host:      ln.Addr().(*net.TCPAddr).IP.String(),
		port:      ln.Addr().(*net.TCPAddr).Port,
		target:    target,
		opts:      o,
		opened:    time.Now(),
		ctx:       ctx,
		cancel:    cancel,
		dialer:    d,
		accepting: true,
		active:    map[net.Conn]struct{}{},
	}
	slog.Info("tunnel open", "listen", t.Addr(), "target", target)
	t.wg.Add(1)
	go t.acceptLoop()
	return t, nil
}

// listen opens the tunnel's port. With an identity that is the endpoint
// belonging to it; a random port on the same address when the port is taken
// (a second tunnel to the same far end); and 127.0.0.1 where the address
// cannot be used at all.
func listen(identity string) (*net.TCPListener, error) {
	if identity != "" {
		ip, port := endpoint(identity)
		for _, p := range []int{port, 0} {
			ln, err := listenOn(ip, p)
			if err == nil {
				return ln, nil
			}
			slog.Info("tunnel: own endpoint not free", "listen", net.JoinHostPort(ip.String(), strconv.Itoa(p)), "err", err)
		}
	}
	return listenOn(net.ParseIP(loopback), 0)
}

// endpoint is the loopback address and port that belong to an identity. All
// of 127.0.0.0/8 is the local computer, so a hash of the identity picks one
// of 16 million addresses, from 127.1.0.1 to 127.254.255.254. Two identities
// on one address would only mean being asked about a certificate again. The
// port is fixed as well, in case a client remembers by address and port.
func endpoint(identity string) (net.IP, int) {
	sum := sha256.Sum256([]byte(identity))
	ip := net.IPv4(127, 1+sum[0]%254, sum[1], 1+sum[2]%254)
	port := 20000 + int(binary.BigEndian.Uint16(sum[3:5]))%20000
	return ip, port
}

// listenOn listens on a loopback address. Port 0 is a random port that is
// not the RDP port.
func listenOn(ip net.IP, port int) (*net.TCPListener, error) {
	var skipped []*net.TCPListener
	defer func() {
		for _, ln := range skipped {
			ln.Close()
		}
	}()
	for {
		ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: port})
		if err != nil {
			return nil, err
		}
		if ln.Addr().(*net.TCPAddr).Port != rdpPort {
			return ln, nil
		}
		// Keep it open until the next try, so that one gets another port.
		skipped = append(skipped, ln)
	}
}

// Host is the loopback address to give to the RDP control.
func (t *Tunnel) Host() string { return t.host }

// Port is the listening port.
func (t *Tunnel) Port() int { return t.port }

// Addr is Host:Port.
func (t *Tunnel) Addr() string { return net.JoinHostPort(t.host, strconv.Itoa(t.port)) }

// SetAccepting turns accepting on or off. While off, and while a connection
// is open, new connections are closed at once.
func (t *Tunnel) SetAccepting(on bool) {
	t.mu.Lock()
	t.accepting = on
	t.mu.Unlock()
}

// SetDialer replaces the SSH connection after a reconnect. A connection that
// is still open on the old one is closed.
func (t *Tunnel) SetDialer(d Dialer) {
	t.mu.Lock()
	t.dialer = d
	old := make([]net.Conn, 0, len(t.active))
	for c := range t.active {
		old = append(old, c)
	}
	t.mu.Unlock()
	for _, c := range old {
		c.Close()
	}
}

// Stats returns the counters.
func (t *Tunnel) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Stats{
		Conns:    t.conns,
		Active:   t.busy,
		BytesIn:  t.bytesIn.Load(),
		BytesOut: t.bytesOut.Load(),
		Refused:  t.refused,
	}
}

// Close stops listening and closes the open connections.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.accepting = false
	open := make([]net.Conn, 0, len(t.active))
	for c := range t.active {
		open = append(open, c)
	}
	t.mu.Unlock()

	t.cancel()
	err := t.ln.Close()
	for _, c := range open {
		c.Close()
	}
	t.wg.Wait()
	s := t.Stats()
	slog.Info("tunnel closed", "listen", t.Addr(), "target", t.target, "conns", s.Conns, "refused", s.Refused,
		"bytesIn", s.BytesIn, "bytesOut", s.BytesOut, "duration", time.Since(t.opened).Round(time.Millisecond))
	return err
}

func (t *Tunnel) acceptLoop() {
	defer t.wg.Done()
	for {
		conn, err := t.ln.AcceptTCP()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			slog.Warn("tunnel accept", "err", err)
			select {
			case <-t.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		d, reason := t.admit(conn)
		if reason != "" {
			slog.Warn("tunnel connection refused", "listen", t.Addr(), "reason", reason)
			refuse(conn)
			continue
		}
		t.wg.Add(1)
		go t.serve(conn, d)
	}
}

// admit decides whether an accepted connection may use the tunnel. It
// returns the dialer to use, or the reason for refusing.
func (t *Tunnel) admit(conn *net.TCPConn) (Dialer, string) {
	reason := ""
	if !t.opts.AnyProcess {
		peer, _ := conn.RemoteAddr().(*net.TCPAddr)
		local, _ := conn.LocalAddr().(*net.TCPAddr)
		if peer == nil || local == nil || !checkPeer(local, peer) {
			reason = "not our process"
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case reason != "":
	case t.closed:
		reason = "closed"
	case !t.accepting:
		reason = "not accepting"
	case t.busy > 0:
		reason = "busy"
	}
	if reason != "" {
		t.refused++
		return nil, reason
	}
	t.conns++
	t.busy++
	t.active[conn] = struct{}{}
	return t.dialer, ""
}

// refuse closes a connection at once, with a reset instead of a lingering
// close, so the other side sees the refusal immediately.
func refuse(conn *net.TCPConn) {
	_ = conn.SetLinger(0)
	conn.Close()
}

// track registers the remote end of a connection, so Close can reach it. It
// reports false when the tunnel was closed meanwhile.
func (t *Tunnel) track(remote net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.active[remote] = struct{}{}
	return true
}

// serve forwards one connection until both directions are done.
func (t *Tunnel) serve(local *net.TCPConn, d Dialer) {
	defer t.wg.Done()
	start := time.Now()
	in0, out0 := t.bytesIn.Load(), t.bytesOut.Load()
	var remote net.Conn
	defer func() {
		local.Close()
		if remote != nil {
			remote.Close()
		}
		t.mu.Lock()
		delete(t.active, local)
		if remote != nil {
			delete(t.active, remote)
		}
		t.busy--
		t.mu.Unlock()
	}()
	_ = local.SetNoDelay(true)

	remote, err := d.DialTCP(t.ctx, t.target)
	if err != nil {
		slog.Warn("tunnel forward failed", "target", t.target, "err", err)
		return
	}
	if !t.track(remote) {
		return
	}
	slog.Info("tunnel connection open", "listen", t.Addr(), "target", t.target)

	done := make(chan struct{})
	go func() {
		defer close(done)
		pump(local, remote, &t.bytesIn) // RDP host to control
	}()
	pump(remote, local, &t.bytesOut) // control to RDP host
	<-done
	slog.Info("tunnel connection closed", "listen", t.Addr(), "target", t.target,
		"bytesIn", t.bytesIn.Load()-in0, "bytesOut", t.bytesOut.Load()-out0,
		"duration", time.Since(start).Round(time.Millisecond))
}

// closeWriter is the half-close of a TCP connection or an SSH channel.
type closeWriter interface {
	CloseWrite() error
}

// pump copies src to dst until src ends, counting the bytes. At a clean end
// it passes the end on (half-close) and leaves the other direction running;
// on an error it closes both ends, which stops the other direction too.
func pump(dst, src net.Conn, count *atomic.Uint64) {
	buf := make([]byte, bufSize)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				dst.Close()
				src.Close()
				return
			}
			count.Add(uint64(n))
		}
		if rerr == nil {
			continue
		}
		if rerr == io.EOF {
			if cw, ok := dst.(closeWriter); ok && cw.CloseWrite() == nil {
				return
			}
		}
		dst.Close()
		src.Close()
		return
	}
}
