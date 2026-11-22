# srdp4win: Plan

A Windows app (Go + [windigo](https://github.com/rodrigocfd/windigo)) for remote
desktop sessions that always travel through an SSH tunnel. The app starts with
a connection manager. A connection has two addresses: the SSH host to log in
to and the RDP host to connect to from there. Pick one and you get the remote
desktop in a tab. The RDP port is never exposed: only SSH is.

The RDP session itself is Microsoft's own client control (`mstscax.dll`, the
engine of `mstsc.exe`) hosted inside our window. The app structure, SSH login,
look, installer and release tooling are ported from
[ssh-cloud](../ssh-cloud); the small-tool spirit comes from
[otp4win](../otp4win). The session window (tabs, full screen, floating toolbar,
scaling modes) is modelled on [Remmina](https://remmina.org).

## Constraints

- Windows 11 only. MIT licensed. Single `.exe`, no cgo, no bundled DLLs.
  Installed per user with an MSI (wixl), no admin rights.
- Runs without elevated rights, always: the manifest asks for `asInvoker`,
  nothing is written outside the user's profile (`%APPDATA%`,
  `%LOCALAPPDATA%`, `HKCU`), the tunnel listens on loopback only (so there is
  no firewall prompt), and installing and updating need no admin either.
- Cross compiled on Linux. It must start and stay usable under Wine, so
  everything except the live RDP picture can be tested there (see "Wine").
- Native Win32 controls via windigo plus GDI drawing, Windows 11 look, light
  and dark. COM is used only to host the RDP control (and the file dialog).
- Server side: any OpenSSH server that allows TCP forwarding
  (`AllowTcpForwarding`), and any RDP server reachable from it (Windows,
  xrdp, GNOME Remote Desktop). Nothing is installed on the server.
- Direct RDP without SSH is not allowed. Every session goes through the SSH
  tunnel and there is no setting, flag or fallback that skips it. A
  connection without an SSH host cannot be saved, and the RDP control is only
  ever given the tunnel's loopback address, never the RDP host's address.
- SSH passwords and key passphrases are never stored. The RDP password can be
  saved. Nothing secret is ever logged.
- Performance comes first: no extra copies, no extra latency, no feature that
  costs frames.

## Why the Windows RDP control

Three ways to get an RDP session in a Go program were considered:

| | mstscax ActiveX (chosen) | FreeRDP (what Remmina uses) | pure Go RDP |
|---|---|---|---|
| Single exe, no cgo | yes | no (cgo or DLLs, mingw build) | yes |
| Performance | best on Windows: AVC/H.264, RemoteFX, GPU decode | good | poor |
| Printers | Easy Print, works out of the box | needs driver mapping | none |
| Clipboard, font smoothing | yes | yes | partly |
| NLA / CredSSP | yes | yes | partly |
| Runs under Wine | no | yes | yes |

mstscax wins on every requirement except Wine. That gap is closed by keeping
the session behind a small interface with a second, fake implementation (see
"Wine").

## How a connection works

1. SSH: connect and log in to the SSH server (`golang.org/x/crypto/ssh`), host
   key checked against `~/.ssh/known_hosts`.
2. Tunnel: listen on the loopback address and port that belong to this RDP
   host (see "Tunnel details"). Every accepted
   connection is forwarded through a `direct-tcpip` channel to the RDP host,
   an address as seen from the SSH host (`localhost` when both are the same
   machine).
3. RDP: create the control, point it at the loopback address and port, apply
   the profile settings, connect.
4. On disconnect or when the tab closes: the control is released, the listener
   is closed, the SSH connection is closed.

One SSH connection per tab. Sharing one between tabs would save a login but
makes sessions wait on each other inside one TCP stream.

### Tunnel details

- Loopback only, never port 3389 (mstscax refuses a loopback connection that
  looks like the local console).
- Every RDP host has a loopback address of its own. The control remembers a
  trusted certificate by the address it connects to
  (`HKCU\Software\Microsoft\Terminal Server Client\Servers\<address>`,
  value `CertHash`), so behind `127.0.0.1` all hosts would be one computer
  whose certificate keeps changing. All of `127.0.0.0/8` is the local
  computer, so a hash of the SSH host and the RDP target picks an address
  from `127.1.0.1` to `127.254.255.254` and a port from 20000 to 39999, the
  same every time. The SSH host is part of it because `localhost` is another
  machine behind every SSH host. A second tab to the same host gets a random
  port on the same address; where the address cannot be used the tunnel falls
  back to `127.0.0.1` with a random port.
- The listener accepts while the SSH connection is up (the control reconnects
  by itself on the same port) and refuses while it is down, one connection at
  a time. On Windows the peer's process ID is checked
  (`GetExtendedTcpTable`) so only srdp4win itself can use the tunnel; other
  local users and programs are refused. Under Wine this check is skipped when
  the API is missing.
- Plain byte copy in both directions with 32 KB buffers (the SSH packet size),
  `TCP_NODELAY` on both sockets, no buffering, no SSH compression (RDP is
  compressed already). Preferred ciphers are the hardware-accelerated AEADs
  (`aes128-gcm`, `aes256-gcm`, then `chacha20-poly1305`).
- SSH keep-alives detect a dead link. When the tunnel drops, the tab shows
  "Reconnecting", logs in again (agent and keys silently, a password is asked
  again) and lets the control reconnect to the same session.

## The RDP control

- Hosting: `atl.dll` (`AtlAxWinInit`, an `AtlAxWin` child window created with
  the control's CLSID, `AtlAxGetControl`). ATL does the whole OLE container
  side, so no hand-written `IOleClientSite`. The newest available class is
  tried first (`MsRdpClient10NotSafeForScripting`, then 9).
- Calls go straight through the vtables (`IMsRdpClient9`,
  `IMsRdpClientAdvancedSettings8`, `IMsRdpClientNonScriptable5`,
  `IMsRdpExtendedSettings`), declared by hand in Go from the type library, in
  the style of ssh-cloud's `dragout.go`.
- Events: a hand-written `IDispatch` sink connected through
  `IConnectionPoint` to `IMsTscAxEvents`: `OnConnected`, `OnLoginComplete`,
  `OnDisconnected` (with reason, turned into a readable message),
  `OnAutoReconnecting2`, `OnRequestGoFullScreen`, `OnRequestLeaveFullScreen`,
  `OnRequestContainerMinimize`, `OnConfirmClose`, `OnFatalError`,
  `OnRemoteDesktopSizeChange`.
- Everything touching the control runs on the UI thread (STA). The SSH login
  and the tunnel run on goroutines and report back with a posted message.

### Session settings (per connection)

| Setting | Default | How |
|---|---|---|
| Clipboard | on | `RedirectClipboard` (text, images, files) |
| Printers | on | `RedirectPrinters` (Easy Print on Windows servers) |
| Font smoothing | on | `PerformanceFlags` bit `0x80` (ClearType) |
| Drives | off | `RedirectDrives`, optional list of drive letters |
| Sound | play locally | `AudioRedirectionMode`; microphone off |
| Smart cards, ports | off | `RedirectSmartCards`, `RedirectPorts` |
| Windows key combos | in full screen | `KeyboardHookMode` (never / always / full screen) |
| Resize mode | dynamic | see "Resizing" |
| Quality | best | see "Performance" |
| Colour depth | 32 bit | `ColorDepth` |
| Admin session | off | `ConnectToAdministerServer` |
| Verify RDP certificate | on | `AuthenticationLevel` 0, or 2 (Windows asks, once per host) |

### Performance

- `NetworkConnectionType` = LAN with bandwidth auto-detect on, so the server
  picks the best codec for the real link (AVC 4:4:4 / H.264 where the server
  offers it, hardware decoded).
- Bitmap cache and persistent bitmap cache on, RDP bulk compression on,
  `RedirectDirectX`/graphics pipeline left on.
- Quality presets, as in Remmina. **Best** (default): desktop composition,
  wallpaper, full window drag, animations, themes. **Medium**: themes only.
  **Poor**: every effect off. A preset is only a set of `PerformanceFlags`
  and overrides nothing else: font smoothing, the redirections and the
  colour depth each follow their own option in all three.
- The control gets the real pixel size and the monitor's scale factor
  (`DesktopScaleFactor`, `DeviceScaleFactor`), so the picture is 1:1 and sharp
  and nothing is stretched in the default mode.
- RDP's UDP transport cannot pass through an SSH tunnel; the session runs on
  TCP. Milestone 2 checks that the failed UDP probe costs no connect time.
- No per-frame work on our side: we never touch the pixels. The tunnel copy is
  the only code in the data path and gets a benchmark test.

### Resizing

Three modes, switchable from the toolbar while connected, like Remmina:

- **Dynamic full screen**, only as a choice in the connection: dynamic
  resolution that goes to full screen just before it connects, so the remote
  desktop starts at the screen's size. A reconnect does not force full screen
  again.
- **Dynamic resolution** (default): when the tab area changes size (window
  resize, maximize, full screen, moving to another monitor) the remote
  desktop is resized to match with `UpdateSessionDisplaySettings`, debounced
  by 250 ms. Needs Windows 8.1 / Server 2012 R2 or a recent xrdp. When the
  server does not support it, the app falls back to Scaled and says so in the
  status line.
- **Scaled**: the remote size stays what it was at connect; the picture is
  stretched to the tab (`SmartSizing`).
- **Fixed**: a chosen resolution (list of common sizes or custom), shown 1:1
  and centred, with the control's scroll bars when the tab is smaller.

### Full screen

- Toggle with the toolbar, F11 in the app chrome, or Ctrl+Alt+Break (the
  control's own hotkey, which also works while the keyboard is grabbed).
- The container handles it (`ContainerHandledFullScreen`): our window drops
  its frame, tabs and status line and covers the monitor it is on; the
  control keeps the same session and, in dynamic mode, resizes the remote
  desktop to the monitor. Leaving full screen restores the window and resizes
  back.
- The control's own blue connection bar is off (`DisplayConnectionBar`), and
  because newer controls show it in full screen anyway, its window (class
  `BBarWindowClass`) is hidden whenever the session goes to full screen,
  connects or logs in. Instead there is our
  **floating toolbar**: a small owned popup window at the top centre of the
  screen (a child window cannot sit on top of the control reliably).
  - Hidden, it leaves a 2 px strip at the top edge. Moving the mouse onto the
    strip slides it down (about 150 ms); leaving it slides it back after
    half a second.
  - A pin button keeps it visible. The pin state is remembered.
  - It never takes the keyboard focus (`WS_EX_NOACTIVATE`), so the remote
    session keeps the keys.
  - Buttons (Fluent icons, tooltips): pin, connection name, leave full
    screen, resize mode, previous / next tab (when there are several), send
    keys (Ctrl+Alt+Del, Win, Alt+Tab, Print Screen ...), screenshot to
    clipboard, minimize, disconnect.
- The same buttons sit in a normal toolbar row under the tabs when not in
  full screen.
- Multi-monitor spanning (`UseMultimon`) is not in the first version.

## Connections and login

- The **connection manager** is what the app shows when it starts, in the
  start tab. Nothing connects by itself. It is a list with name, group, SSH
  host, RDP host and last used time, and a search box. Double-click or Enter
  connects in a new tab; New, Edit, Copy and Delete in a toolbar and the
  right-click menu. Its tab is always the first one: click it (or Ctrl+T)
  to open another session. There is no + button.
- A connection has a stable ID and four groups of fields:
  - **General**: name, group.
  - **SSH host**: address, port (22), user, authentication. Required.
  - **RDP host**: address and port (3389) as seen from the SSH host, user,
    domain, password, save password. The address is required and has no
    default; enter `localhost` when the RDP host is the SSH host itself.
  - **Options**: the settings table above.
- Storage is otp4win's method: the connections, with any saved RDP passwords,
  are JSON in `%APPDATA%\srdp4win\connections.dat`, the whole file encrypted
  with DPAPI (`CryptProtectData`, no prompt), so only the current Windows
  user can read it. It is written to a temp file and renamed. Preferences are
  not secret and go to `settings.json` next to it.
- SSH authentication is ssh-cloud's, unchanged: **Automatic** (Windows
  OpenSSH agent, Pageant, default keys, then password), **Key file**,
  **Pageant**, **Password**; keyboard-interactive supported; unknown host keys
  are shown with their fingerprint to trust, a changed key is refused.
- RDP login: the user name and domain come from the connection. The password
  comes from the connection when it was saved, otherwise it is asked in our
  own dialog (with a "Save password" box). It is handed to the control
  (`ClearTextPassword`), so NLA works without the Windows credential prompt.
  A saved password that the server rejects is asked again. With an empty
  password the control shows the server's own logon screen (or the Windows
  credential prompt when NLA is required).

## Main window

- ssh-cloud's shell: rounded window, Mica title bar, tab strip (one tab per
  session after the connection manager's start tab), session toolbar row, content
  area, status line (state on the left: "Connecting to SSH", "Opening tunnel",
  "Connected", "Reconnecting"; remote size and resize mode on the right).
- Ctrl+T to the connection manager, Ctrl+W close (asks when the session is connected),
  Ctrl+Tab switch. These only work while the focus is in our chrome; inside
  the session the keys belong to the remote desktop.
- Closing the window with sessions open asks once.
- "..." menu, in the connection manager's toolbar: Settings (theme: system / light / dark, default options for new
  connections), Open log, Check for updates, About.
- Updates, About window, logging to `%APPDATA%\srdp4win\srdp4win.log` with
  `log/slog`: ported from ssh-cloud. Logged: SSH login method and key
  fingerprints, host key decisions, tunnel open/close with byte counts and
  duration, every RDP event with its reason code. Never passwords.

## Wine

Wine has no working `mstscax.dll`, so the control cannot be created there. The
session is therefore an interface:

```go
type Session interface {
    Connect(addr string, o Options) error
    Disconnect()
    Resize(w, h, scale int)
    SetMode(m ResizeMode)
    SendKeys(k Keys)
    Screenshot() (image.Image, error)
    Hwnd() win.HWND
}
```

- `mstsc` backend: the real control.
- `fake` backend: a GDI-drawn panel that opens the tunnel like the real one,
  reads the first bytes the RDP server sends back to a minimal X.224
  connection request (proving the whole SSH path works), and shows the
  current state, size, scale and mode as text. It reports the same events.

The fake backend is used automatically when the control's class is not
registered (Wine), or with `--fake`. With it, everything else is testable
under Wine with `run.sh`: connection manager, SSH login and host key prompts,
tunnel, tabs, toolbar, resize logic, full screen and the sliding toolbar,
settings, theme, updater. Only the live picture, printers, clipboard and
font smoothing need a real Windows machine.

## Architecture

```
cmd/srdp4win/        the Windows program (manifest: common controls v6, per-monitor DPI)
cmd/srdp-cli/        command-line tool: open a tunnel and print the port
                     (use with mstsc or xfreerdp to test against real servers)
internal/sshconn/    SSH connect, login methods, Pageant, agent, host keys
                     (from ssh-cloud's internal/remote, without SFTP)
internal/tunnel/     loopback listener, forwarding, peer check, statistics
internal/config/     connections (DPAPI-encrypted file), preferences
internal/rdp/        Session interface, options to control settings mapping,
                     disconnect reason texts (plain Go, testable)
internal/rdp/mstsc/  ATL host, vtable declarations, event sink (Windows only)
internal/rdp/fake/   fake backend
internal/rdp/wincap/ window capture for the screenshot
internal/icons/      Fluent SVG icons, app icon
internal/logging/    log file setup
internal/update/     GitHub release check and MSI download
internal/sshtest/    in-process SSH server with direct-tcpip for tests
internal/ui/         windigo windows: shell, tab strip, start tab, connection
                     editor, session tab, floating toolbar, dialogs, theme
tools/genicon/       renders the app icon (internal/icons/app/icon.svg, with
                     Inkscape) into the embedded PNGs and winres/
tools/mkico/         packs the PNGs into the installer's .ico
winres/              icon, manifest and version info for go-winres
installer/           MSI definition (wixl), per user, new UpgradeCode
run.sh, release.sh   build and run with Wine; build the MSI and publish
```

Everything outside `ui` and `rdp/mstsc` is plain Go and tested on Linux with
`go test ./...` against the in-process SSH server and a tiny TCP echo "RDP
server". `GOOS=windows go vet ./...` covers the rest at compile level.

Ported from ssh-cloud nearly as is: login and host key code, Pageant, logging, update, icons, theme, tab strip, modal helpers, About,
`run.sh`, `release.sh`, the `.wxs`, `winres.json`, `.gitignore`. From otp4win:
the DPAPI store (`protect`, `unprotect`, atomic write). The repos
stay separate, so the code is copied, not imported.

## Milestones

1. **Skeleton**: module, MIT license, `.gitignore`, `run.sh`, winres, an empty
   themed window with the tab strip running under Wine.
2. **SSH and tunnel**: `sshconn`, `tunnel`, `sshtest`, `srdp-cli`. Tests for
   login methods, forwarding, one-connection rule, drop and reconnect, and a
   throughput benchmark. Check with `srdp-cli` plus `mstsc` on Windows that a
   tunnelled session connects fast (UDP probe) and performs well.
3. **Connections**: encrypted connection file, preferences, the connection
   manager at startup, connection editor, login and host key prompts.
4. **Fake session**: Session interface, fake backend, session tab, status
   line, connect and disconnect flow end to end under Wine.
5. **Real session**: ATL host, vtables, event sink; connect with clipboard,
   printers, font smoothing, quality presets, password hand-over. First test
   on Windows 11 against a Windows server and xrdp.
6. **Resizing**: dynamic, scaled, fixed, DPI, the fallback, mode switch.
7. **Full screen**: container-handled full screen, floating auto-hide toolbar,
   send keys, screenshot, keyboard grab modes.
8. **Reconnect and errors**: tunnel drop, readable disconnect reasons, close
   confirmations.
9. **Release**: app icon, MSI, `release.sh`, updater, About, README with
   screenshot. First release v0.1.0.

Milestones 1 to 8 are implemented and, with the fake session, tested under
Wine. Of milestone 9 everything is in place and the MSI builds; the release
itself and the screenshot are left. The real session (`internal/rdp/mstsc`)
compiles but has never run: it needs a first test on Windows 11, see the
checklist below.

## To check on Windows

- The control is created (the log says "rdp control: created" with its
  version) and no "name not supported" or "set failed" warnings follow.
- Events arrive: connected, login complete, disconnected with a reason.
- The password hand-over logs in with NLA without a Windows prompt; a wrong
  password is asked again in our dialog; an empty one shows the server's
  logon screen.
- Verify RDP certificate: the warning has "Don't ask me again for
  connections to this computer"; once ticked the same host connects without
  a warning, also in a new tab and after a restart, and another host still
  asks. The log line "tunnel open" shows the host's own address.
- Dynamic resize follows the window and full screen; an old server or xrdp
  falls back to Scaled.
- Full screen: Ctrl+Alt+Break toggles it, Windows key combinations go to the
  remote desktop only then, the floating toolbar never takes the focus.
- Send keys, in particular Print Screen and Win+L (the extended scancode
  convention is unresolved).
- Typing in the session: Tab, arrows, Enter and Esc reach the remote desktop.
- Clipboard, printers, sound, font smoothing and the quality presets.
- Closing a tab or the window with a session connected does not crash.
- The tunnel refuses a connection from another program.
- Mica frame, dark list header and dark menus.

## Known weaknesses (accepted)

- The control connects to a loopback address, so the RDP server's certificate
  can never match the name: with "Verify RDP certificate" Windows always
  reports a name mismatch the first time. Ticking "Don't ask me again for
  connections to this computer" then trusts that certificate for that host
  only (it has its own loopback address), and Windows asks again when it
  changes. The control cannot be told the real name: its
  `UserSpecifiedServerName` is an internal property. With the check switched
  off, the SSH host key still authenticates the path to the SSH server, but
  the hop from there to the RDP target is trusted like the network it is on.
- With NLA the login uses NTLM, not Kerberos (there is no service name for a
  loopback address).
- No UDP transport, so on lossy links the picture is less smooth than a
  plain RDP connection with UDP would be.
- The live session, printers and clipboard cannot be tested under Wine.
- Between the listener opening and the control connecting, another program of
  the same user could take the tunnel. The process ID check closes this on
  Windows; it stays open under Wine.
- A saved RDP password is as safe as the Windows account: any program running
  as the same user can decrypt the connection file. Under Wine DPAPI gives
  little real protection.
