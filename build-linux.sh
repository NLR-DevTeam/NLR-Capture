#!/bin/sh
# Builds NLR Capture on Linux into dist/linux/.
#   ./build-linux.sh
#
# Gio needs cgo and the X11, Wayland, EGL and xkbcommon development files:
#   Debian, Ubuntu: gcc pkg-config libwayland-dev libx11-dev libx11-xcb-dev
#                   libxkbcommon-x11-dev libgles2-mesa-dev libegl1-mesa-dev
#                   libffi-dev libxcursor-dev libxfixes-dev libvulkan-dev
#   Fedora:         gcc pkg-config wayland-devel libX11-devel libxkbcommon-x11-devel
#                   mesa-libGLES-devel mesa-libEGL-devel libXcursor-devel
#                   libXfixes-devel vulkan-headers
#   Arch:           gcc pkgconf wayland libx11 libxkbcommon-x11 libglvnd
#                   libxcursor libxfixes vulkan-headers
#
# The program is built against Gio with the Linux fixes in tools/giopatch
# (window decorations, cursor shape, window state); a plain go build uses
# upstream Gio and keeps the system's title bar.
set -e
cd "$(dirname "$0")"
export CGO_ENABLED=1

modfile="$(go run ./tools/giopatch)"

rm -rf dist/linux
mkdir -p dist/linux
go build -modfile="$modfile" -trimpath \
	-ldflags "-s -w -X gioui.org/app.ID=nlrcapture" \
	-o dist/linux/nlrcapture .
cp build/icon.png dist/linux/nlrcapture.png
ls -l dist/linux
