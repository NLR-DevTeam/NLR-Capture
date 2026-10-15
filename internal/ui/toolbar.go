package ui

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/config"
	"nlrcapture/internal/proxy"
)

// typeChips are the resource type filters, as in DevTools.
var typeChips = [][2]string{
	{"", "全部"},
	{capture.TypeAPI, "API 请求"},
	{capture.TypeDocument, "文档"},
	{capture.TypeStylesheet, "CSS"},
	{capture.TypeScript, "JS"},
	{capture.TypeFont, "字体"},
	{capture.TypeImage, "图片"},
	{capture.TypeMedia, "媒体"},
	{capture.TypeManifest, "清单"},
	{capture.TypeWebSocket, "WS"},
	{capture.TypeWasm, "Wasm"},
	{capture.TypeOther, "其他"},
}

const toolbarH = unit.Dp(34)

// vsep draws a short vertical separator in a toolbar.
func (a *App) vsep(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(18)
	w := gtx.Dp(13)
	x := w / 2
	fill(gtx.Ops, image.Rect(x, 0, x+max(gtx.Dp(1), 1), h), a.th.Border)
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// smallCheck is a compact labeled check box for toolbars.
func (a *App) smallCheck(gtx layout.Context, b *widget.Bool, label string) layout.Dimensions {
	th := a.th
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				s := gtx.Dp(14)
				r := image.Rectangle{Max: image.Pt(s, s)}
				if b.Value {
					fillRR(gtx.Ops, r, gtx.Dp(3), th.Accent)
					drawIcon(gtx, icCheck, 14, th.AccentFg)
				} else {
					fillRR(gtx.Ops, r, gtx.Dp(3), th.Bg0)
					c := th.BorderHi
					if b.Hovered() {
						c = th.Text3
					}
					strokeRR(gtx.Ops, r, gtx.Dp(3), float32(gtx.Dp(1)), c)
				}
				return layout.Dimensions{Size: r.Max}
			}),
			hspace(6),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, label, 12, th.Text)
			}),
		)
	})
}

func (a *App) layoutToolbar(gtx layout.Context) layout.Dimensions {
	th := a.th
	h := gtx.Dp(toolbarH)
	w := gtx.Constraints.Max.X
	eng := a.svc.engine
	if a.recBtn.Clicked(gtx) {
		a.toggleRecording()
	}
	if a.clearBtn.Clicked(gtx) {
		a.clearAll()
	}
	if a.filterBtn.Clicked(gtx) {
		a.updateSettings(func(s *config.Settings) { s.ShowFilter = !s.ShowFilter })
	}
	if a.findBtn.Clicked(gtx) {
		a.search.toggle(!a.search.open)
	}
	if a.importBtn.Clicked(gtx) {
		a.pickImport()
	}
	if a.exportBtn.Clicked(gtx) {
		a.pickExport(false)
	}
	if a.throttleBtn.Clicked(gtx) {
		var items []MenuItem
		for i, p := range proxy.ThrottlePresets {
			i := i
			items = append(items, MenuItem{Label: p.Name, Checked: a.throttle == i, Do: func() {
				a.throttle = i
				a.svc.applyConfig()
			}})
		}
		a.Menu(items...)
	}
	if a.resumeAllBtn.Clicked(gtx) {
		eng.ContinueAll()
	}
	if a.scrollChk.Update(gtx) {
		a.updateSettings(func(s *config.Settings) { s.AutoScroll = a.scrollChk.Value })
	}

	gtx.Constraints = layout.Exact(image.Pt(w, h))
	btn := func(clk *widget.Clickable, ic *Icon, fg color.NRGBA, on bool, title string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.iconButton(gtx, clk, ic, 28, 17, fg, on, title)
		})
	}
	recFg, recTitle := th.Danger, "停止录制  Ctrl+E"
	if !eng.Recording() {
		recFg, recTitle = th.Text3, "开始录制  Ctrl+E"
	}
	paused := len(eng.Paused())
	layout.Inset{Left: 6, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			btn(&a.recBtn, icRecord, recFg, false, recTitle),
			btn(&a.clearBtn, icBlock, th.Text2, false, "清除  Ctrl+L"),
			layout.Rigid(a.vsep),
			btn(&a.filterBtn, icFilter, th.Text2, a.set.ShowFilter, "筛选栏"),
			btn(&a.findBtn, icSearch, th.Text2, a.search.open, "全文搜索  Ctrl+F"),
			layout.Rigid(a.vsep),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.dropButton(gtx, &a.throttleBtn, proxy.ThrottlePresets[a.throttle].Name, a.throttle != 0)
			}),
			layout.Rigid(a.vsep),
			btn(&a.importBtn, icImport, th.Text2, false, "导入 HAR  Ctrl+O"),
			btn(&a.exportBtn, icExport, th.Text2, false, "导出 HAR  Ctrl+S"),
			layout.Rigid(a.vsep),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return a.smallCheck(gtx, &a.scrollChk, "自动滚动") }),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 1)}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if paused == 0 {
					return layout.Dimensions{}
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icPause, 16, th.Warn) }),
					hspace(6),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.txt(gtx, fmt.Sprintf("已暂停 %d 个请求", paused), 12, th.Warn)
					}),
					hspace(8),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return a.smallButton(gtx, &a.resumeAllBtn, "全部继续", icPlay)
					}),
				)
			}),
		)
	})
	fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), w, h), th.Border)
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// dropButton is a toolbar button that opens a menu.
func (a *App) dropButton(gtx layout.Context, clk *widget.Clickable, label string, active bool) layout.Dimensions {
	th := a.th
	h := gtx.Dp(26)
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 8, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					c := th.Text
					if active {
						c = th.Warn
					}
					return th.txt(gtx, label, 12, c)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icDropDown, 18, th.Text2) }),
			)
		})
		call := m.Stop()
		r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
		if clk.Hovered() {
			fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg3)
		}
		st := op.Offset(image.Pt(0, (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		return layout.Dimensions{Size: r.Max}
	})
}

// smallButton is a compact text button.
func (a *App) smallButton(gtx layout.Context, clk *widget.Clickable, label string, ic *Icon) layout.Dimensions {
	th := a.th
	h := gtx.Dp(24)
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 8, Right: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if ic == nil {
						return layout.Dimensions{}
					}
					d := drawIcon(gtx, ic, 14, th.Text2)
					d.Size.X += gtx.Dp(4)
					return d
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, label, 12, th.Text) }),
			)
		})
		call := m.Stop()
		r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
		bg := th.Bg2
		if clk.Hovered() {
			bg = th.Bg3
		}
		fillRR(gtx.Ops, r, gtx.Dp(5), bg)
		strokeRR(gtx.Ops, r, gtx.Dp(5), float32(gtx.Dp(1)), th.Border)
		st := op.Offset(image.Pt(0, (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		return layout.Dimensions{Size: r.Max}
	})
}

// applyFilter rebuilds the filter from the filter bar.
func (a *App) applyFilter() {
	f := capture.Filter{Invert: a.invertChk.Value, HideTunnels: a.tunnelChk.Value, OnlyErrors: a.errorsChk.Value}
	f.SetText(a.filterText)
	if len(a.types) > 0 {
		f.Types = map[string]bool{}
		for t := range a.types {
			f.Types[t] = true
		}
	}
	a.filter = f
	a.list.invalidate()
}

func (a *App) layoutFilterBar(gtx layout.Context) layout.Dimensions {
	th := a.th
	if _, changed := a.filterField.Events(gtx); changed {
		a.filterText = a.filterField.Text()
		a.applyFilter()
	}
	if a.clearFilterBtn.Clicked(gtx) {
		a.filterField.SetText("")
		a.filterText = ""
		a.applyFilter()
	}
	if a.invertChk.Update(gtx) || a.errorsChk.Update(gtx) {
		a.applyFilter()
	}
	if a.tunnelChk.Update(gtx) {
		a.updateSettings(func(s *config.Settings) { s.HideTunnels = a.tunnelChk.Value })
		a.applyFilter()
	}
	if len(a.typeClk) != len(typeChips) {
		a.typeClk = make([]widget.Clickable, len(typeChips))
	}
	for i := range typeChips {
		for {
			ev, ok := a.typeClk[i].Update(gtx)
			if !ok {
				break
			}
			id := typeChips[i][0]
			switch {
			case id == "":
				a.types = map[string]bool{}
			case ev.Modifiers.Contain(key.ModCtrl): // Ctrl adds to the selection
				if a.types[id] {
					delete(a.types, id)
				} else {
					a.types[id] = true
				}
			default:
				a.types = map[string]bool{id: true}
			}
			a.applyFilter()
		}
	}

	w := gtx.Constraints.Max.X
	rowH := gtx.Dp(34)
	fieldW := min(gtx.Dp(300), w/3)
	// Measure the chips to decide whether they fit on the first row.
	chips := func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, len(typeChips))
		for i := range typeChips {
			i := i
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.typeChip(gtx, i)
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	}
	checks := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return a.smallCheck(gtx, &a.invertChk, "反转") }),
			hspace(12),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return a.smallCheck(gtx, &a.tunnelChk, "隐藏隧道") }),
			hspace(12),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return a.smallCheck(gtx, &a.errorsChk, "仅错误") }),
		)
	}
	mg := gtx
	mg.Constraints = layout.Constraints{Max: image.Pt(w*2, rowH)}
	rec := op.Record(gtx.Ops)
	cw := chips(mg).Size.X
	kw := checks(mg).Size.X
	rec.Stop()
	oneRow := fieldW+kw+cw+gtx.Dp(60) <= w

	row := func(gtx layout.Context, children ...layout.FlexChild) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(w, rowH))
		return layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
		})
	}
	field := layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(fieldW, gtx.Dp(26)))
		a.filterField.Height = 26
		return a.filterField.box(gtx, th, func(gtx layout.Context) layout.Dimensions {
			if a.filterField.Text() == "" {
				return drawIcon(gtx, icFilter, 15, th.Text3)
			}
			return th.iconButton(gtx, &a.clearFilterBtn, icClose, 20, 13, th.Text2, false, "清除过滤")
		})
	})
	var d layout.Dimensions
	if oneRow {
		d = row(gtx, field, hspace(12), layout.Rigid(checks), layout.Rigid(a.vsep), layout.Rigid(chips))
	} else {
		d = layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return row(gtx, field, hspace(12), layout.Rigid(checks))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return row(gtx, layout.Rigid(chips))
			}),
		)
	}
	fill(gtx.Ops, image.Rect(0, d.Size.Y-max(gtx.Dp(1), 1), w, d.Size.Y), th.Border)
	return layout.Dimensions{Size: image.Pt(w, d.Size.Y)}
}

func (a *App) typeChip(gtx layout.Context, i int) layout.Dimensions {
	th := a.th
	id, label := typeChips[i][0], typeChips[i][1]
	sel := a.types[id] || (id == "" && len(a.types) == 0)
	clk := &a.typeClk[i]
	h := gtx.Dp(22)
	return layout.Inset{Right: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			m := op.Record(gtx.Ops)
			d := layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				c := th.Text2
				w := font.Normal
				if sel {
					c, w = th.Text, font.Medium
				}
				return th.txtW(gtx, label, 12, c, w)
			})
			call := m.Stop()
			r := image.Rectangle{Max: image.Pt(d.Size.X, h)}
			switch {
			case sel:
				fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg4)
			case clk.Hovered():
				fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg3)
			}
			st := op.Offset(image.Pt(0, (h-d.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			st.Pop()
			return layout.Dimensions{Size: r.Max}
		})
	})
}

func (a *App) layoutStatusBar(gtx layout.Context) layout.Dimensions {
	th := a.th
	h := gtx.Dp(26)
	w := gtx.Constraints.Max.X
	fill(gtx.Ops, image.Rect(0, 0, w, max(gtx.Dp(1), 1)), th.Border)
	st := a.list.stats()
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	sep := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			fill(gtx.Ops, image.Rect(0, 0, max(gtx.Dp(1), 1), gtx.Dp(12)), th.Border)
			return layout.Dimensions{Size: image.Pt(max(gtx.Dp(1), 1), gtx.Dp(12))}
		})
	}
	t := func(s string, c color.NRGBA) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, s, 12, c) })
	}
	reqs := fmt.Sprintf("%d 个请求", st.total)
	if st.shown != st.total {
		reqs = fmt.Sprintf("%d / %d 个请求", st.shown, st.total)
	}
	xfer := "已传输 " + fmtBytes(st.shownBytes)
	if st.shown != st.total {
		xfer += " / " + fmtBytes(st.totalBytes)
	}
	layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			t(reqs, th.Text2), layout.Rigid(sep), t(xfer, th.Text2), layout.Rigid(sep), t("资源 "+fmtBytes(st.shownRes), th.Text2),
		}
		if st.span > 0 {
			children = append(children, layout.Rigid(sep), t("耗时 "+fmtDuration(st.span), th.Text2))
		}
		if st.selected > 1 {
			children = append(children, layout.Rigid(sep), t(fmt.Sprintf("已选 %d 项", st.selected), th.Text2))
		}
		children = append(children,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 1)}
			}),
			t(a.svc.addr(), th.Text),
		)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// fmtBytes formats a byte count like DevTools: B, kB, MB with decimal
// units.
func fmtBytes(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f kB", float64(n)/1000)
	case n < 1000*1000*1000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.2f GB", float64(n)/1e9)
}

// fmtDuration formats a duration like DevTools: ms below a second.
func fmtDuration(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < time.Millisecond:
		return fmt.Sprintf("%.2f ms", float64(d.Microseconds())/1000)
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	return fmt.Sprintf("%.1f min", d.Minutes())
}
