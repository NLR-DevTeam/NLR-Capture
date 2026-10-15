package ui

import (
	"image"
	"image/color"
	"slices"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/winutil"
)

// ---- Context menu -----------------------------------------------------

// MenuItem is one entry of a popup menu. An item with Sub opens a submenu
// to its side.
type MenuItem struct {
	Label    string
	Icon     *Icon
	Hint     string // shortcut text shown at the right
	Danger   bool
	Disabled bool
	Checked  bool
	Sep      bool // a separator line; other fields are ignored
	Do       func()
	Sub      []MenuItem
}

type menu struct {
	items  []MenuItem
	clicks []widget.Clickable
	pos    image.Point
	scrim  bool
	// keepFocus leaves keyboard focus where it was when the menu closes,
	// for menus that act on a focused input.
	keepFocus bool
	// opened and closed time the menu's fade in and out.
	opened, closed time.Time

	// child is the open submenu, opened from item childOf.
	child   *menu
	childOf int
	// drawn is where the menu was last drawn and rows where its items
	// were, in window coordinates; submenus are placed by them.
	drawn image.Rectangle
	rows  []image.Rectangle
	// parentLeft is the left edge of a submenu's parent, where the
	// submenu goes when it does not fit on the right.
	parentLeft int
}

// Menu opens a popup menu at the pointer position.
func (a *App) Menu(items ...MenuItem) {
	a.MenuAt(a.mouse, items...)
}

// MenuAt opens a popup menu at pos (window coordinates).
func (a *App) MenuAt(pos image.Point, items ...MenuItem) {
	a.menu = newMenu(items, pos)
	a.host.Invalidate()
}

func newMenu(items []MenuItem, pos image.Point) *menu {
	return &menu{items: items, clicks: make([]widget.Clickable, len(items)), pos: pos, opened: time.Now(), childOf: -1}
}

func (a *App) closeMenu() {
	if a.menu != nil {
		a.refocus = a.refocus || !a.menu.keepFocus
		// The closed menu stays around to fade out.
		a.menu.closed = time.Now()
		a.menuOut = a.menu
		a.menu = nil
	}
}

// shadowStrength scales the drop shadows: dark stacked layers that read as
// depth on the dark palette turn into a grey halo on white, so the light
// palette uses a fainter one. Theme.Apply sets it.
var shadowStrength float32 = 1

func shadow(ops *op.Ops, r image.Rectangle, radius int) {
	for i := 1; i <= 6; i++ {
		g := i * 2
		rr := image.Rect(r.Min.X-g, r.Min.Y-g+i, r.Max.X+g, r.Max.Y+g+i)
		fillRR(ops, rr, radius+g, color.NRGBA{A: uint8(float32(26-i*3) * shadowStrength)})
	}
}

func (a *App) layoutMenu(gtx layout.Context) {
	if m := a.menu; m != nil {
		a.menuEvents(gtx, m)
	}
	// The fading menu is drawn after the events are handled: a menu closed
	// by a click this frame must still be drawn, or it would vanish for a
	// frame before it fades.
	if out := a.menuOut; out != nil {
		if p := 1 - a.animIn(gtx, out.closed, animMenu); p > 0 {
			// Pass-through, so that a click on the fading menu reaches
			// what is below it.
			pass := pointer.PassOp{}.Push(gtx.Ops)
			a.drawMenuTree(gtx.Disabled(), out, p)
			pass.Pop()
		} else {
			a.menuOut = nil
		}
	}
	m := a.menu
	if m == nil {
		return
	}
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &m.scrim)
	area.Pop()
	a.drawMenuTree(gtx, m, a.animIn(gtx, m.opened, animMenu))
}

func (a *App) drawMenuTree(gtx layout.Context, m *menu, p float32) {
	for x := m; x != nil; x = x.child {
		a.drawMenu(gtx, x, p)
	}
}

// menuEvents handles the clicks on a menu, its submenu and beside it.
func (a *App) menuEvents(gtx layout.Context, m *menu) {
	if m == a.menu {
		for {
			e, ok := gtx.Event(pointer.Filter{Target: &m.scrim, Kinds: pointer.Press})
			if !ok {
				break
			}
			if _, ok := e.(pointer.Event); ok {
				a.closeMenu()
				return
			}
		}
	}
	for i := range m.items {
		it := &m.items[i]
		if it.Sep || it.Disabled {
			continue
		}
		clicked := m.clicks[i].Clicked(gtx)
		hovered := m.clicks[i].Hovered()
		if len(it.Sub) > 0 {
			if (hovered || clicked) && m.childOf != i && i < len(m.rows) {
				row := m.rows[i]
				c := newMenu(it.Sub, image.Pt(m.drawn.Max.X-gtx.Dp(4), row.Min.Y-gtx.Dp(5)))
				c.parentLeft = m.drawn.Min.X + gtx.Dp(4)
				m.child, m.childOf = c, i
				gtx.Execute(op.InvalidateCmd{})
			}
			continue
		}
		if hovered && m.child != nil {
			// Moving onto a plain item closes the open submenu.
			m.child, m.childOf = nil, -1
		}
		if clicked {
			a.closeMenu()
			if it.Do != nil {
				it.Do()
			}
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	if m.child != nil {
		a.menuEvents(gtx, m.child)
	}
}

// drawMenu draws m at opacity p. A disabled context draws a menu that is
// fading out and no longer takes input.
func (a *App) drawMenu(gtx layout.Context, m *menu, p float32) {
	th := a.th
	// Measure the widest row.
	width := gtx.Dp(180)
	for i := range m.items {
		it := &m.items[i]
		if it.Sep {
			continue
		}
		rec := op.Record(gtx.Ops)
		mg := gtx
		mg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(420), gtx.Dp(40))}
		w := th.txt(mg, it.Label, 13, th.Text).Size.X + gtx.Dp(12+16+10+12)
		if it.Hint != "" {
			w += th.txt(mg, it.Hint, 12, th.Text3).Size.X + gtx.Dp(28)
		}
		if len(it.Sub) > 0 {
			w += gtx.Dp(28)
		}
		rec.Stop()
		width = max(width, w)
	}

	rows := make([]image.Rectangle, len(m.items))
	rec := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Constraints{Min: image.Pt(width, 0), Max: image.Pt(width, gtx.Constraints.Max.Y)}
	children := make([]layout.FlexChild, 0, len(m.items)+2)
	children = append(children, vspace(5))
	y := gtx.Dp(5)
	for i := range m.items {
		i := i
		it := &m.items[i]
		if it.Sep {
			h := gtx.Dp(9)
			y += h
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				fill(gtx.Ops, image.Rect(0, h/2, width, h/2+max(gtx.Dp(1), 1)), th.Border)
				return layout.Dimensions{Size: image.Pt(width, h)}
			}))
			continue
		}
		h := gtx.Dp(30)
		rows[i] = image.Rect(0, y, width, y+h)
		y += h
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(width, h))
			if it.Disabled {
				gtx = gtx.Disabled()
			}
			return m.clicks[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				fg := th.Text
				switch {
				case it.Disabled:
					fg = th.Text3
				case it.Danger:
					fg = th.Danger
				}
				if (m.clicks[i].Hovered() || m.childOf == i) && !it.Disabled {
					c := th.Bg4
					if it.Danger {
						c = alpha(th.Danger, 0x28)
					}
					fillRR(gtx.Ops, image.Rect(gtx.Dp(5), 0, width-gtx.Dp(5), h), gtx.Dp(5), c)
				}
				layout.Inset{Left: 12, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							s := gtx.Dp(16)
							ic := it.Icon
							if it.Checked {
								ic = icCheck
							}
							if ic != nil {
								drawIcon(gtx, ic, 16, mix(fg, th.Bg2, 0.25))
							}
							return layout.Dimensions{Size: image.Pt(s+gtx.Dp(10), s)}
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, it.Label, 13, fg)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if len(it.Sub) > 0 {
								return drawIcon(gtx, icChevron, 14, th.Text2)
							}
							if it.Hint == "" {
								return layout.Dimensions{}
							}
							return th.txt(gtx, it.Hint, 12, th.Text3)
						}),
					)
				})
				return layout.Dimensions{Size: image.Pt(width, h)}
			})
		}))
	}
	children = append(children, vspace(5))
	d := layout.Flex{Axis: layout.Vertical}.Layout(cgtx, children...)
	call := rec.Stop()

	pos := m.pos
	win := gtx.Constraints.Max
	if pos.X+d.Size.X > win.X-4 {
		if m.parentLeft > 0 {
			pos.X = max(m.parentLeft-d.Size.X, 4)
		} else {
			pos.X = max(win.X-d.Size.X-4, 4)
		}
	}
	if pos.Y+d.Size.Y > win.Y-4 {
		if m.parentLeft > 0 {
			pos.Y = max(win.Y-d.Size.Y-4, 4)
		} else {
			pos.Y = max(pos.Y-d.Size.Y, 4)
		}
	}
	m.drawn = image.Rectangle{Min: pos, Max: pos.Add(d.Size)}
	for i := range rows {
		rows[i] = rows[i].Add(pos)
	}
	m.rows = rows
	st := op.Offset(pos).Push(gtx.Ops)
	faded(gtx, p, image.Pt(0, -gtx.Dp(6)), func() {
		r := image.Rectangle{Max: d.Size}
		shadow(gtx.Ops, r, gtx.Dp(8))
		fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
		strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), th.BorderHi)
		// The menu itself swallows clicks that miss an item.
		ca := clip.Rect(r).Push(gtx.Ops)
		if gtx.Enabled() {
			event.Op(gtx.Ops, m)
		}
		call.Add(gtx.Ops)
		ca.Pop()
	})
	st.Pop()
}

// ---- Dialogs ----------------------------------------------------------

// Dialog is a modal window drawn above the main content.
type Dialog interface {
	Layout(gtx layout.Context, a *App) layout.Dimensions
	// Submit is called when Enter is pressed.
	Submit(a *App)
	// Cancel is called when Escape is pressed or the dialog is dismissed.
	Cancel(a *App)
}

// multiline dialogs keep Enter for their text editor.
type multiline interface{ Multiline() bool }

// Open shows a dialog.
func (a *App) Open(d Dialog) {
	a.dialogs = append(a.dialogs, d)
	if a.dialogAt == nil {
		a.dialogAt = map[Dialog]time.Time{}
	}
	a.dialogAt[d] = time.Now()
	for i, x := range a.dialogsOut {
		if x == d {
			a.dialogsOut = append(a.dialogsOut[:i], a.dialogsOut[i+1:]...)
			break
		}
	}
	a.closeMenu()
	a.host.Invalidate()
}

// Close removes a dialog.
func (a *App) Close(d Dialog) {
	for i, x := range a.dialogs {
		if x == d {
			a.dialogs = append(a.dialogs[:i], a.dialogs[i+1:]...)
			// The closed dialog stays around to fade out.
			if a.set.Animations {
				a.dialogAt[d] = time.Now()
				a.dialogsOut = append(a.dialogsOut, d)
			} else {
				delete(a.dialogAt, d)
			}
			break
		}
	}
	a.refocus = true
	a.host.Invalidate()
}

func (a *App) layoutDialogs(gtx layout.Context) {
	scrim := color.NRGBA{A: 0x8c}
	if a.th.Light {
		scrim = color.NRGBA{R: 0x1c, G: 0x21, B: 0x28, A: 0x40}
	}
	draw := func(gtx layout.Context, d Dialog, p float32) {
		c := scrim
		c.A = uint8(float32(c.A) * p)
		fill(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, c)
		faded(gtx, p, image.Pt(0, gtx.Dp(10)), func() {
			layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.Layout(gtx, a)
			})
		})
	}
	// A dialog that closes itself while it is laid out has been drawn for
	// this frame; drawing it again as a fading one would darken the scrim
	// for a frame.
	var drawn []Dialog
	for i, d := range append([]Dialog(nil), a.dialogs...) {
		top := i == len(a.dialogs)-1
		drawn = append(drawn, d)
		// Scrim blocks input to everything below.
		area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		event.Op(gtx.Ops, d)
		pointer.CursorDefault.Add(gtx.Ops)
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press}); !ok {
				break
			}
		}
		dgtx := gtx
		if !top {
			dgtx = dgtx.Disabled()
		}
		draw(dgtx, d, a.animIn(gtx, a.dialogAt[d], animDialog))
		area.Pop()
	}
	// Closed dialogs fade out on top. They are pass-through, so that input
	// reaches what is below them.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	defer pass.Pop()
	out := a.dialogsOut[:0]
	for _, d := range a.dialogsOut {
		p := 1 - a.animIn(gtx, a.dialogAt[d], animDialog)
		if p <= 0 {
			delete(a.dialogAt, d)
			continue
		}
		out = append(out, d)
		if !slices.Contains(drawn, d) {
			draw(gtx.Disabled(), d, p)
		}
	}
	for i := len(out); i < len(a.dialogsOut); i++ {
		a.dialogsOut[i] = nil
	}
	a.dialogsOut = out
}

// dialogFrame draws the standard dialog chrome around body and footer.
func (a *App) dialogFrame(gtx layout.Context, title string, width unit.Dp, closeClk *widget.Clickable, d Dialog, body, footer layout.Widget) layout.Dimensions {
	th := a.th
	if closeClk.Clicked(gtx) {
		d.Cancel(a)
	}
	w := min(gtx.Dp(width), gtx.Constraints.Max.X-gtx.Dp(32))
	maxH := gtx.Constraints.Max.Y - gtx.Dp(48)
	rec := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, maxH)}
	dims := layout.Flex{Axis: layout.Vertical}.Layout(cgtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 20, Right: 10, Top: 12, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, title, 15, th.Text, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, closeClk, icClose, 28, 16, th.Text2, false, "关闭")
					}),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			return layout.Inset{Left: 20, Right: 20, Bottom: 4}.Layout(gtx, body)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if footer == nil {
				return layout.Spacer{Height: 16}.Layout(gtx)
			}
			return layout.Inset{Left: 20, Right: 20, Top: 14, Bottom: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.E.Layout(gtx, footer)
			})
		}),
	)
	call := rec.Stop()
	r := image.Rectangle{Max: dims.Size}
	shadow(gtx.Ops, r, gtx.Dp(12))
	fillRR(gtx.Ops, r, gtx.Dp(12), th.Bg1)
	strokeRR(gtx.Ops, r, gtx.Dp(12), float32(gtx.Dp(1)), th.BorderHi)
	call.Add(gtx.Ops)
	return dims
}

// buttonRow lays out dialog buttons right-aligned with spacing.
func buttonRow(gtx layout.Context, btns ...layout.Widget) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(btns)*2)
	for i, b := range btns {
		if i > 0 {
			children = append(children, hspace(8))
		}
		children = append(children, layout.Rigid(b))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
}

// confirmDialog asks a yes/no question.
type confirmDialog struct {
	title   string
	message string
	detail  string // monospaced, e.g. a fingerprint
	okLabel string
	danger  bool
	onOK    func()
	onNo    func()
	// check is an optional check box below the message.
	check   *widget.Bool
	checkLb string

	closeClk, okClk, noClk widget.Clickable
}

func (d *confirmDialog) Submit(a *App) {
	a.Close(d)
	if d.onOK != nil {
		d.onOK()
	}
}

func (d *confirmDialog) Cancel(a *App) {
	a.Close(d)
	if d.onNo != nil {
		d.onNo()
	}
}

func (d *confirmDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	ok := d.okLabel
	if ok == "" {
		ok = "确定"
	}
	kind := btnPrimary
	if d.danger {
		kind = btnDanger
	}
	return a.dialogFrame(gtx, d.title, 440, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.message == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return Label{Text: d.message, Size: 13, Color: th.Text2, MaxLines: -1}.Layout(gtx, th)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.detail == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						rec := op.Record(gtx.Ops)
						dm := layout.UniformInset(10).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return Label{Text: d.detail, Size: 12, Color: th.Text, Mono: true, MaxLines: -1}.Layout(gtx, th)
						})
						call := rec.Stop()
						r := image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, dm.Size.Y)}
						fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg0)
						strokeRR(gtx.Ops, r, gtx.Dp(6), float32(gtx.Dp(1)), th.Border)
						call.Add(gtx.Ops)
						return layout.Dimensions{Size: r.Max}
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if d.check == nil {
						return layout.Dimensions{}
					}
					return th.checkbox(gtx, d.check, d.checkLb)
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, ok, nil, kind) },
			)
		})
}

// inputDialog asks for one or more lines of text.
type inputDialog struct {
	title   string
	message string
	fields  []*Field
	check   *widget.Bool // optional checkbox
	checkLb string
	okLabel string
	onOK    func(values []string, checked bool)
	onNo    func()

	focused                bool
	closeClk, okClk, noClk widget.Clickable
}

func newInputDialog(title, label, value string, onOK func(string)) *inputDialog {
	f := &Field{Label: label}
	f.SetText(value)
	return &inputDialog{title: title, fields: []*Field{f}, onOK: func(v []string, _ bool) { onOK(v[0]) }}
}

func (d *inputDialog) Submit(a *App) {
	vals := make([]string, len(d.fields))
	for i, f := range d.fields {
		vals[i] = f.Text()
	}
	a.Close(d)
	if d.onOK != nil {
		d.onOK(vals, d.check != nil && d.check.Value)
	}
}

func (d *inputDialog) Cancel(a *App) {
	a.Close(d)
	if d.onNo != nil {
		d.onNo()
	}
}

func (d *inputDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if !d.focused && len(d.fields) > 0 {
		d.focused = true
		d.fields[0].Focus(gtx)
		// Select the name part of a file name for quick renaming.
		n := d.fields[0].Editor.Len()
		d.fields[0].Editor.SetCaret(n, 0)
	}
	for _, f := range d.fields {
		f.Events(gtx)
	}
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.noClk.Clicked(gtx) {
		d.Cancel(a)
	}
	ok := d.okLabel
	if ok == "" {
		ok = "确定"
	}
	return a.dialogFrame(gtx, d.title, 420, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			var rows []layout.FlexChild
			if d.message != "" {
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return Label{Text: d.message, Size: 13, Color: th.Text2, MaxLines: -1}.Layout(gtx, th)
					})
				}))
			}
			for i, f := range d.fields {
				f := f
				if i > 0 {
					rows = append(rows, vspace(10))
				}
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return f.Layout(gtx, th) }))
			}
			if d.check != nil {
				rows = append(rows, vspace(12), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.checkbox(gtx, d.check, d.checkLb)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.noClk, "取消", nil, btnDefault) },
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, ok, nil, btnPrimary) },
			)
		})
}

// ---- Toasts -----------------------------------------------------------

const (
	toastInfo = iota
	toastOK
	toastError
)

type toast struct {
	kind  int
	msg   string
	born  time.Time
	until time.Time
}

// Toast shows a transient message in the corner of the window.
func (a *App) Toast(kind int, msg string) {
	d := 3500 * time.Millisecond
	if kind == toastError {
		d = 7 * time.Second
	}
	now := time.Now()
	a.toasts = append(a.toasts, toast{kind: kind, msg: msg, born: now, until: now.Add(d)})
	if len(a.toasts) > 4 {
		a.toasts = a.toasts[len(a.toasts)-4:]
	}
	a.host.Invalidate()
}

func (a *App) layoutToasts(gtx layout.Context) {
	th := a.th
	live := a.toasts[:0]
	for _, t := range a.toasts {
		if t.until.After(gtx.Now) {
			live = append(live, t)
		}
	}
	a.toasts = live
	if len(live) == 0 {
		return
	}
	// Toasts stack downwards from the top right corner, where they do not
	// cover the buttons along the bottom edge.
	y := gtx.Dp(titleH) + gtx.Dp(12)
	for i := len(live) - 1; i >= 0; i-- {
		t := live[i]
		gtx.Execute(op.InvalidateCmd{At: t.until})
		ic, c := icInfo, th.Blue
		switch t.kind {
		case toastOK:
			ic, c = icDone, th.Accent
		case toastError:
			ic, c = icError, th.Danger
		}
		rec := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(min(gtx.Dp(460), gtx.Constraints.Max.X-gtx.Dp(32)), gtx.Dp(200))}
		d := layout.Inset{Left: 12, Right: 14, Top: 10, Bottom: 10}.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Start}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, 18, c) }),
				hspace(10),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					return Label{Text: t.msg, Size: 13, Color: th.Text, MaxLines: 4}.Layout(gtx, th)
				}),
			)
		})
		call := rec.Stop()
		// Toasts slide in from the right and fade away at the end.
		p := a.animIn(gtx, t.born, animToast)
		if a.set.Animations {
			if left := t.until.Sub(gtx.Now); left < animToast {
				p = min(p, float32(left)/float32(animToast))
				gtx.Execute(op.InvalidateCmd{})
			} else {
				gtx.Execute(op.InvalidateCmd{At: t.until.Add(-animToast)})
			}
		}
		pos := image.Pt(gtx.Constraints.Max.X-d.Size.X-gtx.Dp(16), y)
		st := op.Offset(pos).Push(gtx.Ops)
		faded(gtx, p, image.Pt(gtx.Dp(16), 0), func() {
			r := image.Rectangle{Max: d.Size}
			shadow(gtx.Ops, r, gtx.Dp(8))
			fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
			strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), mix(th.BorderHi, c, 0.35))
			call.Add(gtx.Ops)
		})
		st.Pop()
		y += d.Size.Y + gtx.Dp(8)
	}
}

// aboutDialog shows the version.
type aboutDialog struct {
	closeClk, okClk, dataClk widget.Clickable
}

func (d *aboutDialog) Submit(a *App) { a.Close(d) }
func (d *aboutDialog) Cancel(a *App) { a.Close(d) }

func (d *aboutDialog) Layout(gtx layout.Context, a *App) layout.Dimensions {
	th := a.th
	if d.okClk.Clicked(gtx) {
		d.Submit(a)
	}
	if d.dataClk.Clicked(gtx) {
		go winutil.Open(a.dataDir)
	}
	return a.dialogFrame(gtx, "NLR Capture", 400, &d.closeClk, d,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return drawLogo(gtx, gtx.Dp(48), th.Text, th.Accent)
				}),
				hspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return column(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, "原生 HTTP/HTTPS 抓包调试工具", 14, th.Text)
						}),
						vspace(6),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return th.txt(gtx, "版本："+Version, 13, th.Text2)
						}),
					)
				}),
			)
		},
		func(gtx layout.Context) layout.Dimensions {
			return buttonRow(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return th.button(gtx, &d.dataClk, "打开数据目录", nil, btnDefault)
				},
				func(gtx layout.Context) layout.Dimensions { return th.button(gtx, &d.okClk, "确定", nil, btnPrimary) },
			)
		})
}
