package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/sshconn"
)

func sample() Connection {
	c := NewConnection(rdp.DefaultOptions())
	c.Name = "Office"
	c.Group = "Work"
	c.SSHHost, c.SSHUser = "gate.example.org", "maurits"
	c.RDPHost, c.RDPUser, c.RDPDomain = "localhost", "Administrator", "CORP"
	return c
}

func TestMissingFileIsEmptyStore(t *testing.T) {
	s, err := LoadStore(filepath.Join(t.TempDir(), "nope"))
	if err != nil || s == nil || len(s.Connections) != 0 {
		t.Fatalf("got %v, %v", s, err)
	}
}

func TestEmptyStoreSaves(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "srdp4win") // Save creates it
	if err := (&Store{}).Save(dir); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(dir)
	if err != nil || len(s.Connections) != 0 {
		t.Fatalf("got %v, %v", s, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Store{}
	a := sample()
	a.RDPPassword, a.SavePassword = "s3cret-saved", true
	a.SSHPort, a.RDPPort = 2222, 3390
	a.Options.Resize = rdp.ResizeFixed
	a.Options.Drives, a.Options.DriveLetters = true, "CD"
	a = s.Put(a)
	b := sample()
	b.Name = "Home"
	b.RDPPassword = "s3cret-typed" // SavePassword is off
	b = s.Put(b)
	s.Touch(a.ID)
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(b.ID); got.RDPPassword != "s3cret-typed" {
		t.Fatal("Save must not change the store in memory")
	}

	raw, err := os.ReadFile(filepath.Join(dir, storeFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"s3cret", "gate.example.org", "connections"} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Fatalf("%q is readable in the file", plain)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, storeFile+".tmp")); err == nil {
		t.Fatal("temp file left behind")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, storeFile))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", fi.Mode().Perm())
		}
	}

	got, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Connections) != 2 {
		t.Fatalf("%d connections", len(got.Connections))
	}
	ga, _ := got.Get(a.ID)
	wa, _ := s.Get(a.ID)
	if !ga.LastUsed.Equal(wa.LastUsed) || ga.LastUsed.IsZero() {
		t.Fatalf("last used %v, want %v", ga.LastUsed, wa.LastUsed)
	}
	ga.LastUsed, wa.LastUsed = time.Time{}, time.Time{}
	if ga != wa {
		t.Fatalf("got %+v\nwant %+v", ga, wa)
	}
	gb, _ := got.Get(b.ID)
	if gb.RDPPassword != "" {
		t.Fatal("a password without SavePassword was written")
	}
	if gb.Name != "Home" || gb.Options != rdp.DefaultOptions() {
		t.Fatalf("got %+v", gb)
	}
}

func TestBrokenFileIsKept(t *testing.T) {
	for name, content := range map[string][]byte{
		"garbage": []byte("this is not a connections file"),
		"empty":   {},
		"notjson": mustProtect(t, []byte("{broken")),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, storeFile)
			if err := os.WriteFile(file, content, 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := LoadStore(dir)
			if err == nil {
				t.Fatal("a broken file must give an error")
			}
			if s == nil || len(s.Connections) != 0 {
				t.Fatalf("want an empty store, got %v", s)
			}
			if now, _ := os.ReadFile(file); !bytes.Equal(now, content) {
				t.Fatal("loading changed the broken file")
			}
			s.Put(sample())
			if err := s.Save(dir); err != nil {
				t.Fatal(err)
			}
			if bad, err := os.ReadFile(file + ".bad"); err != nil || !bytes.Equal(bad, content) {
				t.Fatalf("broken file not kept: %v", err)
			}
			if got, err := LoadStore(dir); err != nil || len(got.Connections) != 1 {
				t.Fatalf("got %v, %v", got, err)
			}
			// A good file is replaced without a new .bad file.
			os.Remove(file + ".bad")
			if err := s.Save(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(file + ".bad"); err == nil {
				t.Fatal("a good file was kept as .bad")
			}
		})
	}
}

func mustProtect(t *testing.T, plain []byte) []byte {
	data, err := protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProtect(t *testing.T) {
	if _, err := protect(nil); err == nil {
		t.Fatal("empty input must fail")
	}
	if _, err := unprotect(nil); err == nil {
		t.Fatal("empty file must fail")
	}
	plain := []byte(`{"connections":[]}`)
	data := mustProtect(t, plain)
	if bytes.Contains(data, plain) {
		t.Fatal("not protected")
	}
	back, err := unprotect(data)
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("got %q, %v", back, err)
	}
}

func TestPutGetDelete(t *testing.T) {
	s := &Store{}
	if _, ok := s.Get(""); ok {
		t.Fatal("empty ID found")
	}
	a := s.Put(sample())
	b := s.Put(sample())
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("IDs %q %q", a.ID, b.ID)
	}
	a.Name = "Renamed"
	if got := s.Put(a); got.ID != a.ID || len(s.Connections) != 2 {
		t.Fatalf("replace made %d connections", len(s.Connections))
	}
	if got, ok := s.Get(a.ID); !ok || got.Name != "Renamed" {
		t.Fatalf("got %+v", got)
	}
	c := sample()
	c.ID = "chosen"
	if got := s.Put(c); got.ID != "chosen" || len(s.Connections) != 3 {
		t.Fatal("an unknown ID must be added as it is")
	}
	s.Touch("nope")
	s.Delete("nope")
	s.Delete(a.ID)
	if _, ok := s.Get(a.ID); ok || len(s.Connections) != 2 {
		t.Fatal("not deleted")
	}
	if got, _ := s.Get(b.ID); !got.LastUsed.IsZero() {
		t.Fatal("never used")
	}
	s.Touch(b.ID)
	if got, _ := s.Get(b.ID); time.Since(got.LastUsed) > time.Minute {
		t.Fatal("not touched")
	}
}

func TestValidate(t *testing.T) {
	if err := sample().Validate(); err != nil {
		t.Fatal(err)
	}
	for want, change := range map[string]func(c *Connection){
		"SSH host":       func(c *Connection) { c.SSHHost = " " },
		"SSH user":       func(c *Connection) { c.SSHUser = "" },
		"RDP host":       func(c *Connection) { c.RDPHost = "" },
		"SSH port":       func(c *Connection) { c.SSHPort = 65536 },
		"RDP port":       func(c *Connection) { c.RDPPort = -1 },
		"key file":       func(c *Connection) { c.SSHAuth = sshconn.AuthKeyFile },
		"authentication": func(c *Connection) { c.SSHAuth = "telepathy" },
	} {
		c := sample()
		change(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	for _, change := range []func(c *Connection){
		func(c *Connection) { c.SSHPort, c.RDPPort = 65535, 65535 },
		func(c *Connection) { c.SSHAuth, c.SSHKeyFile = sshconn.AuthKeyFile, `C:\keys\id_ed25519` },
		func(c *Connection) { c.SSHAuth = sshconn.AuthPageant },
		func(c *Connection) { c.SSHAuth = sshconn.AuthPassword },
		func(c *Connection) { c.SSHAuth = sshconn.AuthAuto },
		func(c *Connection) { c.Name, c.RDPUser = "", "" },
	} {
		c := sample()
		change(&c)
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestNames(t *testing.T) {
	c := sample()
	if c.DisplayName() != "Office" {
		t.Fatal(c.DisplayName())
	}
	c.Name = ""
	if got := c.DisplayName(); got != "maurits@gate.example.org > localhost" {
		t.Fatal(got)
	}
	if c.SSHAddr() != "gate.example.org:22" || c.RDPTarget() != "localhost:3389" {
		t.Fatal(c.SSHAddr(), c.RDPTarget())
	}
	c.SSHPort, c.RDPPort, c.RDPHost = 2222, 3390, "fd00::7"
	if c.SSHAddr() != "gate.example.org:2222" || c.RDPTarget() != "[fd00::7]:3390" {
		t.Fatal(c.SSHAddr(), c.RDPTarget())
	}
}

func TestSSHConfig(t *testing.T) {
	c := sample()
	c.SSHPort, c.SSHAuth, c.SSHKeyFile = 2222, sshconn.AuthKeyFile, "key"
	c.RDPPassword = "s3cret"
	got := c.SSHConfig()
	if got.Host != c.SSHHost || got.Port != 2222 || got.User != c.SSHUser ||
		got.Auth != sshconn.AuthKeyFile || got.KeyFile != "key" {
		t.Fatalf("got %+v", got)
	}
	if got.KnownHostsFile != sshconn.DefaultKnownHosts() || got.KnownHostsFile == "" {
		t.Fatalf("known hosts %q", got.KnownHostsFile)
	}
}

func TestPreferences(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "srdp4win")
	def := Preferences{Defaults: rdp.DefaultOptions()}
	if got := LoadPreferences(dir); got != def {
		t.Fatalf("missing file: %+v", got)
	}
	p := def
	p.Theme, p.ToolbarPinned = "dark", true
	p.Defaults.Printers, p.Defaults.Quality = false, rdp.QualityMedium
	p.WindowX, p.WindowY, p.WindowW, p.WindowH, p.Maximized = 10, 20, 1200, 800, true
	if err := p.Save(dir); err != nil {
		t.Fatal(err)
	}
	if got := LoadPreferences(dir); got != p {
		t.Fatalf("got %+v\nwant %+v", got, p)
	}
	file := filepath.Join(dir, prefsFile)
	raw, _ := os.ReadFile(file)
	if !bytes.Contains(raw, []byte("\n  \"theme\": \"dark\"")) {
		t.Fatalf("not plain indented JSON:\n%s", raw)
	}

	// Keys that are missing keep their default.
	os.WriteFile(file, []byte(`{"theme":"light"}`), 0o600)
	want := def
	want.Theme = "light"
	if got := LoadPreferences(dir); got != want {
		t.Fatalf("got %+v", got)
	}
	os.WriteFile(file, []byte(`{"theme":`), 0o600)
	if got := LoadPreferences(dir); got != def {
		t.Fatalf("broken file: %+v", got)
	}
}
