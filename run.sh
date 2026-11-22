#!/bin/bash
#
# go install github.com/tc-hib/go-winres@latest
# sudo apt install wine
#
# Cross compiles srdp4win.exe for Windows and runs it with Wine:
#
#   ./run.sh
#
# Arguments are passed to the program. Wine has no RDP control, so the session
# is the fake one there (as with --fake on Windows):
#
#   ./run.sh --fake
#
# Wine keeps its own %APPDATA%, so the connections, settings and log are
# stored in ~/.wine/drive_c/users/$USER/AppData/Roaming/srdp4win.
set -e
cd "$(dirname "$0")"

# Check the dependencies.
missing=
command -v go >/dev/null || missing="$missing go"
command -v wine >/dev/null || missing="$missing wine"
winres=$(command -v go-winres || echo ~/go/bin/go-winres)
[ -x "$winres" ] || missing="$missing go-winres"
if [ -n "$missing" ]; then
	echo "missing:$missing" >&2
	echo "install with: sudo apt install golang wine" >&2
	echo "              go install github.com/tc-hib/go-winres@latest" >&2
	exit 1
fi

# Build the exe with icon, manifest and version info.
"$winres" make --arch amd64 --out cmd/srdp4win/rsrc
GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o srdp4win.exe ./cmd/srdp4win

# Run it.
exec wine srdp4win.exe "$@"
