//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"gioui.org/io/event"

	"nlrcapture/internal/ui"
)

// platformEvent handles the window events that only exist on some
// platforms. X11 and Wayland expose no native window handle the program
// needs, and Gio delivers no file drops there.
func platformEvent(*host, *ui.App, event.Event) {}

// watchSignals puts the system proxy and the routes back when the session
// ends or the program is told to stop: logging out sends SIGHUP or SIGTERM,
// and the program would otherwise leave the desktop pointing at a proxy
// that no longer runs.
func watchSignals(a *ui.App) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-ch
		a.EmergencyRestore()
		os.Exit(0)
	}()
}
