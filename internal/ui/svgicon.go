package ui

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// Icon is a line icon on a 24×24 grid, drawn with round 2-unit strokes in
// the style of Lucide (ISC license), so every icon in the program shares
// one look. Paths use SVG path syntax; "circle cx cy r" and
// "rect x y w h r" are shorthands. Fill paths are painted solid.
type Icon struct {
	stroke []seg
	fill   []seg
}

type segKind uint8

const (
	segMove segKind = iota
	segLine
	segCube
	segClose
)

type seg struct {
	kind segKind
	pts  [3]f32.Point
}

func newIcon(paths ...string) *Icon {
	ic := &Icon{}
	for _, p := range paths {
		fill := false
		if strings.HasPrefix(p, "fill ") {
			fill, p = true, p[5:]
		}
		segs := parseShape(p)
		if fill {
			ic.fill = append(ic.fill, segs...)
		} else {
			ic.stroke = append(ic.stroke, segs...)
		}
	}
	return ic
}

func circlePath(cx, cy, r float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return "M" + f(cx+r) + " " + f(cy) + "A" + f(r) + " " + f(r) + " 0 1 1 " + f(cx-r) + " " + f(cy) +
		"A" + f(r) + " " + f(r) + " 0 1 1 " + f(cx+r) + " " + f(cy) + "Z"
}

func rectPath(x, y, w, h, r float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	if r <= 0 {
		return "M" + f(x) + " " + f(y) + "h" + f(w) + "v" + f(h) + "h" + f(-w) + "Z"
	}
	a := "a" + f(r) + " " + f(r) + " 0 0 1 "
	return "M" + f(x+r) + " " + f(y) + "h" + f(w-2*r) + a + f(r) + " " + f(r) +
		"v" + f(h-2*r) + a + f(-r) + " " + f(r) + "h" + f(-(w - 2*r)) + a + f(-r) + " " + f(-r) +
		"v" + f(-(h - 2*r)) + a + f(r) + " " + f(-r) + "Z"
}

func parseShape(s string) []seg {
	fs := strings.Fields(s)
	nums := func(from int) []float64 {
		var out []float64
		for _, x := range fs[from:] {
			v, _ := strconv.ParseFloat(x, 64)
			out = append(out, v)
		}
		return out
	}
	switch fs[0] {
	case "circle":
		n := nums(1)
		return parsePath(circlePath(n[0], n[1], n[2]))
	case "rect":
		n := nums(1)
		r := 0.0
		if len(n) > 4 {
			r = n[4]
		}
		return parsePath(rectPath(n[0], n[1], n[2], n[3], r))
	}
	return parsePath(s)
}

// tokens splits path data into commands and numbers.
func tokens(d string) []string {
	var out []string
	i := 0
	for i < len(d) {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\n' || c == '\t':
			i++
		case strings.IndexByte("MmLlHhVvCcSsQqAaZz", c) >= 0:
			out = append(out, string(c))
			i++
		default:
			j := i
			if d[j] == '-' || d[j] == '+' {
				j++
			}
			dot := false
			for j < len(d) {
				ch := d[j]
				if ch >= '0' && ch <= '9' {
					j++
				} else if ch == '.' && !dot {
					dot = true
					j++
				} else if (ch == 'e' || ch == 'E') && j+1 < len(d) {
					j += 2
				} else {
					break
				}
			}
			if j == i {
				j++
			}
			out = append(out, d[i:j])
			i = j
		}
	}
	return out
}

func parsePath(d string) []seg {
	var out []seg
	toks := tokens(d)
	var cur, start, lastCtrl f32.Point
	cmd := byte('M')
	i := 0
	num := func() float32 {
		if i >= len(toks) {
			return 0
		}
		v, _ := strconv.ParseFloat(toks[i], 32)
		i++
		return float32(v)
	}
	isNum := func() bool {
		return i < len(toks) && strings.IndexByte("MmLlHhVvCcSsQqAaZz", toks[i][0]) < 0
	}
	pt := func(rel bool) f32.Point {
		x, y := num(), num()
		if rel {
			return f32.Pt(cur.X+x, cur.Y+y)
		}
		return f32.Pt(x, y)
	}
	for i < len(toks) {
		if !isNum() {
			cmd = toks[i][0]
			i++
		}
		rel := cmd >= 'a'
		prevCmd := cmd
		switch cmd | 0x20 {
		case 'm':
			cur = pt(rel)
			start = cur
			out = append(out, seg{kind: segMove, pts: [3]f32.Point{cur}})
			// Further pairs are line-tos.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			cur = pt(rel)
			out = append(out, seg{kind: segLine, pts: [3]f32.Point{cur}})
		case 'h':
			x := num()
			if rel {
				x += cur.X
			}
			cur = f32.Pt(x, cur.Y)
			out = append(out, seg{kind: segLine, pts: [3]f32.Point{cur}})
		case 'v':
			y := num()
			if rel {
				y += cur.Y
			}
			cur = f32.Pt(cur.X, y)
			out = append(out, seg{kind: segLine, pts: [3]f32.Point{cur}})
		case 'c':
			c1, c2, e := pt(rel), pt(rel), pt(rel)
			out = append(out, seg{kind: segCube, pts: [3]f32.Point{c1, c2, e}})
			lastCtrl, cur = c2, e
		case 's':
			c1 := cur.Add(cur.Sub(lastCtrl))
			c2, e := pt(rel), pt(rel)
			out = append(out, seg{kind: segCube, pts: [3]f32.Point{c1, c2, e}})
			lastCtrl, cur = c2, e
		case 'q':
			q, e := pt(rel), pt(rel)
			c1 := cur.Add(q.Sub(cur).Mul(2.0 / 3))
			c2 := e.Add(q.Sub(e).Mul(2.0 / 3))
			out = append(out, seg{kind: segCube, pts: [3]f32.Point{c1, c2, e}})
			cur = e
		case 'a':
			rx, ry, rot := num(), num(), num()
			large, sweep := num() != 0, num() != 0
			e := pt(rel)
			out = append(out, arcSegs(cur, e, float64(rx), float64(ry), float64(rot), large, sweep)...)
			cur = e
		case 'z':
			out = append(out, seg{kind: segClose})
			cur = start
		default:
			i++
		}
		if prevCmd|0x20 != 'c' && prevCmd|0x20 != 's' {
			lastCtrl = cur
		}
	}
	return out
}

// arcSegs converts an SVG elliptical arc to cubic Béziers.
func arcSegs(p0, p1 f32.Point, rx, ry, rot float64, large, sweep bool) []seg {
	x1, y1, x2, y2 := float64(p0.X), float64(p0.Y), float64(p1.X), float64(p1.Y)
	if rx == 0 || ry == 0 || (x1 == x2 && y1 == y2) {
		return []seg{{kind: segLine, pts: [3]f32.Point{p1}}}
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := rot * math.Pi / 180
	cos, sin := math.Cos(phi), math.Sin(phi)
	dx2, dy2 := (x1-x2)/2, (y1-y2)/2
	x1p := cos*dx2 + sin*dy2
	y1p := -sin*dx2 + cos*dy2
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		s := math.Sqrt(l)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	coef := math.Sqrt(math.Max(0, num/den))
	if large == sweep {
		coef = -coef
	}
	cxp := coef * rx * y1p / ry
	cyp := -coef * ry * x1p / rx
	cx := cos*cxp - sin*cyp + (x1+x2)/2
	cy := sin*cxp + cos*cyp + (y1+y2)/2
	angle := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := angle(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := angle((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dt) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	d := dt / float64(n)
	k := 4.0 / 3 * math.Tan(d/4)
	mp := func(x, y float64) f32.Point {
		return f32.Pt(float32(cx+rx*x*cos-ry*y*sin), float32(cy+rx*x*sin+ry*y*cos))
	}
	var out []seg
	a := t1
	for range n {
		b := a + d
		ca, sa, cb, sb := math.Cos(a), math.Sin(a), math.Cos(b), math.Sin(b)
		out = append(out, seg{kind: segCube, pts: [3]f32.Point{
			mp(ca-k*sa, sa+k*ca), mp(cb+k*sb, sb-k*cb), mp(cb, sb),
		}})
		a = b
	}
	out[len(out)-1].pts[2] = p1
	return out
}

func buildPath(p *clip.Path, segs []seg, s float32, off f32.Point) {
	m := func(q f32.Point) f32.Point { return q.Mul(s).Add(off) }
	var cur, start f32.Point
	for _, g := range segs {
		switch g.kind {
		case segMove:
			cur, start = g.pts[0], g.pts[0]
			p.MoveTo(m(cur))
		case segLine:
			cur = g.pts[0]
			p.LineTo(m(cur))
		case segCube:
			c1, c2, e := fixCube(cur, g.pts[0], g.pts[1], g.pts[2])
			p.CubeTo(m(c1), m(c2), m(e))
			cur = e
		case segClose:
			p.Close()
			cur = start
		}
	}
}

// fixCube moves control points that sit on their end points a little
// along the curve. SVG paths often write curves that way, but a control
// point on the end point leaves the stroker without a tangent there, and
// the round cap then comes out as a stray streak.
func fixCube(p0, c1, c2, e f32.Point) (f32.Point, f32.Point, f32.Point) {
	near := func(a, b f32.Point) bool {
		d := a.Sub(b)
		return d.X*d.X+d.Y*d.Y < 1e-6
	}
	lerp := func(a, b f32.Point, t float32) f32.Point { return a.Add(b.Sub(a).Mul(t)) }
	if near(c1, p0) {
		if near(c2, p0) {
			c1 = lerp(p0, e, 0.05)
		} else {
			c1 = lerp(p0, c2, 0.05)
		}
	}
	if near(c2, e) {
		if near(c1, e) {
			c2 = lerp(e, p0, 0.05)
		} else {
			c2 = lerp(e, c1, 0.05)
		}
	}
	return c1, c2, e
}

// Layout draws the icon filling the constraints' square.
func (ic *Icon) Layout(gtx layout.Context, c color.NRGBA) layout.Dimensions {
	sz := min(gtx.Constraints.Max.X, gtx.Constraints.Max.Y)
	s := float32(sz) / 24
	width := max(2*s, 1.25)
	if len(ic.stroke) > 0 {
		var p clip.Path
		p.Begin(gtx.Ops)
		buildPath(&p, ic.stroke, s, f32.Point{})
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
	}
	if len(ic.fill) > 0 {
		var p clip.Path
		p.Begin(gtx.Ops)
		buildPath(&p, ic.fill, s, f32.Point{})
		paint.FillShape(gtx.Ops, c, clip.Outline{Path: p.End()}.Op())
	}
	return layout.Dimensions{Size: image.Pt(sz, sz)}
}

// drawIcon draws ic at the given size.
func drawIcon(gtx layout.Context, ic *Icon, size unit.Dp, c color.NRGBA) layout.Dimensions {
	px := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(px, px))
	return ic.Layout(gtx, c)
}
