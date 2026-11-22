package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mevdschee/srdp4win/internal/sshtest"
)

func TestWithPort(t *testing.T) {
	for in, want := range map[string]string{
		"localhost":      "localhost:3389",
		"10.0.0.5:3390":  "10.0.0.5:3390",
		"::1":            "[::1]:3389",
		"[::1]":          "[::1]:3389",
		"[fe80::1]:3390": "[fe80::1]:3390",
	} {
		if got := withPort(in, "3389"); got != want {
			t.Errorf("withPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSize(t *testing.T) {
	for in, want := range map[uint64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 5 << 20: "5.0 MiB"} {
		if got := size(in); got != want {
			t.Errorf("size(%d) = %q, want %q", in, got, want)
		}
	}
}

// The built tool against the test server: tunnel, echo, Ctrl+C, stats.
func TestRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs an interrupt signal")
	}
	bin := filepath.Join(t.TempDir(), "srdp-cli")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	srv := sshtest.Start(t, sshtest.Options{})
	cfg := srv.Config()
	line := sshtest.KnownHostsLine(srv.Addr, srv.HostKey.PublicKey())
	if err := os.WriteFile(cfg.KnownHostsFile, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-p", strconv.Itoa(cfg.Port), "-i", cfg.KeyFile, "-known-hosts", cfg.KnownHostsFile,
		cfg.User+"@"+cfg.Host, sshtest.Echo(t))
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	addr, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("no address printed: %v\n%s", err, stderr.String())
	}

	// A connection from another process (this test) is let in.
	conn, err := net.Dial("tcp", strings.TrimSpace(addr))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.Write([]byte("ping"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo %q: %v\n%s", got, err, stderr.String())
	}
	conn.Close()

	cmd.Process.Signal(os.Interrupt)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit: %v\n%s", err, stderr.String())
	}
	if out := stderr.String(); !strings.Contains(out, "1 connection(s), 0 refused, 4 B received, 4 B sent") {
		t.Fatalf("stats missing:\n%s", out)
	}
}
