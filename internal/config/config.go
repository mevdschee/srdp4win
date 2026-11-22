// Package config stores the connections and the preferences in
// %APPDATA%\srdp4win. The connections, with any saved RDP passwords, are in
// connections.dat, encrypted with DPAPI as a whole (like otp4win's
// accounts.dat). Preferences are not secret and go to settings.json.
//
// The exported names in this file are the contract with the UI.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/sshconn"
)

// Connection is one saved connection: an SSH host and the RDP host behind it.
type Connection struct {
	ID    string `json:"id"` // stable, never shown
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`

	SSHHost    string `json:"sshHost"`
	SSHPort    int    `json:"sshPort,omitempty"` // 0 is 22
	SSHUser    string `json:"sshUser"`
	SSHAuth    string `json:"sshAuth,omitempty"` // sshconn.AuthAuto (default), AuthKeyFile, AuthPageant, AuthPassword
	SSHKeyFile string `json:"sshKeyFile,omitempty"`

	RDPHost      string `json:"rdpHost"`           // as seen from the SSH host; required, no default
	RDPPort      int    `json:"rdpPort,omitempty"` // 0 is 3389
	RDPUser      string `json:"rdpUser,omitempty"`
	RDPDomain    string `json:"rdpDomain,omitempty"`
	RDPPassword  string `json:"rdpPassword,omitempty"` // only when SavePassword
	SavePassword bool   `json:"savePassword,omitempty"`

	Options  rdp.Options `json:"options"`
	LastUsed time.Time   `json:"lastUsed,omitempty"`
}

// Store is the content of connections.dat.
type Store struct {
	Connections []Connection `json:"connections"`
}

// Preferences is the content of settings.json.
type Preferences struct {
	Theme         string      `json:"theme,omitempty"` // "", "light" or "dark"; "" follows Windows
	ToolbarPinned bool        `json:"toolbarPinned"`   // the floating full screen toolbar stays visible
	Defaults      rdp.Options `json:"defaults"`        // options of a new connection
	WindowX       int         `json:"windowX,omitempty"`
	WindowY       int         `json:"windowY,omitempty"`
	WindowW       int         `json:"windowW,omitempty"`
	WindowH       int         `json:"windowH,omitempty"`
	Maximized     bool        `json:"maximized,omitempty"`
}

const (
	storeFile = "connections.dat"
	prefsFile = "settings.json"
)

// Dir returns the settings directory (%APPDATA%\srdp4win).
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "srdp4win"), nil
}

// LoadStore reads and decrypts connections.dat in dir. A missing file is an
// empty store. A file that cannot be read gives an empty store and an error;
// the file itself is kept (see Save).
func LoadStore(dir string) (*Store, error) {
	data, err := os.ReadFile(filepath.Join(dir, storeFile))
	if errors.Is(err, fs.ErrNotExist) {
		return &Store{}, nil
	}
	if err != nil {
		return &Store{}, err
	}
	s, err := decode(data)
	if err != nil {
		return &Store{}, fmt.Errorf("%s: %w", storeFile, err)
	}
	return s, nil
}

func decode(data []byte) (*Store, error) {
	plain, err := unprotect(data)
	if err != nil {
		return nil, err
	}
	s := &Store{}
	if err := json.Unmarshal(plain, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Save encrypts and writes connections.dat (temp file, then rename).
// Passwords of connections without SavePassword are not written. An existing
// file that cannot be decrypted or parsed is not lost: it is kept as
// connections.dat.bad.
func (s *Store) Save(dir string) error {
	out := Store{Connections: make([]Connection, len(s.Connections))}
	for i, c := range s.Connections {
		if !c.SavePassword {
			c.RDPPassword = ""
		}
		out.Connections[i] = c
	}
	plain, err := json.Marshal(out)
	if err != nil {
		return err
	}
	data, err := protect(plain)
	if err != nil {
		return err
	}
	file := filepath.Join(dir, storeFile)
	if old, err := os.ReadFile(file); err == nil {
		if _, err := decode(old); err != nil {
			if err := os.Rename(file, file+".bad"); err != nil {
				return err
			}
		}
	}
	return writeFile(file, data)
}

// writeFile writes through a temp file, so a crash never leaves half a file.
func writeFile(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func (s *Store) index(id string) int {
	if id == "" {
		return -1
	}
	for i := range s.Connections {
		if s.Connections[i].ID == id {
			return i
		}
	}
	return -1
}

// Get returns the connection with the ID.
func (s *Store) Get(id string) (Connection, bool) {
	if i := s.index(id); i >= 0 {
		return s.Connections[i], true
	}
	return Connection{}, false
}

// Put adds c (when its ID is empty or unknown; an empty ID gets a new one) or
// replaces the connection with the same ID, and returns it as stored.
func (s *Store) Put(c Connection) Connection {
	if c.ID == "" {
		c.ID = newID()
	}
	if i := s.index(c.ID); i >= 0 {
		s.Connections[i] = c
	} else {
		s.Connections = append(s.Connections, c)
	}
	return c
}

// Delete removes the connection with the ID.
func (s *Store) Delete(id string) {
	if i := s.index(id); i >= 0 {
		s.Connections = append(s.Connections[:i], s.Connections[i+1:]...)
	}
}

// Touch sets LastUsed of the connection to now.
func (s *Store) Touch(id string) {
	if i := s.index(id); i >= 0 {
		s.Connections[i].LastUsed = time.Now()
	}
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// NewConnection returns an unsaved connection with the given default options.
func NewConnection(defaults rdp.Options) Connection { return Connection{Options: defaults} }

// Validate reports the first problem that keeps c from being saved or used.
// The SSH host, SSH user and RDP host are required: there is no direct RDP.
func (c Connection) Validate() error {
	switch {
	case strings.TrimSpace(c.SSHHost) == "":
		return errors.New("the SSH host is required")
	case strings.TrimSpace(c.SSHUser) == "":
		return errors.New("the SSH user is required")
	case strings.TrimSpace(c.RDPHost) == "":
		return errors.New("the RDP host is required (localhost when it is the SSH host itself)")
	case c.SSHPort < 0 || c.SSHPort > 65535:
		return errors.New("the SSH port must be between 0 and 65535")
	case c.RDPPort < 0 || c.RDPPort > 65535:
		return errors.New("the RDP port must be between 0 and 65535")
	}
	switch c.SSHAuth {
	case "", sshconn.AuthAuto, sshconn.AuthPageant, sshconn.AuthPassword:
	case sshconn.AuthKeyFile:
		if strings.TrimSpace(c.SSHKeyFile) == "" {
			return errors.New("the key file is required")
		}
	default:
		return fmt.Errorf("unknown SSH authentication %q", c.SSHAuth)
	}
	return nil
}

// DisplayName is the name, or "user@sshhost > rdphost" when it has none.
func (c Connection) DisplayName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.SSHUser + "@" + c.SSHHost + " > " + c.RDPHost
}

// SSHAddr is the SSH host with its port, for display.
func (c Connection) SSHAddr() string { return hostPort(c.SSHHost, c.SSHPort, 22) }

// RDPTarget is "host:port" of the RDP host as seen from the SSH host.
func (c Connection) RDPTarget() string { return hostPort(c.RDPHost, c.RDPPort, 3389) }

func hostPort(host string, port, def int) string {
	if port == 0 {
		port = def
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// SSHConfig returns the login settings without the prompt callbacks, which
// the caller fills in. known_hosts is the user's OpenSSH file, shared with
// other SSH tools.
func (c Connection) SSHConfig() sshconn.Config {
	return sshconn.Config{
		Host:           c.SSHHost,
		Port:           c.SSHPort,
		User:           c.SSHUser,
		Auth:           c.SSHAuth,
		KeyFile:        c.SSHKeyFile,
		KnownHostsFile: sshconn.DefaultKnownHosts(),
	}
}

// LoadPreferences reads settings.json in dir. A missing or broken file gives
// the defaults (Defaults = rdp.DefaultOptions()).
func LoadPreferences(dir string) Preferences {
	def := Preferences{Defaults: rdp.DefaultOptions()}
	data, err := os.ReadFile(filepath.Join(dir, prefsFile))
	if err != nil {
		return def
	}
	p := def
	if err := json.Unmarshal(data, &p); err != nil {
		return def
	}
	return p
}

// Save writes settings.json.
func (p Preferences) Save(dir string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, prefsFile), data)
}
