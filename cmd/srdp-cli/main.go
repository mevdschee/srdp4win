// Command srdp-cli opens the SSH tunnel of srdp4win without the app, to test
// against real servers with another RDP client:
//
//	srdp-cli [-p sshport] [-i keyfile] user@sshhost rdphost[:port]
//
// It prints the loopback address to connect to (mstsc /v:127.0.0.1:port or
// xfreerdp /v:127.0.0.1:port) and runs until Ctrl+C, then prints what went
// through. The RDP host is an address as seen from the SSH host.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/mevdschee/srdp4win/internal/tunnel"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func main() {
	cfg := sshconn.Config{Auth: sshconn.AuthAuto}
	flag.IntVar(&cfg.Port, "p", 22, "SSH port")
	flag.StringVar(&cfg.KeyFile, "i", "", "private key file (default: agent, Pageant, default keys, password)")
	flag.StringVar(&cfg.KnownHostsFile, "known-hosts", sshconn.DefaultKnownHosts(), "known_hosts file")
	verbose := flag.Bool("v", false, "log to the terminal")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: srdp-cli [-p sshport] [-i keyfile] user@sshhost rdphost[:port]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	at := strings.LastIndex(flag.Arg(0), "@")
	if at <= 0 || at == len(flag.Arg(0))-1 {
		flag.Usage()
		os.Exit(2)
	}
	user, host := flag.Arg(0)[:at], flag.Arg(0)[at+1:]
	cfg.User, cfg.Host = user, host
	if cfg.KeyFile != "" {
		cfg.Auth = sshconn.AuthKeyFile
	}
	cfg.TrustHost = askTrust
	cfg.Passphrase = askPassphrase
	cfg.Password = askPassword
	target := withPort(flag.Arg(1), "3389")

	// Errors are printed by fail; the log is for -v only.
	level := slog.LevelError + 1
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c, err := sshconn.Dial(ctx, cfg)
	if err != nil {
		fail(err)
	}
	defer c.Close()

	// Another program connects here, so the tunnel is open to every local
	// process for as long as this tool runs.
	t, err := tunnel.OpenWith(c, target, tunnel.Options{AnyProcess: true})
	if err != nil {
		c.Close()
		fail(err)
	}
	start := time.Now()
	fmt.Printf("%s\n", t.Addr())
	fmt.Fprintf(os.Stderr, "Tunnel to %s through %s@%s is open. Press Ctrl+C to stop.\n", target, user, host)

	var lost error
	select {
	case <-ctx.Done():
	case <-c.Done():
		lost = c.Err()
	}
	t.Close()
	s := t.Stats()
	fmt.Fprintf(os.Stderr, "\n%d connection(s), %d refused, %s received, %s sent, open for %s\n",
		s.Conns, s.Refused, size(s.BytesIn), size(s.BytesOut), time.Since(start).Round(time.Second))
	if lost != nil {
		c.Close()
		fail(lost)
	}
}

// withPort adds the default port to an address without one.
func withPort(addr, port string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	// A bare IPv6 address may come with or without brackets.
	return net.JoinHostPort(strings.Trim(addr, "[]"), port)
}

// size formats a byte count.
func size(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func askTrust(host string, key ssh.PublicKey) bool {
	fmt.Fprintf(os.Stderr, "Unknown host %s\n%s key fingerprint %s\nTrust and remember it? [y/N] ",
		host, key.Type(), sshconn.Fingerprint(key))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

func askPassphrase(keyFile string) ([]byte, error) {
	fmt.Fprintf(os.Stderr, "Passphrase for %s: ", keyFile)
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return pass, err
}

func askPassword(prompt string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", strings.TrimSuffix(strings.TrimSpace(prompt), ":"))
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(pass), err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
