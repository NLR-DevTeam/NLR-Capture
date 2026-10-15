package main

import (
	"gioui.org/app"
	"gioui.org/io/event"

	"nlrcapture/internal/ui"
)

// platformEvent handles the window events that only exist on Windows.
func platformEvent(h *host, a *ui.App, e event.Event) {
	v, ok := e.(app.Win32ViewEvent)
	if !ok {
		return
	}
	h.hwnd.Store(v.HWND)
	if !v.Valid() {
		return
	}
	// Window-affecting Win32 calls must run on the window's own thread.
	hwnd := v.HWND
	h.w.Run(func() { ui.InstallWindowHook(hwnd, a.DropFiles, a.EmergencyRestore) })
}
