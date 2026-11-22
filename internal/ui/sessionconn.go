//go:build windows

package ui

// The connect flow of a session page: log in to the SSH host, open the
// tunnel, connect the RDP backend to the tunnel's loopback port, and get
// all of it back when the link drops. SSH and the tunnel run on goroutines
// and report with sh.Post; everything touching the backend runs on the UI
// thread. The backend is never given anything but the tunnel's address.

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mevdschee/srdp4win/internal/rdp"
	"github.com/mevdschee/srdp4win/internal/rdp/fake"
	"github.com/mevdschee/srdp4win/internal/rdp/mstsc"
	"github.com/mevdschee/srdp4win/internal/sshconn"
	"github.com/mevdschee/srdp4win/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// post runs f on the UI thread unless the page is gone by then.
func (p *sessionPage) post(f func()) {
	p.sh.Post(func() {
		if !p.dead {
			f()
		}
	})
}

// onUI runs f on the UI thread and waits for it, from a goroutine. False
// when the tab closed first.
func (p *sessionPage) onUI(ctx context.Context, f func()) bool {
	done := make(chan bool, 1)
	p.sh.Post(func() {
		if p.dead {
			done <- false
			return
		}
		f()
		done <- true
	})
	select {
	case ok := <-done:
		return ok
	case <-ctx.Done():
		return false
	}
}

// sshConfig is the connection's SSH login with prompts that ask on the UI
// thread. Cancelled is set when the user dismissed one of them.
func (p *sessionPage) sshConfig(ctx context.Context, cancelled *atomic.Bool) sshconn.Config {
	cfg := p.c.SSHConfig()
	if cfg.KnownHostsFile == "" {
		cfg.KnownHostsFile = sshconn.DefaultKnownHosts()
	}
	who := p.c.SSHUser + "@" + p.c.SSHAddr()
	ask := func(title, prompt string) (string, bool) {
		var answer string
		var ok bool
		shown := p.onUI(ctx, func() {
			p.loggingIn()
			answer, _, ok = p.sh.AskPassword(title, prompt, false)
		})
		if !shown || !ok {
			cancelled.Store(true)
			return "", false
		}
		return answer, true
	}
	cfg.Password = func(prompt string) (string, error) {
		if prompt == "" {
			prompt = "Password for " + who
		}
		answer, ok := ask("SSH login", prompt)
		if !ok {
			return "", context.Canceled
		}
		return answer, nil
	}
	cfg.Passphrase = func(keyFile string) ([]byte, error) {
		answer, ok := ask("SSH key", "Passphrase for "+keyFile)
		if !ok {
			return nil, context.Canceled
		}
		return []byte(answer), nil
	}
	cfg.TrustHost = func(hostport string, key ssh.PublicKey) bool {
		trusted := false
		shown := p.onUI(ctx, func() {
			p.loggingIn()
			trusted = p.sh.AskTrustHost(hostport, key.Type(), sshconn.Fingerprint(key))
		})
		if !shown || !trusted {
			cancelled.Store(true)
			return false
		}
		return true
	}
	return cfg
}

// loggingIn moves the status from "Connecting to" to "Logging in" when the
// first SSH prompt shows that the host answered.
func (p *sessionPage) loggingIn() {
	if p.state == stSSH {
		p.setState(stSSHLogin)
	}
}

// start runs the whole flow from the SSH login.
func (p *sessionPage) start() {
	p.reason, p.noDynamic, p.rdpLost = "", false, false
	p.setState(stSSH)
	p.dialing = true
	ctx, target := p.ctx, p.c.RDPTarget()
	var cancelled atomic.Bool
	cfg := p.sshConfig(ctx, &cancelled)
	go func() {
		client, err := sshconn.Dial(ctx, cfg)
		if err != nil {
			p.post(func() {
				p.dialing = false
				if cancelled.Load() {
					p.fail("the SSH login was cancelled")
				} else {
					p.fail(sshErrText(err.Error()))
				}
			})
			return
		}
		p.post(func() { p.setState(stTunnel) })
		tun, err := tunnel.Open(client, target)
		p.sh.Post(func() {
			if p.dead {
				if tun != nil {
					tun.Close()
				}
				client.Close()
				return
			}
			p.dialing = false
			if err != nil {
				go client.Close()
				p.fail("the tunnel could not be opened: " + err.Error())
				return
			}
			p.client, p.tun = client, tun
			p.watch(client)
			p.connectRDP()
		})
	}()
}

// watch reports when the SSH connection ends.
func (p *sessionPage) watch(client *sshconn.Client) {
	ctx := p.ctx
	go func() {
		select {
		case <-client.Done():
			p.post(func() { p.sshDown(client) })
		case <-ctx.Done():
		}
	}()
}

// dropLink closes the tunnel and the SSH connection.
func (p *sessionPage) dropLink() {
	if p.tun != nil {
		st := p.tun.Stats()
		slog.Info("session link closed", "connection", p.c.DisplayName(), "conns", st.Conns, "bytesIn", st.BytesIn, "bytesOut", st.BytesOut)
		p.tun.Close()
		p.tun = nil
	}
	if p.client != nil {
		go p.client.Close() // may wait for the network
		p.client = nil
	}
}

// sshDown handles a lost SSH connection: stop accepting on the tunnel and
// log in again, unless the session had ended before for another reason.
func (p *sessionPage) sshDown(client *sshconn.Client) {
	if p.client != client {
		return
	}
	slog.Warn("session lost SSH", "connection", p.c.DisplayName(), "err", client.Err())
	if p.state == stDisconnected {
		if time.Since(p.lostAt) > 3*time.Second {
			p.dropLink() // an idle link of an ended session: nothing to get back
			return
		}
		p.rdpLost = true // the session ended because of this drop
	}
	p.client = nil
	go client.Close()
	p.before = p.state
	if p.tun != nil {
		p.tun.SetAccepting(false)
	}
	p.attempt = 1
	p.setState(stReconnecting)
	p.redial()
}

// redial logs in to the SSH host again until it works: after 1s, 2s, 5s,
// then every 10s. Agent and keys go silently, a password is asked again.
func (p *sessionPage) redial() {
	p.dialing = true
	ctx := p.ctx
	var cancelled atomic.Bool
	cfg := p.sshConfig(ctx, &cancelled)
	go func() {
		for n := 1; ; n++ {
			p.post(func() {
				if p.client == nil && p.dialing {
					p.attempt = n
					p.setState(stReconnecting)
				}
			})
			select {
			case <-time.After(backoffDelay(n)):
			case <-ctx.Done():
				return
			}
			client, err := sshconn.Dial(ctx, cfg)
			if err == nil {
				p.sh.Post(func() {
					if p.dead {
						client.Close()
						return
					}
					p.sshBack(client)
				})
				return
			}
			if ctx.Err() != nil {
				return
			}
			if cancelled.Load() {
				p.post(func() {
					p.dialing = false
					p.dropLink()
					p.fail("the SSH login was cancelled")
				})
				return
			}
		}
	}()
}

// sshBack gives the tunnel the new SSH connection. The backend's own
// reconnect then finds the tunnel again; when it had given up already, it is
// connected anew.
func (p *sessionPage) sshBack(client *sshconn.Client) {
	p.dialing = false
	if p.tun == nil {
		go client.Close()
		p.fail("the tunnel is gone")
		return
	}
	p.client = client
	p.tun.SetDialer(client)
	p.tun.SetAccepting(true)
	p.watch(client)
	switch {
	case p.sess == nil || p.rdpLost || !p.sess.Connected():
		p.connectRDP()
	case !p.rdpTrouble:
		// The backend never noticed (it had no connection open just then):
		// nothing more will come from it, so the session is as it was.
		switch p.before {
		case stRDP, stRDPLogin:
			p.setState(p.before)
		default:
			p.setState(stConnected)
		}
	}
}

// reconnect is the Reconnect button of an ended session.
func (p *sessionPage) reconnect() {
	if p.dead || p.state != stDisconnected || p.dialing {
		return
	}
	if p.client == nil || p.tun == nil {
		p.dropLink()
		p.start()
		return
	}
	p.reason, p.noDynamic = "", false
	p.connectRDP()
}

// askPassword asks the RDP password and saves or forgets it as the user
// chose. False when cancelled.
func (p *sessionPage) askPassword() bool {
	who := p.c.RDPHost
	if p.c.RDPUser != "" {
		who = p.c.RDPUser + " on " + p.c.RDPHost
	}
	prompt := "Password for " + who
	if p.askFresh {
		prompt = "The password was rejected. Password for " + who
	}
	pw, save, ok := p.sh.AskPassword("Remote desktop login", prompt, true)
	if p.dead || !ok {
		return false
	}
	p.password, p.havePassword, p.askFresh = pw, true, false
	// Save the new one, or drop a saved one that is wrong now. Read the
	// connection again: it may have been edited while this tab was open.
	store := p.sh.Store()
	cur, found := store.Get(p.c.ID)
	if !found {
		return true
	}
	switch {
	case save:
		cur.RDPPassword, cur.SavePassword = pw, true
	case cur.RDPPassword != "" || cur.SavePassword:
		cur.RDPPassword, cur.SavePassword = "", false
	default:
		return true
	}
	p.c.RDPPassword, p.c.SavePassword = cur.RDPPassword, cur.SavePassword
	store.Put(cur)
	p.sh.SaveStore()
	return true
}

// backend creates the session backend in the container, once.
func (p *sessionPage) backend() error {
	if p.sess != nil {
		return nil
	}
	p.gen++
	ev := p.events(p.gen)
	var s rdp.Session
	var err error
	if p.useFake {
		s, err = fake.New(p.hwnd, ev)
	} else {
		s, err = mstsc.New(p.hwnd, ev)
	}
	if err != nil {
		return err
	}
	p.sess = s
	p.have = sessRect{}
	if p.full {
		s.SetFullScreen(true)
	}
	return nil
}

// connectRDP connects the backend to the tunnel. UI thread, SSH up.
func (p *sessionPage) connectRDP() {
	if p.dead {
		return
	}
	if !p.havePassword {
		switch {
		case p.askFresh:
			if !p.askPassword() {
				p.fail("the login was cancelled")
				return
			}
		case p.c.RDPPassword != "":
			p.password, p.havePassword = p.c.RDPPassword, true
		case p.c.RDPUser == "":
			p.password, p.havePassword = "", true // the server asks
		default:
			if !p.askPassword() {
				p.fail("the login was cancelled")
				return
			}
		}
	}
	if p.dead {
		return
	}
	if p.client == nil || p.tun == nil {
		// SSH dropped while the password was asked: sshBack comes here again.
		p.rdpLost = true
		if !p.dialing {
			p.fail("the SSH connection was lost")
		}
		return
	}
	if p.sess != nil && p.sess.Connected() {
		// Still busy with a connection we gave up on: start with a new one.
		p.sess.Close()
		p.sess = nil
	}
	if err := p.backend(); err != nil {
		slog.Error("session backend", "err", err)
		p.fail(err.Error())
		return
	}
	p.rdpLost, p.rdpTrouble = false, false
	p.setState(stRDP)
	p.applyRect(true)
	cred := rdp.Credentials{User: p.c.RDPUser, Domain: p.c.RDPDomain, Password: p.password}
	if err := p.sess.Connect(p.tun.Host(), p.tun.Port(), cred, p.c.Options); err != nil {
		slog.Error("session connect", "err", err)
		p.fail(err.Error())
		return
	}
	p.focusSession()
}

// events are the backend's callbacks. They arrive on the UI thread, from
// inside the backend (the control's event sink). Each is queued with a
// posted message, so that dialogs, resizes and closing the session never
// run inside such a call (sh.Post would run them at once: on the UI thread
// it is a plain call).
func (p *sessionPage) events(gen int) rdp.Events {
	later := func(f func()) {
		p.deferUI(func() {
			if p.gen == gen {
				f()
			}
		})
	}
	return rdp.Events{
		Connected: func() {
			later(func() {
				p.rdpTrouble = false
				if p.state != stConnected {
					p.setState(stRDPLogin)
				}
			})
		},
		LoginComplete: func() {
			later(func() {
				p.rdpTrouble = false
				p.setState(stConnected)
				p.applyRect(false)
				p.focusSession()
			})
		},
		Disconnected: func(reason int, text string, userEnded bool) {
			// The end that comes with a rejected password is not the end:
			// the password is asked again. Decided now, not when the queue
			// gets here, because the new login may be on its way by then.
			expected := p.asking
			later(func() { p.disconnected(reason, text, userEnded, expected) })
		},
		Reconnecting: func(attempt int) {
			later(func() {
				p.rdpTrouble = true
				if p.client == nil {
					return // the SSH login counts the attempts
				}
				p.attempt = attempt
				p.setState(stReconnecting)
			})
		},
		RequestFullScreen: func(on bool) {
			later(func() { p.sh.SetFullScreen(on) })
		},
		RequestMinimize: func() {
			later(func() { p.sh.Minimize() })
		},
		RemoteSize: func(width, height int) {
			later(p.changed)
		},
		DynamicUnsupported: func() {
			later(func() {
				p.noDynamic = true
				p.changed()
			})
		},
		AuthFailed: func() {
			p.asking = true
			later(p.authFailed)
		},
	}
}

func (p *sessionPage) disconnected(reason int, text string, userEnded, expected bool) {
	slog.Info("session disconnected", "connection", p.c.DisplayName(), "reason", reason, "text", text, "userEnded", userEnded)
	switch {
	case p.ending:
	case userEnded:
		p.sh.ClosePage(p)
	case expected:
	case p.client == nil && p.dialing:
		p.rdpLost = true // SSH is on its way back: connect again then
	default:
		p.fail(text)
	}
}

// authFailed asks for another password after the server rejected ours, and
// connects again. A saved password is only replaced once a new one is given.
func (p *sessionPage) authFailed() {
	p.asking = true
	p.havePassword, p.askFresh = false, true
	ok := p.askPassword()
	p.asking = false
	if p.dead {
		return
	}
	if !ok {
		p.fail("the password was rejected")
		return
	}
	if p.client == nil {
		p.rdpLost = true // sshBack connects
		return
	}
	p.connectRDP()
}
