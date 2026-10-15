package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
)

// searchPane is the full-text search drawer: it looks through URLs,
// headers, bodies and WebSocket messages of every captured request.
type searchPane struct {
	a     *App
	open  bool
	field Field
	caseB widget.Bool
	regex widget.Bool

	closeClk, runClk, clearClk widget.Clickable

	cancel   context.CancelFunc
	running  bool
	results  []capture.SearchResult
	hits     int
	err      string
	query    string
	changeAt time.Time
	pending  bool
	gen      int

	rows      []searchRow
	collapsed map[int64]bool
	list      widget.List
	tag       struct{}
	hover     int
	focus     bool
}

type searchRow struct {
	res int // index into results
	hit int // -1 for the group header
}

func newSearchPane(a *App) *searchPane {
	p := &searchPane{a: a, collapsed: map[int64]bool{}, hover: -1}
	p.field.Hint = "搜索"
	p.list.Axis = layout.Vertical
	return p
}

func (p *searchPane) toggle(on bool) {
	p.open = on
	if on {
		p.focus = true
	}
}

func (p *searchPane) clearResults() {
	if p.cancel != nil {
		p.cancel()
	}
	p.results, p.rows, p.hits, p.err, p.running = nil, nil, 0, "", false
}

func (p *searchPane) start() {
	q := p.field.Text()
	p.clearResults()
	p.query = q
	if q == "" {
		return
	}
	opts := capture.SearchOptions{Query: q, CaseSensitive: p.caseB.Value, Regex: p.regex.Value}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.running = true
	p.gen++
	gen := p.gen
	sessions := p.a.store.All()
	a := p.a
	go func() {
		var batch []capture.SearchResult
		last := time.Now()
		flush := func(final bool, err error) {
			b := batch
			batch = nil
			a.Post(func() {
				if gen != p.gen {
					return
				}
				for _, r := range b {
					p.results = append(p.results, r)
					p.hits += len(r.Hits) + r.More
				}
				p.rebuild()
				if final {
					p.running = false
					if err != nil && err != context.Canceled {
						p.err = err.Error()
					}
				}
			})
		}
		err := capture.Search(ctx, sessions, opts, func(r capture.SearchResult) {
			batch = append(batch, r)
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				flush(false, nil)
			}
		})
		flush(true, err)
	}()
}

func (p *searchPane) rebuild() {
	p.rows = p.rows[:0]
	for i, r := range p.results {
		p.rows = append(p.rows, searchRow{res: i, hit: -1})
		if p.collapsed[r.Session.ID] {
			continue
		}
		for j := range r.Hits {
			p.rows = append(p.rows, searchRow{res: i, hit: j})
		}
	}
}

func (p *searchPane) events(gtx layout.Context) {
	if p.closeClk.Clicked(gtx) {
		p.open = false
	}
	if p.focus {
		p.focus = false
		p.field.Focus(gtx)
		n := p.field.Editor.Len()
		p.field.Editor.SetCaret(n, 0)
	}
	submitted, changed := p.field.Events(gtx)
	if p.caseB.Update(gtx) || p.regex.Update(gtx) {
		submitted = true
	}
	if p.runClk.Clicked(gtx) {
		submitted = true
	}
	if p.clearClk.Clicked(gtx) {
		p.field.SetText("")
		p.clearResults()
	}
	if changed {
		p.changeAt, p.pending = gtx.Now, true
	}
	if submitted {
		p.pending = false
		p.start()
	} else if p.pending {
		// Search as you type, after a pause.
		if at := p.changeAt.Add(400 * time.Millisecond); gtx.Now.Before(at) {
			gtx.Execute(op.InvalidateCmd{At: at})
		} else {
			p.pending = false
			p.start()
		}
	}
}

// toggleChip is a small text toggle such as "Aa" or ".*".
func (p *searchPane) toggleChip(gtx layout.Context, b *widget.Bool, label, title string) layout.Dimensions {
	th := p.a.th
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(24)
		r := image.Rectangle{Max: image.Pt(s, s)}
		switch {
		case b.Value:
			fillRR(gtx.Ops, r, gtx.Dp(5), alpha(th.Accent, 0x30))
		case b.Hovered():
			fillRR(gtx.Ops, r, gtx.Dp(5), th.Bg3)
		}
		if b.Hovered() {
			th.tip.hover(gtx, b, title)
		}
		cg := gtx
		cg.Constraints = layout.Exact(r.Max)
		layout.Center.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			c := th.Text2
			if b.Value {
				c = th.AccentText
			}
			return th.txtW(gtx, label, 12, c, font.Bold)
		})
		return layout.Dimensions{Size: r.Max}
	})
}

func (p *searchPane) Layout(gtx layout.Context) layout.Dimensions {
	a := p.a
	th := a.th
	p.events(gtx)
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg1)
	rh := gtx.Dp(22)

	for {
		e, ok := gtx.Event(pointer.Filter{Target: &p.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		y := int(pe.Position.Y)
		idx := p.list.Position.First + (y+p.list.Position.Offset)/rh
		if y < 0 || idx < 0 || idx >= len(p.rows) {
			idx = -1
		}
		switch pe.Kind {
		case pointer.Leave:
			p.hover = -1
		case pointer.Move:
			p.hover = idx
		case pointer.Press:
			if idx < 0 {
				continue
			}
			row := p.rows[idx]
			r := p.results[row.res]
			if row.hit < 0 {
				p.collapsed[r.Session.ID] = !p.collapsed[r.Session.ID]
				p.rebuild()
				continue
			}
			p.reveal(r.Session, r.Hits[row.hit])
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(30)
			w := gtx.Constraints.Max.X
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			layout.Inset{Left: 10, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, "搜索", 12, th.Text, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, &p.closeClk, icClose, 24, 15, th.Text2, false, "关闭")
					}),
				)
			})
			fill(gtx.Ops, image.Rect(0, h-1, w, h), th.Border)
			return layout.Dimensions{Size: image.Pt(w, h)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 8, Right: 8, Top: 8, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						p.field.Height = 28
						return p.field.box(gtx, th, func(gtx layout.Context) layout.Dimensions {
							if p.running {
								return th.spinner(gtx, 14)
							}
							return layout.Dimensions{}
						})
					}),
					hspace(4),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return p.toggleChip(gtx, &p.caseB, "Aa", "区分大小写")
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return p.toggleChip(gtx, &p.regex, ".*", "正则表达式")
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, &p.runClk, icRefresh, 24, 15, th.Text2, false, "重新搜索")
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.iconButton(gtx, &p.clearClk, icBlock, 24, 15, th.Text2, false, "清除")
					}),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
			event.Op(gtx.Ops, &p.tag)
			d := th.list(gtx, &p.list, len(p.rows), func(gtx layout.Context, i int) layout.Dimensions {
				return p.row(gtx, i, rh)
			})
			if len(p.rows) == 0 {
				msg := ""
				switch {
				case p.running:
					msg = "正在搜索…"
				case p.err != "":
					msg = p.err
				case p.query != "":
					msg = "无匹配"
				}
				layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return Label{Text: msg, Size: 12, Color: th.Text3, MaxLines: 3}.Layout(gtx, th)
				})
			}
			area.Pop()
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			w := gtx.Constraints.Max.X
			fill(gtx.Ops, image.Rect(0, 0, w, 1), th.Border)
			return layout.Inset{Left: 10, Top: 5, Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				text := ""
				switch {
				case p.running:
					text = fmt.Sprintf("正在搜索… %d", p.hits)
				case p.query != "":
					text = fmt.Sprintf("%d 个请求 · %d 个匹配", len(p.results), p.hits)
				}
				return th.txt(gtx, text, 11, th.Text2)
			})
		}),
	)
}

// reveal selects the session of a hit and opens the tab that holds it.
func (p *searchPane) reveal(s *capture.Session, h capture.SearchHit) {
	a := p.a
	a.list.selectID(s.ID)
	switch h.Where {
	case capture.WhereRespBody:
		a.details.tab = tabResponse
	case capture.WhereReqBody:
		a.details.tab = tabPayload
	case capture.WhereWS:
		a.details.tab = tabMessages
	default:
		a.details.tab = tabHeaders
	}
}

func (p *searchPane) row(gtx layout.Context, i, rh int) layout.Dimensions {
	th := p.a.th
	row := p.rows[i]
	r := p.results[row.res]
	w := gtx.Constraints.Max.X
	if p.hover == i {
		fill(gtx.Ops, image.Rect(0, 0, w, rh), th.Bg3)
	}
	if row.hit < 0 {
		ic := icExpand
		if p.collapsed[r.Session.ID] {
			ic = icChevron
		}
		st := op.Offset(image.Pt(gtx.Dp(4), (rh-gtx.Dp(14))/2)).Push(gtx.Ops)
		drawIcon(gtx, ic, 14, th.Text2)
		st.Pop()
		n := len(r.Hits) + r.More
		p.runs(gtx, gtx.Dp(22), rh, []run{
			{r.Session.Name(), th.Text, font.Medium, nilColor},
			{"  " + r.Session.Hostname(), th.Text3, font.Normal, nilColor},
			{fmt.Sprintf("  %d", n), th.Text3, font.Normal, nilColor},
		})
		return layout.Dimensions{Size: image.Pt(w, rh)}
	}
	h := r.Hits[row.hit]
	where := h.Where
	if h.Line > 0 {
		where = fmt.Sprintf("%s:%d", h.Where, h.Line)
	}
	text := h.Text
	a, b := min(max(h.Start, 0), len(text)), min(max(h.End, 0), len(text))
	p.runs(gtx, gtx.Dp(26), rh, []run{
		{where + "  ", th.Text3, font.Normal, nilColor},
		{text[:a], th.Text2, font.Normal, nilColor},
		{text[a:b], th.Text, font.Medium, alpha(th.Warn, 0x55)},
		{text[b:], th.Text2, font.Normal, nilColor},
	})
	return layout.Dimensions{Size: image.Pt(w, rh)}
}

var nilColor = color.NRGBA{}

type run struct {
	text string
	c    color.NRGBA
	w    font.Weight
	bg   color.NRGBA
}

// runs draws pieces of text one after another on a line, cut at the
// right edge.
func (p *searchPane) runs(gtx layout.Context, x, rh int, rs []run) {
	th := p.a.th
	w := gtx.Constraints.Max.X - gtx.Dp(6)
	for _, r := range rs {
		if r.text == "" || x >= w {
			continue
		}
		cg := gtx
		cg.Constraints = layout.Constraints{Max: image.Pt(w-x, rh)}
		rec := op.Record(gtx.Ops)
		d := Label{Text: r.text, Size: 12, Color: r.c, Weight: r.w}.Layout(cg, th)
		call := rec.Stop()
		if r.bg.A != 0 {
			fillRR(gtx.Ops, image.Rect(x-1, (rh-d.Size.Y)/2, x+d.Size.X+1, (rh+d.Size.Y)/2), gtx.Dp(2), r.bg)
		}
		st := op.Offset(image.Pt(x, (rh-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		x += d.Size.X
	}
}

var _ = key.NameReturn
