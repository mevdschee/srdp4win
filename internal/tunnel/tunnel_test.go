package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/mevdschee/srdp4win/internal/sshtest"
)

// directDialer connects straight to the target, without SSH.
type directDialer struct{}

func (directDialer) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// open starts an SSH server, an echo server and a tunnel between them.
func open(t testing.TB) (*Tunnel, *sshtest.Server) {
	t.Helper()
	srv := sshtest.Start(t, sshtest.Options{})
	tun, err := Open(srv.Dial(), sshtest.Echo(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tun.Close() })
	return tun, srv
}

func connect(t testing.TB, tun *Tunnel) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", tun.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// echo sends msg and expects it back.
func echo(t testing.TB, conn net.Conn, msg []byte) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	go conn.Write(msg)
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("echo differs")
	}
}

// refused expects the connection to be closed by the tunnel without data.
func refused(t testing.TB, conn net.Conn) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.Write([]byte("x"))
	if n, err := conn.Read(make([]byte, 1)); err == nil || n > 0 {
		t.Fatal("connection was not refused")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection was left open")
	}
}

// waitFor polls until cond holds.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestListensOnLoopback(t *testing.T) {
	tun, _ := open(t)
	if tun.Host() != "127.0.0.1" {
		t.Fatalf("host %q", tun.Host())
	}
	if tun.Port() == 0 || tun.Port() == 3389 {
		t.Fatalf("port %d", tun.Port())
	}
	ip := tun.ln.Addr().(*net.TCPAddr).IP
	if !ip.IsLoopback() {
		t.Fatalf("listening on %v", ip)
	}
	if want := net.JoinHostPort(tun.Host(), strconv.Itoa(tun.Port())); tun.Addr() != want {
		t.Fatalf("addr %q, want %q", tun.Addr(), want)
	}
}

// openAs opens a tunnel with an identity to an echo server.
func openAs(t *testing.T, identity string) *Tunnel {
	t.Helper()
	tun, err := OpenWith(directDialer{}, sshtest.Echo(t), Options{Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tun.Close() })
	return tun
}

func TestIdentityEndpoint(t *testing.T) {
	desk := Identity("Jump.example:22", "desk:3389")
	if desk != Identity("jump.example:22", "DESK:3389") {
		t.Fatal("the identity depends on the case")
	}
	first := openAs(t, desk)
	ip := net.ParseIP(first.Host())
	if !ip.IsLoopback() || first.Host() == loopback {
		t.Fatalf("host %q", first.Host())
	}
	wantIP, wantPort := endpoint(desk)
	if !ip.Equal(wantIP) || first.Port() != wantPort {
		t.Fatalf("listening on %s, want %s:%d", first.Addr(), wantIP, wantPort)
	}
	// It forwards like any tunnel.
	conn := connect(t, first)
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q, %v", buf, err)
	}
	conn.Close()

	// A second tunnel to the same far end: the same address, another port.
	second := openAs(t, desk)
	if second.Host() != first.Host() || second.Port() == first.Port() || second.Port() == 3389 {
		t.Fatalf("second tunnel on %s, first on %s", second.Addr(), first.Addr())
	}
	// The same target behind another SSH host is another computer.
	other := openAs(t, Identity("other.example:22", "desk:3389"))
	if other.Host() == first.Host() {
		t.Fatalf("two far ends on %s", other.Host())
	}
	// Opened again later, it is where it was.
	first.Close()
	again := openAs(t, desk)
	if again.Addr() != first.Addr() {
		t.Fatalf("reopened on %s, was on %s", again.Addr(), first.Addr())
	}
}

func TestEndpointRange(t *testing.T) {
	for i := 0; i < 2000; i++ {
		ip, port := endpoint(Identity("jump:22", "host"+strconv.Itoa(i)+":3389"))
		v4 := ip.To4()
		if v4 == nil || v4[0] != 127 || v4[1] == 0 || v4[1] == 255 || v4[3] == 0 || v4[3] == 255 {
			t.Fatalf("address %v", ip)
		}
		if port < 20000 || port > 39999 {
			t.Fatalf("port %d", port)
		}
	}
}

func TestOpenRejectsBadInput(t *testing.T) {
	if _, err := Open(nil, "localhost:3389"); err == nil {
		t.Fatal("nil dialer accepted")
	}
	if _, err := Open(directDialer{}, "localhost"); err == nil {
		t.Fatal("target without port accepted")
	}
}

func TestForward(t *testing.T) {
	tun, srv := open(t)
	conn := connect(t, tun)
	echo(t, conn, []byte("hello"))
	big := make([]byte, 3<<20) // many buffers
	rand.Read(big)
	echo(t, conn, big)
	if srv.Forwarded() != 1 {
		t.Fatalf("%d channels opened", srv.Forwarded())
	}
}

func TestStats(t *testing.T) {
	tun, _ := open(t)
	if s := tun.Stats(); s != (Stats{}) {
		t.Fatalf("fresh tunnel has stats %+v", s)
	}
	conn := connect(t, tun)
	echo(t, conn, make([]byte, 100000))
	want := Stats{Conns: 1, Active: 1, BytesIn: 100000, BytesOut: 100000}
	// The tunnel counts a buffer after writing it, so the last bytes can
	// be here before they are counted.
	waitFor(t, "the bytes to be counted", func() bool { return tun.Stats() == want })
	conn.Close()
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })
	want.Active = 0
	if s := tun.Stats(); s != want {
		t.Fatalf("stats %+v, want %+v", s, want)
	}
}

func TestOneConnectionAtATime(t *testing.T) {
	tun, _ := open(t)
	first := connect(t, tun)
	echo(t, first, []byte("one"))

	refused(t, connect(t, tun))
	if s := tun.Stats(); s.Refused != 1 || s.Conns != 1 || s.Active != 1 {
		t.Fatalf("stats %+v", s)
	}
	echo(t, first, []byte("still there"))

	// When the first is gone the next one is served.
	first.Close()
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })
	echo(t, connect(t, tun), []byte("two"))
	if s := tun.Stats(); s.Refused != 1 || s.Conns != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestSetAccepting(t *testing.T) {
	tun, srv := open(t)
	tun.SetAccepting(false)
	refused(t, connect(t, tun))
	if s := tun.Stats(); s.Refused != 1 || s.Conns != 0 {
		t.Fatalf("stats %+v", s)
	}
	if srv.Forwarded() != 0 {
		t.Fatal("a refused connection reached the SSH server")
	}
	tun.SetAccepting(true)
	conn := connect(t, tun)
	echo(t, conn, []byte("open again"))

	// Turning it off leaves the open connection alone.
	tun.SetAccepting(false)
	echo(t, conn, []byte("still open"))
}

func TestPeerCheck(t *testing.T) {
	var mu sync.Mutex
	var gotLocal, gotPeer *net.TCPAddr
	allow := false
	old := checkPeer
	checkPeer = func(local, peer *net.TCPAddr) bool {
		mu.Lock()
		defer mu.Unlock()
		gotLocal, gotPeer = local, peer
		return allow
	}
	defer func() { checkPeer = old }()

	tun, srv := open(t)
	conn := connect(t, tun)
	refused(t, conn)
	mu.Lock()
	if gotLocal.Port != tun.Port() || gotPeer.Port != conn.LocalAddr().(*net.TCPAddr).Port {
		t.Fatalf("checked %v <- %v", gotLocal, gotPeer)
	}
	allow = true
	mu.Unlock()
	if s := tun.Stats(); s.Refused != 1 || s.Conns != 0 || srv.Forwarded() != 0 {
		t.Fatalf("stats %+v, %d forwarded", s, srv.Forwarded())
	}
	echo(t, connect(t, tun), []byte("own process"))
}

func TestAnyProcessSkipsPeerCheck(t *testing.T) {
	old := checkPeer
	checkPeer = func(local, peer *net.TCPAddr) bool {
		t.Error("peer checked")
		return false
	}
	defer func() { checkPeer = old }()

	tun, err := OpenWith(directDialer{}, sshtest.Echo(t), Options{AnyProcess: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	echo(t, connect(t, tun), []byte("anyone"))
}

func TestSetDialerAfterDrop(t *testing.T) {
	tun, srv := open(t)
	conn := connect(t, tun)
	echo(t, conn, []byte("before"))

	// The SSH connection drops: the tunnelled connection ends.
	srv.KillClients()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection survived the drop")
	}
	conn.Close()
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })

	// Without a new SSH connection a new attempt fails.
	c2 := connect(t, tun)
	refused(t, c2)
	c2.Close()
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })

	// Log in again and carry on, on the same port.
	port := tun.Port()
	tun.SetDialer(srv.Dial())
	echo(t, connect(t, tun), []byte("after"))
	if tun.Port() != port {
		t.Fatal("port changed")
	}
	if s := tun.Stats(); s.Conns != 3 || s.BytesOut != uint64(len("before")+len("after")) {
		t.Fatalf("stats %+v", s)
	}
}

func TestSetDialerClosesOldConnection(t *testing.T) {
	tun, srv := open(t)
	conn := connect(t, tun)
	echo(t, conn, []byte("old"))
	tun.SetDialer(srv.Dial())
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("old connection survived")
	}
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })
	echo(t, connect(t, tun), []byte("new"))
}

func TestDialFailureClosesConnection(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	tun, err := Open(srv.Dial(), dead)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	refused(t, connect(t, tun))
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })
}

func TestHalfClose(t *testing.T) {
	// A target that reads everything, then answers with the byte count.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		conn.Write([]byte(strconv.Itoa(len(data))))
	}()
	srv := sshtest.Start(t, sshtest.Options{})
	tun, err := Open(srv.Dial(), ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()

	conn := connect(t, tun).(*net.TCPConn)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.Write(make([]byte, 70000))
	conn.CloseWrite()
	answer, err := io.ReadAll(conn)
	if err != nil || string(answer) != "70000" {
		t.Fatalf("answer %q, %v", answer, err)
	}
	waitFor(t, "connection to end", func() bool { return tun.Stats().Active == 0 })
}

func TestClose(t *testing.T) {
	tun, _ := open(t)
	conn := connect(t, tun)
	echo(t, conn, []byte("x"))
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection survived Close")
	}
	if s := tun.Stats(); s.Active != 0 || s.Conns != 1 {
		t.Fatalf("stats %+v", s)
	}
	if c, err := net.DialTimeout("tcp", tun.Addr(), time.Second); err == nil {
		c.Close()
		t.Fatal("still listening after Close")
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// blockingDialer hangs until its context is cancelled.
type blockingDialer struct{ started chan struct{} }

func (b blockingDialer) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCloseCancelsDial(t *testing.T) {
	d := blockingDialer{started: make(chan struct{})}
	tun, err := Open(d, "localhost:3389")
	if err != nil {
		t.Fatal(err)
	}
	connect(t, tun)
	<-d.started
	done := make(chan struct{})
	go func() { tun.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hangs on a pending dial")
	}
}

// The SSH client is what the tunnel dials through.
var _ Dialer = (*sshconn.Client)(nil)

// throughput pushes b.N buffers through the tunnel in the direction of the
// picture: from the target to the local side.
func throughput(b *testing.B, d Dialer) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	const chunk = 64 * 1024
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, chunk)
		for i := 0; i < b.N; i++ {
			if _, err := conn.Write(buf); err != nil {
				return
			}
		}
	}()
	tun, err := Open(d, ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	defer tun.Close()
	conn, err := net.Dial("tcp", tun.Addr())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	b.SetBytes(chunk)
	b.ResetTimer()
	n, err := io.Copy(io.Discard, conn)
	b.StopTimer()
	if err != nil || n != int64(b.N)*chunk {
		b.Fatalf("received %d of %d bytes: %v", n, int64(b.N)*chunk, err)
	}
}

// BenchmarkCopy measures the tunnel's own copy loop, without SSH.
func BenchmarkCopy(b *testing.B) { throughput(b, directDialer{}) }

// BenchmarkSSH measures the whole path: loopback, SSH channel, target.
func BenchmarkSSH(b *testing.B) {
	srv := sshtest.Start(b, sshtest.Options{})
	throughput(b, srv.Dial())
}
