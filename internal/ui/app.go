package ui

import (
	"image"
	"image/color"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/config"
)

// Version is the application version shown in the about box.
var Version = "1.0.0"

// Host is the window the app is displayed in.
type Host interface {
	// Invalidate requests a new frame; safe to call from any goroutine.
	Invalidate()
	// Perform executes a window action such as close or move.
	Perform(system.Action)
	// SetWindowMode changes the window state.
	SetWindowMode(app.WindowMode)
	// HWND returns the native window handle, or 0 if there is none.
	HWND() uintptr
}

// App is the root of the user interface.
type App struct {
	host    Host
	th      *Theme
	cfg     *config.Store
	set     config.Settings
	dataDir string
	// drawnSel is the request whose details this frame shows, 0 for none.
	drawnSel int64
	svc      *service
	store    *capture.Store

	list    *netList
	details *detailView
	search  *searchPane

	filterField Field
	filter      capture.Filter
	filterText  string
	types       map[string]bool
	typeClk     []widget.Clickable
	invertChk   widget.Bool
	tunnelChk   widget.Bool
	errorsChk   widget.Bool

	scrollChk widget.Bool
	throttle  int

	postMu sync.Mutex
	posted []func()
	// withGtx holds UI-goroutine actions that need a layout context, such as
	// clipboard commands chosen from a menu.
	withGtx []func(gtx layout.Context)

	mouse     image.Point
	size      image.Point
	pxPerDp   float32
	maximized bool
	decorated bool
	refocus   bool
	setDirty  time.Time
	mods      key.Modifiers

	menu       *menu
	dialogs    []Dialog
	toasts     []toast
	menuOut    *menu
	dialogsOut []Dialog
	dialogAt   map[Dialog]time.Time

	// quitting is set once the window may close.
	quitting atomic.Bool

	logoClk, winBtns0, winBtns1, winBtns2 widget.Clickable
	modeClk                               [3]widget.Clickable
	certBtn, rulesBtn, bpBtn, composeBtn  widget.Clickable
	setBtn                                widget.Clickable
	recBtn, clearBtn, filterBtn, findBtn  widget.Clickable
	importBtn, exportBtn, throttleBtn     widget.Clickable
	resumeAllBtn                          widget.Clickable
	clearFilterBtn                        widget.Clickable

	searchSplit Splitter
	// lastSysDark caches the system theme for the "follow system" setting.
	sysDark atomic.Int32
}

// New creates the app with the settings in dir.
func New(host Host, cfg *config.Store, dataDir string) *App {
	a := &App{host: host, cfg: cfg, set: cfg.Settings(), dataDir: dataDir, th: NewTheme(), types: map[string]bool{}}
	a.th.editMenu = a.editMenu
	a.applyTheme()
	go a.watchSystemTheme()
	a.store = capture.NewStore()
	a.store.Limit = a.set.MaxSessions
	a.list = newNetList(a)
	a.details = newDetailView(a)
	a.search = newSearchPane(a)
	a.filterField.Hint = "过滤"
	a.tunnelChk.Value = a.set.HideTunnels
	a.scrollChk.Value = a.set.AutoScroll
	a.svc = newService(a)
	a.store.Notify = a.svc.notify
	a.applyFilter()
	return a
}

// Start brings up the proxy and the capture mode. It is called once the
// window exists.
func (a *App) Start() { a.svc.start() }

// Store returns the session store.
func (a *App) Store() *capture.Store { return a.store }

// Theme returns the app theme.
func (a *App) Theme() *Theme { return a.th }

// SetMaximized tells the app whether the window is maximized.
func (a *App) SetMaximized(m bool) { a.maximized = m }

// SetDecorated tells the app whether the platform draws the decorations.
func (a *App) SetDecorated(on bool) { a.decorated = on }

// RememberWindow records the window size (in dp) and state.
func (a *App) RememberWindow(w, h int, maximized bool) {
	if maximized {
		if !a.set.Maximized {
			a.updateSettings(func(s *config.Settings) { s.Maximized = true })
		}
		return
	}
	if w < 800 || h < 500 || (w == a.set.WindowWidth && h == a.set.WindowHeight && !a.set.Maximized) {
		return
	}
	a.updateSettings(func(s *config.Settings) { s.WindowWidth, s.WindowHeight, s.Maximized = w, h, false })
}

// Post schedules f to run on the UI goroutine before the next frame.
func (a *App) Post(f func()) {
	a.postMu.Lock()
	a.posted = append(a.posted, f)
	a.postMu.Unlock()
	a.host.Invalidate()
}

func (a *App) runPosted() {
	a.postMu.Lock()
	fs := a.posted
	a.posted = nil
	a.postMu.Unlock()
	for _, f := range fs {
		f()
	}
}

// later runs fn on the UI goroutine with a layout context.
func (a *App) later(fn func(gtx layout.Context)) {
	a.withGtx = append(a.withGtx, fn)
	a.host.Invalidate()
}

// copyText puts s on the clipboard.
func (a *App) copyText(s string, what string) {
	a.later(func(gtx layout.Context) {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
	})
	a.Toast(toastOK, "已复制")
}

// updateSettings changes settings and persists them shortly after.
func (a *App) updateSettings(f func(*config.Settings)) {
	f(&a.set)
	if a.setDirty.IsZero() {
		a.setDirty = time.Now()
	}
}

// Flush writes pending settings to disk.
func (a *App) Flush() {
	if !a.setDirty.IsZero() {
		a.setDirty = time.Time{}
		a.cfg.Save(a.set)
	}
}

// Shutdown restores the system settings and saves state. It is safe to
// call more than once.
func (a *App) Shutdown() {
	a.list.saveColumns()
	a.Flush()
	a.svc.shutdown()
}

// EmergencyRestore puts the system proxy and routes back; the window
// procedure calls it when Windows is logging off.
func (a *App) EmergencyRestore() { a.svc.shutdown() }

// CloseRequested is asked whether the window may close; it always may,
// after the system settings are restored.
func (a *App) CloseRequested(shift bool) bool {
	return false
}

// DropFiles imports HAR files dropped on the window.
func (a *App) DropFiles(paths []string) {
	a.Post(func() {
		for _, p := range paths {
			a.importHAR(p)
		}
	})
}

func (a *App) quit() {
	a.quitting.Store(true)
	a.host.Perform(system.ActionClose)
}

// ---- Layout -----------------------------------------------------------

// Layout draws the whole window.
func (a *App) Layout(gtx layout.Context) layout.Dimensions {
	a.size = gtx.Constraints.Max
	a.pxPerDp = gtx.Metric.PxPerDp
	a.runPosted()
	a.th.animate = a.set.Animations
	for _, fn := range a.withGtx {
		fn(gtx)
	}
	a.withGtx = nil
	if !a.setDirty.IsZero() {
		if gtx.Now.Sub(a.setDirty) > time.Second {
			a.Flush()
		} else {
			gtx.Execute(op.InvalidateCmd{At: a.setDirty.Add(1100 * time.Millisecond)})
		}
	}
	for {
		e, ok := gtx.Event(pointer.Filter{Target: a, Kinds: pointer.Move | pointer.Drag | pointer.Press | pointer.Release})
		if !ok {
			break
		}
		if pe, ok := e.(pointer.Event); ok {
			a.mouse = image.Pt(int(pe.Position.X), int(pe.Position.Y))
			a.mods = pe.Modifiers
		}
	}
	a.shortcuts(gtx)
	a.svc.frame(gtx)

	root := clip.Rect{Max: a.size}.Push(gtx.Ops)
	event.Op(gtx.Ops, a)
	paint.Fill(gtx.Ops, a.th.Bg1)

	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.layoutTitlebar),
		layout.Rigid(a.layoutToolbar),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !a.set.ShowFilter {
				return layout.Dimensions{}
			}
			return a.layoutFilterBar(gtx)
		}),
		layout.Flexed(1, a.layoutBody),
		layout.Rigid(a.layoutStatusBar),
	)
	if a.refocus && a.menu == nil && len(a.dialogs) == 0 {
		a.refocus = false
		gtx.Execute(key.FocusCmd{Tag: a.list})
	}
	a.layoutDialogs(gtx)
	a.layoutMenu(gtx)
	a.layoutToasts(gtx)
	a.layoutTooltip(gtx)
	if !a.maximized {
		strokeRR(gtx.Ops, image.Rectangle{Max: a.size}, 0, float32(max(gtx.Dp(1), 1)), a.th.Border)
	}
	// The selection is often changed by events handled while this frame
	// was being laid out, after the details were drawn for the old one,
	// such as a click released on a row: draw the next frame at once
	// rather than when something else happens to ask for one.
	cur := int64(0)
	if s := a.list.selected(); s != nil {
		cur = s.ID
	}
	if cur != a.drawnSel {
		gtx.Execute(op.InvalidateCmd{})
	}
	root.Pop()
	return layout.Dimensions{Size: a.size}
}

func (a *App) layoutBody(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	if !a.search.open {
		return a.layoutMain(gtx)
	}
	w := min(gtx.Dp(unit.Dp(a.set.SearchWidth)), size.X/2)
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(w, size.Y))
			return a.search.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			v := a.set.SearchWidth
			d := a.searchSplit.Layout(gtx, a.th, layout.Horizontal, &v, 200, 900, 1, a.mouse)
			if v != a.set.SearchWidth {
				a.updateSettings(func(s *config.Settings) { s.SearchWidth = v })
			}
			return d
		}),
		layout.Flexed(1, a.layoutMain),
	)
}

// layoutMain draws the request list and, with a request selected, its
// details beside the narrowed list, as the DevTools network panel does.
func (a *App) layoutMain(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	sel := a.list.selected()
	a.drawnSel = 0
	if sel != nil {
		a.drawnSel = sel.ID
	}
	if sel == nil {
		a.details.reset()
		return a.list.Layout(gtx, false)
	}
	w := min(gtx.Dp(unit.Dp(a.set.NameWidth)), size.X-gtx.Dp(320))
	w = max(w, gtx.Dp(140))
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(w, size.Y))
			return a.list.Layout(gtx, true)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			v := a.set.NameWidth
			d := a.list.split.Layout(gtx, a.th, layout.Horizontal, &v, 140, 2000, 1, a.mouse)
			if v != a.set.NameWidth {
				a.updateSettings(func(s *config.Settings) { s.NameWidth = v })
			}
			return d
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return a.details.Layout(gtx, sel)
		}),
	)
}

func (a *App) shortcuts(gtx layout.Context) {
	var filters []event.Filter
	modal := len(a.dialogs) > 0
	if a.menu != nil || modal {
		filters = append(filters, key.Filter{Name: key.NameEscape})
	}
	if modal {
		top := a.dialogs[len(a.dialogs)-1]
		if m, ok := top.(multiline); !ok || !m.Multiline() {
			filters = append(filters, key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		}
	} else {
		for _, n := range []key.Name{"E", "L", "F", "O", "S", "K"} {
			filters = append(filters, key.Filter{Name: n, Required: key.ModCtrl, Optional: key.ModShift})
		}
		filters = append(filters, key.Filter{Name: key.NameF5}, key.Filter{Name: key.NameEscape})
	}
	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		ke, ok := e.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch {
		case ke.Name == key.NameEscape:
			switch {
			case a.menu != nil:
				a.closeMenu()
			case len(a.dialogs) > 0:
				a.dialogs[len(a.dialogs)-1].Cancel(a)
			case a.list.selected() != nil:
				a.list.clearSelection()
			case a.search.open:
				a.search.open = false
			}
		case ke.Name == key.NameReturn || ke.Name == key.NameEnter:
			if n := len(a.dialogs); n > 0 {
				a.dialogs[n-1].Submit(a)
			}
		case ke.Name == "E":
			a.toggleRecording()
		case ke.Name == "L":
			a.clearAll()
		case ke.Name == "F":
			if ke.Modifiers.Contain(key.ModShift) {
				a.updateSettings(func(s *config.Settings) { s.ShowFilter = true })
				a.later(func(gtx layout.Context) { a.filterField.Focus(gtx) })
			} else {
				a.search.toggle(true)
			}
		case ke.Name == "O":
			a.pickImport()
		case ke.Name == "S":
			a.pickExport(ke.Modifiers.Contain(key.ModShift))
		case ke.Name == "K":
			a.Open(newComposeDialog(a, a.list.selected()))
		case ke.Name == key.NameF5:
			if s := a.list.selected(); s != nil {
				a.svc.engine.Replay(s)
			}
		}
	}
}

func (a *App) toggleRecording() {
	on := !a.svc.engine.Recording()
	a.svc.engine.SetRecording(on)
}

func (a *App) clearAll() {
	a.store.Clear()
	a.list.clearSelection()
	a.search.clearResults()
}

const titleH = unit.Dp(40)

var modeChoices = [3][2]string{
	{config.ModeManual, "仅代理端口"},
	{config.ModeSystem, "系统代理"},
	{config.ModeTUN, "虚拟网卡"},
}

func (a *App) layoutTitlebar(gtx layout.Context) layout.Dimensions {
	th := a.th
	h := gtx.Dp(titleH)
	w := gtx.Constraints.Max.X
	bar := image.Rectangle{Max: image.Pt(w, h)}
	drag := clip.Rect(bar).Push(gtx.Ops)
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
	drag.Pop()
	fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), w, h), th.Border)

	if a.logoClk.Clicked(gtx) {
		a.appMenu()
	}
	winBtns := []*widget.Clickable{&a.winBtns0, &a.winBtns1, &a.winBtns2}
	if winBtns[0].Clicked(gtx) {
		a.host.SetWindowMode(app.Minimized)
	}
	if winBtns[1].Clicked(gtx) {
		if a.maximized {
			a.host.SetWindowMode(app.Windowed)
		} else {
			a.host.SetWindowMode(app.Maximized)
		}
	}
	if winBtns[2].Clicked(gtx) {
		a.quit()
	}
	for i := range a.modeClk {
		if a.modeClk[i].Clicked(gtx) {
			a.svc.requestMode(modeChoices[i][0])
		}
	}
	if a.certBtn.Clicked(gtx) {
		a.Open(newCertDialog(a))
	}
	if a.rulesBtn.Clicked(gtx) {
		a.Open(newRulesDialog(a))
	}
	if a.bpBtn.Clicked(gtx) {
		a.Open(newBreakDialog(a))
	}
	if a.composeBtn.Clicked(gtx) {
		a.Open(newComposeDialog(a, a.list.selected()))
	}
	if a.setBtn.Clicked(gtx) {
		a.Open(newSettingsDialog(a))
	}

	at := func(x, y int, wd layout.Widget) int {
		st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		d := wd(gtx)
		st.Pop()
		return x + d.Size.X
	}
	x := at(0, 0, func(gtx layout.Context) layout.Dimensions {
		sz := image.Pt(gtx.Dp(46), h)
		return a.logoClk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if a.logoClk.Hovered() {
				fillRR(gtx.Ops, image.Rectangle{Max: sz}.Inset(gtx.Dp(5)), gtx.Dp(6), th.Bg3)
			}
			ls := gtx.Dp(24)
			st := op.Offset(image.Pt((sz.X-ls)/2, (sz.Y-ls)/2)).Push(gtx.Ops)
			drawLogo(gtx, ls, th.Text, th.Accent)
			st.Pop()
			return layout.Dimensions{Size: sz}
		})
	})
	x = at(x, 0, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(100), h))
		return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return th.txtW(gtx, "NLR Capture", 13, th.Text, font.SemiBold)
		})
	})

	// Capture mode switch.
	segH := gtx.Dp(28)
	x = at(x+gtx.Dp(8), (h-segH)/2, func(gtx layout.Context) layout.Dimensions {
		return a.modeSwitch(gtx, segH)
	})

	btnW := gtx.Dp(46)
	right := w
	if !a.decorated {
		right -= 3 * btnW
		for i := range 3 {
			at(right+i*btnW, 0, func(gtx layout.Context) layout.Dimensions {
				sz := image.Pt(btnW, h)
				return winBtns[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					fg := th.Text2
					if winBtns[i].Hovered() {
						c := th.Bg3
						fg = th.Text
						if i == 2 {
							c, fg = rgb(0xe5484d), color.NRGBA{R: 255, G: 255, B: 255, A: 255}
						}
						fill(gtx.Ops, image.Rectangle{Max: sz}, c)
					}
					kind := []int{0, 1, 3}[i]
					if i == 1 && a.maximized {
						kind = 2
					}
					drawWinGlyph(gtx.Ops, kind, image.Pt(sz.X/2, sz.Y/2), float32(gtx.Dp(10)), fg, float32(gtx.Dp(1)))
					return layout.Dimensions{Size: sz}
				})
			})
		}
	}
	type toolBtn struct {
		clk    *widget.Clickable
		ic     *Icon
		active bool
		title  string
		badge  int
	}
	cfg := a.set
	rulesOn := false
	for _, r := range cfg.Rules {
		rulesOn = rulesOn || r.Enabled
	}
	certTitle := "HTTPS 证书"
	certIc := icLock
	if !cfg.Decrypt {
		certIc = icLockOpen
		certTitle = "HTTPS 证书"
	}
	tools := []toolBtn{
		{&a.certBtn, certIc, cfg.Decrypt, certTitle, 0},
		{&a.rulesBtn, icRules, rulesOn, "自动响应规则", 0},
		{&a.bpBtn, icPause, cfg.Breakpoints.Request || cfg.Breakpoints.Response, "断点", len(a.svc.engine.Paused())},
		{&a.composeBtn, icSend, false, "构造请求", 0},
		{&a.setBtn, icSettings, false, "设置", 0},
	}
	tool := gtx.Dp(32)
	right -= gtx.Dp(8) + len(tools)*tool
	for i, t := range tools {
		st := op.Offset(image.Pt(right+i*tool, (h-gtx.Dp(28))/2)).Push(gtx.Ops)
		d := th.iconButton(gtx, t.clk, t.ic, 28, 17, th.Text2, t.active, t.title)
		if t.badge > 0 {
			s := gtx.Dp(14)
			r := image.Rect(d.Size.X-s+gtx.Dp(2), -gtx.Dp(2), d.Size.X+gtx.Dp(2), s-gtx.Dp(2))
			fillRR(gtx.Ops, r, s/2, th.Warn)
			bs := op.Offset(r.Min).Push(gtx.Ops)
			cg := gtx
			cg.Constraints = layout.Exact(r.Size())
			layout.Center.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				return th.txtW(gtx, itoa(t.badge), 9, rgb(0x101317), font.Bold)
			})
			bs.Pop()
		}
		st.Pop()
	}
	_ = x
	return layout.Dimensions{Size: bar.Max}
}

// modeSwitch draws the capture mode choice.
func (a *App) modeSwitch(gtx layout.Context, h int) layout.Dimensions {
	th := a.th
	cur := a.svc.mode()
	pending := a.svc.pendingMode()
	gtx.Constraints.Min = image.Point{}
	m := op.Record(gtx.Ops)
	children := make([]layout.FlexChild, len(modeChoices))
	for i := range modeChoices {
		i := i
		children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return a.modeClk[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				id := modeChoices[i][0]
				sel := cur == id
				busy := pending == id
				gtx.Constraints.Min = image.Point{}
				mm := op.Record(gtx.Ops)
				d := layout.Inset{Left: 10, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							s := gtx.Dp(7)
							c := th.Text3
							switch {
							case busy:
								c = th.Warn
							case sel && id != config.ModeManual:
								c = th.Accent
							case sel:
								c = th.Blue
							}
							st := op.Offset(image.Pt(0, 0)).Push(gtx.Ops)
							dot(gtx, s, c)
							st.Pop()
							return layout.Dimensions{Size: image.Pt(s+gtx.Dp(7), s)}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							c := th.Text2
							if sel {
								c = th.Text
							}
							return th.txt(gtx, modeChoices[i][1], 12, c)
						}),
					)
				})
				call := mm.Stop()
				r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
				if sel {
					fillRR(gtx.Ops, r.Inset(gtx.Dp(2)), gtx.Dp(5), th.Bg4)
				} else if a.modeClk[i].Hovered() {
					fillRR(gtx.Ops, r.Inset(gtx.Dp(2)), gtx.Dp(5), th.Bg3)
				}
				st := op.Offset(image.Pt(0, (h-d.Size.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				st.Pop()
				return layout.Dimensions{Size: r.Max}
			})
		})
	}
	d := layout.Flex{}.Layout(gtx, children...)
	call := m.Stop()
	r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
	fillRR(gtx.Ops, r, gtx.Dp(7), th.Bg0)
	strokeRR(gtx.Ops, r, gtx.Dp(7), float32(gtx.Dp(1)), th.Border)
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: r.Max}
}

// layoutTooltip draws the title of the control the pointer rests on.
func (a *App) layoutTooltip(gtx layout.Context) {
	th := a.th
	t := th.tip
	if !t.seen || a.menu != nil {
		t.key, t.seen = nil, false
		return
	}
	t.seen = false
	const delay = 450 * time.Millisecond
	if dt := gtx.Now.Sub(t.since); dt < delay {
		gtx.Execute(op.InvalidateCmd{At: t.since.Add(delay)})
		return
	}
	if !t.shown {
		t.shown, t.pos, t.shownAt = true, a.mouse, gtx.Now
	}
	rec := op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(520), gtx.Dp(200))}
	d := layout.Inset{Left: 8, Right: 8, Top: 4, Bottom: 5}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
		return Label{Text: t.text, Size: 12, Color: th.Text, MaxLines: 6}.Layout(gtx, th)
	})
	call := rec.Stop()
	pos := t.pos.Add(image.Pt(gtx.Dp(10), gtx.Dp(20)))
	if pos.X+d.Size.X > a.size.X-4 {
		pos.X = max(a.size.X-d.Size.X-4, 4)
	}
	if pos.Y+d.Size.Y > a.size.Y-4 {
		pos.Y = max(t.pos.Y-d.Size.Y-gtx.Dp(8), 4)
	}
	st := op.Offset(pos).Push(gtx.Ops)
	faded(gtx, a.animIn(gtx, t.shownAt, animMenu), image.Point{}, func() {
		r := image.Rectangle{Max: d.Size}
		fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg3)
		strokeRR(gtx.Ops, r, gtx.Dp(5), float32(gtx.Dp(1)), th.BorderHi)
		call.Add(gtx.Ops)
	})
	st.Pop()
}

func (a *App) appMenu() {
	a.MenuAt(image.Pt(a.dp(4), a.dp(titleH)),
		MenuItem{Label: "导入 HAR…", Icon: icImport, Hint: "Ctrl+O", Do: a.pickImport},
		MenuItem{Label: "导出全部为 HAR…", Icon: icExport, Hint: "Ctrl+Shift+S", Do: func() { a.pickExport(true) }},
		MenuItem{Label: "导出筛选结果为 HAR…", Icon: icExport, Hint: "Ctrl+S", Do: func() { a.pickExport(false) }},
		MenuItem{Sep: true},
		MenuItem{Label: "构造请求", Icon: icSend, Hint: "Ctrl+K", Do: func() { a.Open(newComposeDialog(a, nil)) }},
		MenuItem{Label: "自动响应规则", Icon: icRules, Do: func() { a.Open(newRulesDialog(a)) }},
		MenuItem{Label: "断点", Icon: icPause, Do: func() { a.Open(newBreakDialog(a)) }},
		MenuItem{Label: "HTTPS 证书", Icon: icCert, Do: func() { a.Open(newCertDialog(a)) }},
		MenuItem{Sep: true},
		MenuItem{Label: "清除全部", Icon: icBlock, Hint: "Ctrl+L", Do: a.clearAll},
		MenuItem{Label: "设置", Icon: icSettings, Do: func() { a.Open(newSettingsDialog(a)) }},
		MenuItem{Label: "关于", Icon: icInfo, Do: func() { a.Open(&aboutDialog{}) }},
		MenuItem{Sep: true},
		MenuItem{Label: "退出", Icon: icPower, Do: a.quit},
	)
}

// dp converts dp to pixels using the metric of the last frame.
func (a *App) dp(v unit.Dp) int {
	s := a.pxPerDp
	if s == 0 {
		s = 1
	}
	return int(float32(v)*s + 0.5)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
