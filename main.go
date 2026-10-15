// NLR Capture is a native HTTP/HTTPS debugging proxy for Windows and
// Linux: it records traffic through the system proxy or a virtual network
// adapter, decrypts HTTPS, and shows it in a DevTools-style network panel.
// The window is drawn with Gio, without any web view.
package main

import (
	"flag"
	"fmt"
	"os"
	"sync/atomic"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"nlrcapture/internal/config"
	"nlrcapture/internal/ui"
)

var (
	modeFlag = flag.String("mode", "", "capture mode on start: manual, system or tun")
	portFlag = flag.Int("port", 0, "proxy port (overrides the setting)")
	harFlag  = flag.String("open", "", "HAR file to import on start")
)

type host struct {
	w    *app.Window
	hwnd atomic.Uintptr
}

func (h *host) Invalidate()                    { h.w.Invalidate() }
func (h *host) Perform(a system.Action)        { h.w.Perform(a) }
func (h *host) HWND() uintptr                  { return h.hwnd.Load() }
func (h *host) SetWindowMode(m app.WindowMode) { h.w.Option(m.Option()) }

func main() {
	flag.Parse()
	dir := config.Dir()
	cfg := config.Open(dir)
	set := cfg.Settings()
	switch *modeFlag {
	case config.ModeManual, config.ModeSystem, config.ModeTUN:
		set.Mode = *modeFlag
	case "":
	default:
		fmt.Fprintln(os.Stderr, "nlrcapture: unknown -mode", *modeFlag)
		os.Exit(2)
	}
	if *portFlag > 0 && *portFlag < 65536 {
		set.Port = *portFlag
	}
	cfg.Save(set)

	w := new(app.Window)
	opts := []app.Option{
		app.Title("NLR Capture"),
		app.Size(unit.Dp(set.WindowWidth), unit.Dp(set.WindowHeight)),
		app.MinSize(unit.Dp(860), unit.Dp(520)),
		app.Decorated(false),
	}
	if set.Maximized {
		opts = append(opts, app.Maximized.Option())
	}
	w.Option(opts...)

	h := &host{w: w}
	a := ui.New(h, cfg, dir)
	watchSignals(a)
	if *harFlag != "" {
		a.DropFiles([]string{*harFlag})
	} else if flag.NArg() > 0 {
		a.DropFiles(flag.Args())
	}
	go func() {
		run(w, h, a)
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, h *host, a *ui.App) {
	// Whatever happens, the system proxy must not stay pointed at a dead
	// process.
	defer func() {
		if r := recover(); r != nil {
			a.EmergencyRestore()
			panic(r)
		}
	}()
	var ops op.Ops
	var metric unit.Metric
	started := false
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			a.Shutdown()
			return
		case app.ConfigEvent:
			a.SetDecorated(e.Config.Decorated)
			a.SetMaximized(e.Config.Mode == app.Maximized)
			switch {
			case e.Config.Mode == app.Maximized:
				a.RememberWindow(0, 0, true)
			case e.Config.Mode == app.Windowed && metric.PxPerDp > 0:
				a.RememberWindow(int(metric.PxToDp(e.Config.Size.X)), int(metric.PxToDp(e.Config.Size.Y)), false)
			}
		case app.FrameEvent:
			metric = e.Metric
			gtx := app.NewContext(&ops, e)
			if !started {
				started = true
				a.Start()
			}
			a.Layout(gtx)
			e.Frame(gtx.Ops)
		default:
			platformEvent(h, a, e)
		}
	}
}
