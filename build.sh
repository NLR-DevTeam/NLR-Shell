#!/bin/sh
# Builds the NLR Shell binary for Linux as ./NLRShell.
#   ./build.sh           release build
#   ./build.sh --test    run the test suite first
#
# Gio needs cgo on Linux, plus the X11 and Wayland development libraries;
# see README.md.
set -e
cd "$(dirname "$0")"
export CGO_ENABLED=1

# Build against the patched Gio: upstream ignores app.Decorated(false) on X11
# and Wayland and sizes the pointer cursor itself, so the Linux fixes live in
# tools/giopatch/gioui-fixes.patch. A plain go build uses upstream Gio, with
# native decorations and an oversized cursor under fractional scaling.
modfile="$(go run ./tools/giopatch)"

if [ "$1" = "--test" ]; then
	go vet -modfile="$modfile" ./...
	go test -modfile="$modfile" ./...
fi

go build -modfile="$modfile" -trimpath \
	-ldflags "-s -w -X gioui.org/app.ID=nlrshell" \
	-o NLRShell .
ls -lh NLRShell
