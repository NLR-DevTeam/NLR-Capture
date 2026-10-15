package ui

import (
	"image"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
)

// Dialog forms are built from titled groups: a heading above a panel that
// holds rows of a label column and a control.

const (
	// controlH is the height of single-line controls.
	controlH = unit.Dp(32)
	// formLabelW is the usual width of the label column.
	formLabelW = unit.Dp(64)
)

// group draws a heading, with extra at its right if set, above a panel
// holding body.
func (th *Theme) group(gtx layout.Context, title string, extra layout.Widget, body layout.Widget) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 2, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, title, 12, th.Text2, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if extra == nil {
							return layout.Dimensions{}
						}
						return extra(gtx)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.panel(gtx, 12, body)
		}),
	)
}

// panel draws body on a raised, bordered surface across the full width.
func (th *Theme) panel(gtx layout.Context, pad unit.Dp, body layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	rec := op.Record(gtx.Ops)
	d := layout.UniformInset(pad).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return body(gtx)
	})
	call := rec.Stop()
	r := image.Rectangle{Max: d.Size}
	fillRR(gtx.Ops, r, gtx.Dp(8), th.Bg2)
	strokeRR(gtx.Ops, r, gtx.Dp(8), float32(gtx.Dp(1)), th.Border)
	call.Add(gtx.Ops)
	return d
}

// formRow lays out a label beside a control. The label is centered on the
// first line of the control, so rows with taller controls still line up.
// gap is the space below the row.
func (th *Theme) formRow(gtx layout.Context, label string, labelW, gap unit.Dp, control layout.Widget) layout.Dimensions {
	return layout.Inset{Bottom: gap}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return exact(gtx, gtx.Dp(labelW), gtx.Dp(controlH), func(gtx layout.Context) layout.Dimensions {
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						c := th.Text2
						if !gtx.Enabled() {
							c = th.Text3
						}
						return th.txt(gtx, label, 13, c)
					})
				})
			}),
			layout.Flexed(1, control),
		)
	})
}

// Select is a drop-down box that shows the chosen option and lists the
// options in a menu when clicked.
type Select struct {
	Value   string
	changed bool
	clk     widget.Clickable
}

// Changed reports whether the user picked an option since the last call.
func (s *Select) Changed() bool {
	c := s.changed
	s.changed = false
	return c
}

// Layout draws the box across the available width. opts are value, label
// pairs.
func (s *Select) Layout(gtx layout.Context, a *App, opts [][2]string) layout.Dimensions {
	th := a.th
	if s.clk.Clicked(gtx) {
		items := make([]MenuItem, len(opts))
		for i, o := range opts {
			items[i] = MenuItem{Label: o[1], Checked: o[0] == s.Value, Do: func() {
				if s.Value != o[0] {
					s.Value, s.changed = o[0], true
				}
			}}
		}
		a.Menu(items...)
	}
	label := s.Value
	for _, o := range opts {
		if o[0] == s.Value {
			label = o[1]
		}
	}
	w, h := gtx.Constraints.Max.X, gtx.Dp(controlH)
	return s.clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rectangle{Max: image.Pt(w, h)}
		rad := gtx.Dp(6)
		bg, border, fg := th.Bg0, th.Border, th.Text
		switch {
		case !gtx.Enabled():
			bg, fg = th.Bg1, th.Text3
		case s.clk.Hovered():
			border = th.BorderHi
		}
		fillRR(gtx.Ops, r, rad, bg)
		strokeRR(gtx.Ops, r, rad, float32(gtx.Dp(1)), border)
		gtx.Constraints = layout.Exact(r.Max)
		layout.Inset{Left: 10, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return th.txt(gtx, label, 13, fg)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return drawIcon(gtx, icDropDown, 16, th.Text3)
				}),
			)
		})
		return layout.Dimensions{Size: r.Max}
	})
}

// fixedW lays w out at a fixed width, or narrower if space is short.
func fixedW(gtx layout.Context, width unit.Dp, w layout.Widget) layout.Dimensions {
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(width))
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return w(gtx)
}

// footerSplit lays out dialog buttons with left at the left edge and right
// at the right edge.
func footerSplit(gtx layout.Context, left []layout.Widget, right []layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return buttonRow(gtx, left...) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 1)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return buttonRow(gtx, right...) }),
	)
}
