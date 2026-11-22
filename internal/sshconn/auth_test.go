package sshconn_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/mevdschee/srdp4win/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

// fakeHome points the default key lookup (~/.ssh) at a temp dir.
func fakeHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	// No agent in tests.
	t.Setenv("SSH_AUTH_SOCK", "")
	return home
}

func dial(cfg sshconn.Config) error {
	c, err := sshconn.Dial(context.Background(), cfg)
	if err == nil {
		c.Close()
	}
	return err
}

func TestPasswordAuth(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{Password: "s3cret"})
	cfg := srv.Config()
	cfg.Auth = sshconn.AuthPassword
	asked := 0
	cfg.Password = func(string) (string, error) { asked++; return "s3cret", nil }
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("password asked %d times", asked)
	}
	cfg.Password = func(string) (string, error) { return "wrong", nil }
	if err := dial(cfg); err == nil {
		t.Fatal("wrong password must fail")
	}
}

func TestAutoUsesDefaultKeyFile(t *testing.T) {
	home := fakeHome(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pem.EncodeToMemory(block), 0o600)

	srv := sshtest.Start(t, sshtest.Options{ExtraKeys: []ssh.PublicKey{signer.PublicKey()}})
	cfg := srv.Config()
	cfg.Auth, cfg.KeyFile = sshconn.AuthAuto, ""
	cfg.Password = func(string) (string, error) { t.Fatal("no password prompt expected"); return "", nil }
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestAutoAsksPassphraseOnlyForAcceptedEncryptedKey(t *testing.T) {
	home := fakeHome(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pp"))
	keyFile := filepath.Join(home, ".ssh", "id_ed25519")
	os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
	os.WriteFile(keyFile+".pub", ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644)

	// Server that does not accept the key: no passphrase prompt, password used.
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	cfg := srv.Config()
	cfg.Auth, cfg.KeyFile = sshconn.AuthAuto, ""
	cfg.Passphrase = func(string) ([]byte, error) {
		t.Fatal("passphrase must not be asked for a rejected key")
		return nil, nil
	}
	cfg.Password = func(string) (string, error) { return "pw", nil }
	if err := dial(cfg); err != nil {
		t.Fatalf("auto should fall back to the password: %v", err)
	}

	// Server that accepts the key: the passphrase is asked once.
	srv = sshtest.Start(t, sshtest.Options{ExtraKeys: []ssh.PublicKey{signer.PublicKey()}})
	cfg = srv.Config()
	cfg.Auth, cfg.KeyFile = sshconn.AuthAuto, ""
	asked := 0
	cfg.Passphrase = func(string) ([]byte, error) { asked++; return []byte("pp"), nil }
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("passphrase asked %d times", asked)
	}
}

func TestAutoWithNothingAvailable(t *testing.T) {
	fakeHome(t)
	srv := sshtest.Start(t, sshtest.Options{})
	cfg := srv.Config()
	cfg.Auth, cfg.KeyFile = sshconn.AuthAuto, ""
	if err := dial(cfg); !errors.Is(err, sshconn.ErrNoKeys) {
		t.Fatalf("want ErrNoKeys, got %v", err)
	}
}

func TestPageantOnlyOnWindows(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	cfg := srv.Config()
	cfg.Auth = sshconn.AuthPageant
	if err := dial(cfg); err == nil {
		t.Fatal("Pageant is not available here")
	}
}

func TestKeyboardInteractiveAuth(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{Password: "s3cret", KeyboardInteractive: true})
	cfg := srv.Config()
	cfg.Auth = sshconn.AuthPassword
	asked := 0
	cfg.Password = func(string) (string, error) { asked++; return "s3cret", nil }
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("password asked %d times", asked)
	}
}

func TestKeyFileAuth(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{})
	if err := dial(srv.Config()); err != nil {
		t.Fatal(err)
	}
	// A key the server does not know.
	_, otherPEM := sshtest.NewKey(t)
	cfg := srv.Config()
	cfg.KeyFile = filepath.Join(t.TempDir(), "other")
	os.WriteFile(cfg.KeyFile, otherPEM, 0o600)
	if err := dial(cfg); err == nil {
		t.Fatal("dial with unknown client key must fail")
	}
}

func TestEncryptedKeyFileAsksPassphrase(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pp"))
	srv := sshtest.Start(t, sshtest.Options{ExtraKeys: []ssh.PublicKey{signer.PublicKey()}})
	cfg := srv.Config()
	cfg.KeyFile = filepath.Join(t.TempDir(), "enc")
	os.WriteFile(cfg.KeyFile, pem.EncodeToMemory(block), 0o600)
	if err := dial(cfg); err == nil {
		t.Fatal("encrypted key without a passphrase prompt must fail")
	}
	cfg.Passphrase = func(string) ([]byte, error) { return []byte("pp"), nil }
	if err := dial(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Passphrase = func(string) ([]byte, error) { return []byte("wrong"), nil }
	if err := dial(cfg); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
}
