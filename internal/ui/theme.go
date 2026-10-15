// Package ui is the Gio user interface of NLR Capture. Its look and its
// building blocks (theme, widgets, menus, dialogs) come from NLR Shell.
package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/gesture"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Theme holds fonts and colors shared by all widgets.
type Theme struct {
	Shaper *text.Shaper
	Mat    *material.Theme
	Face   font.Typeface
	Mono   font.Typeface
	tip    *tooltip
	// animate mirrors the animations setting; scrolls holds the lists that
	// are still travelling after a turn of the wheel.
	animate bool
	scrolls map[*widget.List]*smoothScroll
	hovers  map[*widget.List]*gesture.Hover

	Bg0      color.NRGBA // window background, inputs
	Bg1      color.NRGBA // panels
	Bg2      color.NRGBA // raised surfaces
	Bg3      color.NRGBA // hover
	Bg4      color.NRGBA // pressed, selected
	Border   color.NRGBA
	BorderHi color.NRGBA
	Text     color.NRGBA
	Text2    color.NRGBA
	Text3    color.NRGBA
	Accent   color.NRGBA
	AccentFg color.NRGBA
	// AccentText is the accent for text and thin icons: on the light
	// palette the bright accent colors are too pale to read on white.
	AccentText color.NRGBA
	Blue       color.NRGBA
	Purple     color.NRGBA
	Warn       color.NRGBA
	Danger     color.NRGBA
	Green      color.NRGBA
	Teal       color.NRGBA
	Orange     color.NRGBA
	Select     color.NRGBA // text selection
	Stripe     color.NRGBA // alternate table rows

	// Syntax colors for the JSON viewer.
	SynKey, SynString, SynNumber, SynKeyword color.NRGBA

	// Light reports whether the UI uses the light palette.
	Light bool
	// editMenu opens the cut/copy/paste menu of an input; set by the App.
	editMenu func(e *widget.Editor)
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func alpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// mix blends a towards b by t in [0,1].
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	f := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t) }
	return color.NRGBA{R: f(a.R, b.R), G: f(a.G, b.G), B: f(a.B, b.B), A: f(a.A, b.A)}
}

// NewTheme builds the theme in the default dark palette.
func NewTheme() *Theme {
	th := &Theme{
		Shaper: text.NewShaper(text.WithCollection(gofont.Collection())),
		tip:    new(tooltip),
		Face:   uiFont,
		Mono:   monoFont,
	}
	m := material.NewTheme()
	m.Shaper = th.Shaper
	m.Face = th.Face
	m.TextSize = 13
	th.Mat = m
	th.Apply(false, rgb(0x00b935))
	return th
}

// Apply switches the palette: light selects the light colors, and accent
// replaces the brand color.
func (th *Theme) Apply(light bool, accent color.NRGBA) {
	th.Light = light
	if light {
		th.Bg0 = rgb(0xffffff)
		th.Bg1 = rgb(0xf3f4f6)
		th.Bg2 = rgb(0xffffff)
		th.Bg3 = rgb(0xe9ebef)
		th.Bg4 = rgb(0xdde1e6)
		th.Border = rgb(0xe1e4e8)
		th.BorderHi = rgb(0xd0d5db)
		th.Text = rgb(0x1c2128)
		th.Text2 = rgb(0x555f6b)
		th.Text3 = rgb(0x8a929c)
		th.Blue = rgb(0x0969da)
		th.Purple = rgb(0x8250df)
		th.Warn = rgb(0xbf8700)
		th.Danger = rgb(0xd1242f)
		th.Green = rgb(0x1a7f37)
		th.Teal = rgb(0x1b7c83)
		th.Orange = rgb(0xbc4c00)
		th.Stripe = rgb(0xf7f8fa)
		th.SynKey = rgb(0x8250df)
		th.SynString = rgb(0x0a3069)
		th.SynNumber = rgb(0x0550ae)
		th.SynKeyword = rgb(0xcf222e)
	} else {
		th.Bg0 = rgb(0x0b0d10)
		th.Bg1 = rgb(0x101317)
		th.Bg2 = rgb(0x171b21)
		th.Bg3 = rgb(0x1e242c)
		th.Bg4 = rgb(0x27303a)
		th.Border = rgb(0x232a33)
		th.BorderHi = rgb(0x34404d)
		th.Text = rgb(0xd9dee6)
		th.Text2 = rgb(0x8d97a6)
		th.Text3 = rgb(0x5a6472)
		th.Blue = rgb(0x5aa9ff)
		th.Purple = rgb(0xb18cff)
		th.Warn = rgb(0xf2c45a)
		th.Danger = rgb(0xff6b6b)
		th.Green = rgb(0x3ddc97)
		th.Teal = rgb(0x4fd6d6)
		th.Orange = rgb(0xffa657)
		th.Stripe = rgb(0x0e1115)
		th.SynKey = rgb(0xc58cff)
		th.SynString = rgb(0xf2c45a)
		th.SynNumber = rgb(0x8cc4ff)
		th.SynKeyword = rgb(0xff8f8f)
	}
	th.Accent = accent
	th.AccentFg = rgb(0xffffff)
	th.AccentText = accent
	if light {
		th.AccentText = mix(accent, color.NRGBA{A: 0xff}, 0.3)
	}
	th.Select = alpha(accent, 0x50)
	th.Mat.Palette = material.Palette{Bg: th.Bg1, Fg: th.Text, ContrastBg: th.Accent, ContrastFg: th.AccentFg}
	shadowStrength = 1
	if light {
		shadowStrength = 0.4
	}
}

// ---- Drawing helpers --------------------------------------------------

func fill(ops *op.Ops, r image.Rectangle, c color.NRGBA) {
	if r.Empty() {
		return
	}
	paint.FillShape(ops, c, clip.Rect(r).Op())
}

func fillRR(ops *op.Ops, r image.Rectangle, radius int, c color.NRGBA) {
	if r.Empty() {
		return
	}
	paint.FillShape(ops, c, clip.UniformRRect(r, fitRadius(r, radius)).Op(ops))
}

// fitRadius limits a corner radius to half the short side of r. A larger
// one makes the corner arcs overshoot and draws shapes far outside r.
func fitRadius(r image.Rectangle, radius int) int {
	return min(radius, min(r.Dx(), r.Dy())/2)
}

// strokeRR draws a border of the given width just inside r. The outer edge
// follows exactly the outline that fillRR(r, radius) fills.
func strokeRR(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	if r.Empty() || width <= 0 {
		return
	}
	w := int(width + 0.5)
	if w < 1 {
		w = 1
	}
	if 2*w >= r.Dx() || 2*w >= r.Dy() {
		fillRR(ops, r, radius, c)
		return
	}
	radius = fitRadius(r, radius)
	if radius <= 0 {
		fill(ops, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), c)
		fill(ops, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), c)
		fill(ops, image.Rect(r.Min.X, r.Min.Y+w, r.Min.X+w, r.Max.Y-w), c)
		fill(ops, image.Rect(r.Max.X-w, r.Min.Y+w, r.Max.X, r.Max.Y-w), c)
		return
	}
	rad := float32(min(radius, r.Dx()/2, r.Dy()/2))
	fw := float32(w)
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	var p clip.Path
	p.Begin(ops)
	rrOutline(&p, x0, y0, x1, y1, rad, true)
	rrOutline(&p, x0+fw, y0+fw, x1-fw, y1-fw, max(rad-fw, 0), false)
	paint.FillShape(ops, c, clip.Outline{Path: p.End()}.Op())
}

// rrOutline appends a closed rounded rectangle to p, clockwise or
// counter-clockwise, with corners drawn as cubic Bézier quarter circles.
func rrOutline(p *clip.Path, x0, y0, x1, y1, r float32, clockwise bool) {
	const k = 0.5522847498
	c := r * (1 - k)
	pt := f32.Pt
	if clockwise {
		p.MoveTo(pt(x0+r, y0))
		p.LineTo(pt(x1-r, y0))
		p.CubeTo(pt(x1-c, y0), pt(x1, y0+c), pt(x1, y0+r))
		p.LineTo(pt(x1, y1-r))
		p.CubeTo(pt(x1, y1-c), pt(x1-c, y1), pt(x1-r, y1))
		p.LineTo(pt(x0+r, y1))
		p.CubeTo(pt(x0+c, y1), pt(x0, y1-c), pt(x0, y1-r))
		p.LineTo(pt(x0, y0+r))
		p.CubeTo(pt(x0, y0+c), pt(x0+c, y0), pt(x0+r, y0))
	} else {
		p.MoveTo(pt(x0+r, y0))
		p.CubeTo(pt(x0+c, y0), pt(x0, y0+c), pt(x0, y0+r))
		p.LineTo(pt(x0, y1-r))
		p.CubeTo(pt(x0, y1-c), pt(x0+c, y1), pt(x0+r, y1))
		p.LineTo(pt(x1-r, y1))
		p.CubeTo(pt(x1-c, y1), pt(x1, y1-c), pt(x1, y1-r))
		p.LineTo(pt(x1, y0+r))
		p.CubeTo(pt(x1, y0+c), pt(x1-c, y0), pt(x1-r, y0))
		p.LineTo(pt(x0+r, y0))
	}
	p.Close()
}

func line(ops *op.Ops, a, b f32.Point, width float32, c color.NRGBA) {
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(a)
	p.LineTo(b)
	paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func colorMat(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// Label describes one run of text.
type Label struct {
	Text      string
	Size      unit.Sp
	Color     color.NRGBA
	Weight    font.Weight
	Mono      bool
	Italic    bool
	MaxLines  int // 0 means 1; -1 means unlimited
	Alignment text.Alignment
}

// Layout draws the label.
func (l Label) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	f := font.Font{Typeface: th.Face, Weight: l.Weight}
	if l.Mono {
		f.Typeface = th.Mono
	}
	if l.Italic {
		f.Style = font.Italic
	}
	if l.Size == 0 {
		l.Size = 13
	}
	lines := l.MaxLines
	switch lines {
	case 0:
		lines = 1
	case -1:
		lines = 0
	}
	gtx.Constraints.Min.Y = 0
	wl := widget.Label{MaxLines: lines, Alignment: l.Alignment}
	if lines != 1 {
		return wl.Layout(gtx, th.Shaper, f, l.Size, l.Text, colorMat(gtx, l.Color))
	}
	// A single line gets the same box whatever the font, with the glyphs
	// centered in it, so that code and UI text side by side line up: the
	// fonts' own line boxes differ in ascent and descent.
	rec := op.Record(gtx.Ops)
	d := wl.Layout(gtx, th.Shaper, f, l.Size, l.Text, colorMat(gtx, l.Color))
	call := rec.Stop()
	px := gtx.Metric.PxPerSp * float32(l.Size)
	h := int(px*lineBox + 0.5)
	base := h/2 + int(textMiddle*px+0.5)
	st := op.Offset(image.Pt(0, base-(d.Size.Y-d.Baseline))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
	return layout.Dimensions{Size: image.Pt(d.Size.X, h), Baseline: h - base}
}

// lineBox is the height of a single line of text, in em.
const lineBox = 1.36

// txt is shorthand for a single-line label.
func (th *Theme) txt(gtx layout.Context, s string, size unit.Sp, c color.NRGBA) layout.Dimensions {
	return Label{Text: s, Size: size, Color: c}.Layout(gtx, th)
}

func (th *Theme) txtW(gtx layout.Context, s string, size unit.Sp, c color.NRGBA, w font.Weight) layout.Dimensions {
	return Label{Text: s, Size: size, Color: c, Weight: w}.Layout(gtx, th)
}

func (th *Theme) mono(gtx layout.Context, s string, size unit.Sp, c color.NRGBA) layout.Dimensions {
	return Label{Text: s, Size: size, Color: c, Mono: true}.Layout(gtx, th)
}

// exact lays out w with exact dimensions.
func exact(gtx layout.Context, wpx, hpx int, w layout.Widget) layout.Dimensions {
	gtx.Constraints = layout.Exact(image.Pt(wpx, hpx))
	w(gtx)
	return layout.Dimensions{Size: image.Pt(wpx, hpx)}
}

// column is a vertical Flex that takes its natural height even inside a
// fixed-height parent, so the parent can center it.
func column(gtx layout.Context, children ...layout.FlexChild) layout.Dimensions {
	gtx.Constraints.Min.Y = 0
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func hspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Width: dp}.Layout)
}

func vspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}

// hline draws a one-pixel horizontal rule across the width.
func hline(gtx layout.Context, c color.NRGBA) layout.Dimensions {
	h := max(gtx.Dp(1), 1)
	fill(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, h), c)
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}
