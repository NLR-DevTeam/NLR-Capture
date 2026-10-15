package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// All icons are line icons with the same stroke; see Icon.
var (
	icCheck     = newIcon("M20 6 9 17l-5-5")
	icClose     = newIcon("M18 6 6 18", "m6 6 12 12")
	icClear     = icClose
	icAdd       = newIcon("M5 12h14", "M12 5v14")
	icFile      = newIcon("M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z", "M14 2v4a2 2 0 0 0 2 2h4")
	icDoc       = newIcon("M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z", "M14 2v4a2 2 0 0 0 2 2h4", "M16 13H8", "M16 17H8", "M10 9H8")
	icLink      = newIcon("M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71", "M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71")
	icRefresh   = newIcon("M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8", "M21 3v5h-5", "M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16", "M8 16H3v5")
	icUp        = newIcon("m5 12 7-7 7 7", "M12 19V5")
	icDown      = newIcon("M12 5v14", "m19 12-7 7-7-7")
	icImport    = newIcon("M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4", "M17 8l-5-5-5 5", "M12 3v12")
	icExport    = newIcon("M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4", "M7 10l5 5 5-5", "M12 15V3")
	icDelete    = newIcon("M3 6h18", "M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6", "M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2", "M10 11v6", "M14 11v6")
	icSearch    = newIcon("circle 11 11 8", "m21 21-4.3-4.3")
	icSettings  = newIcon("M20 7h-9", "M14 17H5", "circle 17 17 3", "circle 7 7 3")
	icEdit      = newIcon("M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z", "m15 5 4 4")
	icCopy      = newIcon("rect 8 8 14 14 2", "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2")
	icPaste     = newIcon("rect 8 2 8 4 1", "M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2")
	icMore      = newIcon("M5 12h.01", "M12 12h.01", "M19 12h.01")
	icLock      = newIcon("rect 3 11 18 11 2", "M7 11V7a5 5 0 0 1 10 0v4")
	icLockOpen  = newIcon("rect 3 11 18 11 2", "M7 11V7a5 5 0 0 1 9.9-1")
	icChevron   = newIcon("m9 18 6-6-6-6")
	icExpand    = newIcon("m6 9 6 6 6-6")
	icDropDown  = icExpand
	icInfo      = newIcon("circle 12 12 10", "M12 16v-4", "M12 8h.01")
	icError     = newIcon("circle 12 12 10", "M12 8v4", "M12 16h.01")
	icWarn      = newIcon("m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3", "M12 9v4", "M12 17h.01")
	icDone      = newIcon("circle 12 12 10", "m9 12 2 2 4-4")
	icSave      = newIcon("M15.2 3a2 2 0 0 1 1.4.6l3.8 3.8a2 2 0 0 1 .6 1.4V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z", "M17 21v-7a1 1 0 0 0-1-1H8a1 1 0 0 0-1 1v7", "M7 3v4a1 1 0 0 0 1 1h7")
	icPlay      = newIcon("M6 3 20 12 6 21Z")
	icStop      = newIcon("rect 6 6 12 12 2")
	icOpen      = newIcon("M15 3h6v6", "M10 14 21 3", "M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6")
	icReplay    = newIcon("M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8", "M3 3v5h5")
	icSend      = newIcon("M22 2 15 22 11 13 2 9Z", "M22 2 11 13")
	icPower     = newIcon("M12 2v10", "M18.4 6.6a9 9 0 1 1-12.77.04")
	icSelectAll = newIcon("M3 7V5a2 2 0 0 1 2-2h2", "M17 3h2a2 2 0 0 1 2 2v2", "M21 17v2a2 2 0 0 1-2 2h-2", "M7 21H5a2 2 0 0 1-2-2v-2")
	icCut       = newIcon("circle 6 6 3", "circle 6 18 3", "M20 4 8.12 15.88", "M14.47 14.48 20 20", "M8.12 8.12 12 12")
	icRecord    = newIcon("circle 12 12 9", "fill "+circlePath(12, 12, 5))
	icBlock     = newIcon("circle 12 12 10", "m4.9 4.9 14.2 14.2")
	icFilter    = newIcon("M3 6h18", "M7 12h10", "M10 18h4")
	icRules     = newIcon("m17 2 4 4-4 4", "M3 11v-1a4 4 0 0 1 4-4h14", "m7 22-4-4 4-4", "M21 13v1a4 4 0 0 1-4 4H3")
	icPause     = newIcon("circle 12 12 10", "M10 15V9", "M14 15V9")
	icCode      = newIcon("m16 18 6-6-6-6", "m8 6-6 6 6 6")
	icImage     = newIcon("rect 3 3 18 18 2", "circle 9 9 2", "m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21")
	icMedia     = newIcon("rect 2 6 14 12 2", "m16 13 5.22 3.48a.5.5 0 0 0 .78-.42V7.87a.5.5 0 0 0-.75-.43L16 10.5")
	icFont      = newIcon("M4 7V4h16v3", "M9 20h6", "M12 4v16")
	icWasm      = newIcon("rect 4 4 16 16 2", "rect 9 9 6 6 1", "M15 2v2", "M15 20v2", "M2 15h2", "M2 9h2", "M20 15h2", "M20 9h2", "M9 2v2", "M9 20v2")
	icGlobe     = newIcon("circle 12 12 10", "M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20", "M2 12h20")
	icWS        = newIcon("M13 2 3 14h9l-1 8 10-12h-9l1-8z")
	icTunnel    = newIcon("M8 3 4 7l4 4", "M4 7h16", "m16 21 4-4-4-4", "M20 17H4")
	icFetch     = newIcon("m21 16-4 4-4-4", "M17 20V4", "m3 8 4-4 4 4", "M7 4v16")
	icStyle     = newIcon("M4 9h16", "M4 15h16", "M10 3 8 21", "M16 3l-2 18")
	icNIC       = newIcon("rect 16 16 6 6 1", "rect 2 16 6 6 1", "rect 9 2 6 6 1", "M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3", "M12 12V8")
	icProxy     = newIcon("rect 2 2 20 8 2", "rect 2 14 20 8 2", "M6 6h.01", "M6 18h.01")
	icWrap      = newIcon("M3 6h18", "M3 12h15a3 3 0 1 1 0 6h-4", "m16 16-2 2 2 2", "M3 18h7")
	icFormat    = newIcon("M15 12H3", "M17 18H3", "M21 6H3")
	icTimer     = newIcon("M10 2h4", "M12 14l3-3", "circle 12 14 8")
	icHelp      = newIcon("circle 12 12 10", "M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3", "M12 17h.01")
	icManifest  = newIcon("M3 6h.01", "M3 12h.01", "M3 18h.01", "M8 6h13", "M8 12h13", "M8 18h13")
	icCert      = newIcon("M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z", "m9 12 2 2 4-4")
	icMark      = newIcon("m19 21-7-4-7 4V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v16z")
	icNext      = newIcon("M5 4l10 8-10 8Z", "M19 5v14")
	icBug       = icInfo
)

// drawLogo paints the mark inside a square of the given size: an "N" and
// two arrows for the traffic going each way.
func drawLogo(gtx layout.Context, size int, fg, accent color.NRGBA) layout.Dimensions {
	s := float32(size)
	p := func(x, y float32) f32.Point { return f32.Pt(x*s, y*s) }
	stroke := func(c color.NRGBA, w float32, pts ...f32.Point) {
		var path clip.Path
		path.Begin(gtx.Ops)
		path.MoveTo(pts[0])
		for _, pt := range pts[1:] {
			path.LineTo(pt)
		}
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: path.End(), Width: w * s}.Op())
	}
	// N
	stroke(fg, 0.12, p(0.12, 0.78), p(0.12, 0.22), p(0.46, 0.78), p(0.46, 0.22))
	// → and ←
	stroke(accent, 0.09, p(0.60, 0.36), p(0.90, 0.36))
	stroke(accent, 0.09, p(0.79, 0.25), p(0.90, 0.36), p(0.79, 0.47))
	stroke(accent, 0.09, p(0.90, 0.66), p(0.60, 0.66))
	stroke(accent, 0.09, p(0.71, 0.55), p(0.60, 0.66), p(0.71, 0.77))
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// Window control glyphs, drawn with hairlines like the native ones.
func drawWinGlyph(ops *op.Ops, kind int, center image.Point, s float32, c color.NRGBA, px float32) {
	cx, cy := float32(center.X), float32(center.Y)
	h := s / 2
	switch kind {
	case 0: // minimize
		line(ops, f32.Pt(cx-h, cy), f32.Pt(cx+h, cy), px, c)
	case 1: // maximize
		var p clip.Path
		p.Begin(ops)
		p.MoveTo(f32.Pt(cx-h, cy-h))
		p.LineTo(f32.Pt(cx+h, cy-h))
		p.LineTo(f32.Pt(cx+h, cy+h))
		p.LineTo(f32.Pt(cx-h, cy+h))
		p.Close()
		paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: px}.Op())
	case 2: // restore
		o := s * 0.22
		var p clip.Path
		p.Begin(ops)
		p.MoveTo(f32.Pt(cx-h, cy-h+o))
		p.LineTo(f32.Pt(cx+h-o, cy-h+o))
		p.LineTo(f32.Pt(cx+h-o, cy+h))
		p.LineTo(f32.Pt(cx-h, cy+h))
		p.Close()
		paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: px}.Op())
		var q clip.Path
		q.Begin(ops)
		q.MoveTo(f32.Pt(cx-h+o, cy-h+o))
		q.LineTo(f32.Pt(cx-h+o, cy-h))
		q.LineTo(f32.Pt(cx+h, cy-h))
		q.LineTo(f32.Pt(cx+h, cy+h-o))
		q.LineTo(f32.Pt(cx+h-o, cy+h-o))
		paint.FillShape(ops, c, clip.Stroke{Path: q.End(), Width: px}.Op())
	case 3: // close
		line(ops, f32.Pt(cx-h, cy-h), f32.Pt(cx+h, cy+h), px, c)
		line(ops, f32.Pt(cx-h, cy+h), f32.Pt(cx+h, cy-h), px, c)
	}
}

// dot draws a filled circle of diameter d at the current offset.
func dot(gtx layout.Context, d int, c color.NRGBA) layout.Dimensions {
	paint.FillShape(gtx.Ops, c, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
	return layout.Dimensions{Size: image.Pt(d, d)}
}
