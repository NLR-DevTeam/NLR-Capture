package ui

import (
	"fmt"
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
)

// wsView lists the messages of a WebSocket session, like the DevTools
// Messages tab, with the selected message shown in full below.
type wsView struct {
	a      *App
	filter Field
	list   widget.List
	tag    struct{}
	hover  int
	sel    int
	view   []int
	id     int64
	n      int
	ftext  string
	detail *textView
	split  Splitter
	h      int // height of the message list, dp
}

func newWSView(a *App) *wsView {
	w := &wsView{a: a, hover: -1, sel: -1, detail: newTextView(a), h: 260}
	w.list.Axis = layout.Vertical
	w.list.ScrollToEnd = true
	w.filter.Hint = "过滤消息"
	w.filter.Height = 26
	return w
}

func opcodeName(op byte) string {
	switch op {
	case 1:
		return "文本"
	case 2:
		return "二进制"
	case 8:
		return "关闭"
	case 9:
		return "Ping"
	case 10:
		return "Pong"
	}
	return fmt.Sprintf("操作码 %d", op)
}

func wsPreview(m capture.WSMessage) string {
	if m.Opcode == 1 && utf8.Valid(m.Data) {
		s := m.Data
		if len(s) > 400 {
			s = s[:400]
		}
		return strings.ReplaceAll(string(s), "\n", " ")
	}
	if m.Opcode == 8 && len(m.Data) >= 2 {
		code := int(m.Data[0])<<8 | int(m.Data[1])
		return fmt.Sprintf("关闭 %d %s", code, string(m.Data[2:]))
	}
	if m.Opcode == 9 || m.Opcode == 10 {
		return opcodeName(m.Opcode)
	}
	return "二进制 · " + fmtBytes(int64(m.Length))
}

func (w *wsView) Layout(gtx layout.Context, s *capture.Session) layout.Dimensions {
	th := w.a.th
	if s.ID != w.id {
		w.id, w.sel, w.n = s.ID, -1, -1
		w.list.Position = layout.Position{}
	}
	if _, changed := w.filter.Events(gtx); changed {
		w.n = -1
	}
	if w.n != len(s.WS) || w.ftext != w.filter.Text() {
		w.n, w.ftext = len(s.WS), w.filter.Text()
		w.view = w.view[:0]
		q := strings.ToLower(w.ftext)
		for i, m := range s.WS {
			if q == "" || strings.Contains(strings.ToLower(string(m.Data)), q) {
				w.view = append(w.view, i)
			}
		}
	}
	rh := gtx.Dp(22)
	size := gtx.Constraints.Max
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &w.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		y := int(pe.Position.Y)
		idx := w.list.Position.First + (y+w.list.Position.Offset)/rh
		if y < 0 || idx < 0 || idx >= len(w.view) {
			idx = -1
		}
		switch pe.Kind {
		case pointer.Leave:
			w.hover = -1
		case pointer.Move:
			w.hover = idx
		case pointer.Press:
			if idx >= 0 {
				w.sel = w.view[idx]
				if pe.Buttons.Contain(pointer.ButtonSecondary) {
					m := s.WS[w.sel]
					w.a.Menu(MenuItem{Label: "复制消息", Icon: icCopy, Do: func() { w.a.copyText(string(m.Data), "消息") }})
				}
			}
		}
	}
	if len(s.WS) == 0 {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			msg := "无消息"
			if s.Status != 101 && !s.Running() {
				msg = "未升级为 WebSocket"
			}
			return th.txt(gtx, msg, 12, th.Text3)
		})
	}
	listH := gtx.Dp(260)
	if w.sel < 0 {
		listH = size.Y - gtx.Dp(36)
	} else {
		listH = min(max(gtx.Dp(unit.Dp(w.h)), gtx.Dp(80)), size.Y-gtx.Dp(120))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 8, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(320))
				return w.filter.box(gtx, th, nil)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(size.X, listH))
			area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
			event.Op(gtx.Ops, &w.tag)
			d := th.list(gtx, &w.list, len(w.view), func(gtx layout.Context, i int) layout.Dimensions {
				return w.row(gtx, s, i, rh)
			})
			area.Pop()
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if w.sel < 0 {
				return layout.Dimensions{}
			}
			return w.split.Layout(gtx, th, layout.Vertical, &w.h, 80, 4000, 1, w.a.mouse)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if w.sel < 0 || w.sel >= len(s.WS) {
				return layout.Dimensions{Size: gtx.Constraints.Max}
			}
			m := s.WS[w.sel]
			text := wsPreview(m)
			if m.Opcode == 1 {
				text = string(m.Data)
				if p, ok := prettyJSON(m.Data); ok {
					text = p
				}
			} else if m.Opcode == 2 {
				var b strings.Builder
				for i, c := range m.Data {
					if i > 0 && i%16 == 0 {
						b.WriteByte('\n')
					}
					fmt.Fprintf(&b, "%02x ", c)
				}
				text = b.String()
			}
			w.detail.set(text, fmt.Sprintf("%d-%d", s.ID, w.sel))
			return w.detail.Layout(gtx, true)
		}),
	)
}

func (w *wsView) row(gtx layout.Context, s *capture.Session, i, rh int) layout.Dimensions {
	th := w.a.th
	m := s.WS[w.view[i]]
	width := gtx.Constraints.Max.X
	r := image.Rect(0, 0, width, rh)
	switch {
	case w.view[i] == w.sel:
		fill(gtx.Ops, r, alpha(th.Accent, 0x30))
	case w.hover == i:
		fill(gtx.Ops, r, th.Bg3)
	case m.FromClient:
		fill(gtx.Ops, r, alpha(th.Green, 0x12))
	}
	ic, c := icDown, th.Danger
	if m.FromClient {
		ic, c = icUp, th.Green
	}
	if m.Opcode >= 8 {
		c = th.Text3
	}
	timeW := gtx.Dp(96)
	lenW := gtx.Dp(70)
	st := op.Offset(image.Pt(gtx.Dp(8), (rh-gtx.Dp(14))/2)).Push(gtx.Ops)
	drawIcon(gtx, ic, 14, c)
	st.Pop()
	x := gtx.Dp(28)
	cg := gtx
	cg.Constraints = layout.Constraints{Max: image.Pt(max(width-x-timeW-lenW, 0), rh)}
	rec := op.Record(gtx.Ops)
	fg := th.Text
	if m.Opcode != 1 {
		fg = th.Text2
	}
	d := Label{Text: wsPreview(m), Size: 12, Color: fg, Mono: m.Opcode == 1}.Layout(cg, th)
	call := rec.Stop()
	st = op.Offset(image.Pt(x, (rh-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
	put := func(text string, at, w int) {
		cg := gtx
		cg.Constraints = layout.Constraints{Max: image.Pt(w, rh)}
		rec := op.Record(gtx.Ops)
		d := th.txtW(cg, text, 11, th.Text2, font.Normal)
		call := rec.Stop()
		st := op.Offset(image.Pt(at+w-d.Size.X-gtx.Dp(8), (rh-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
	}
	put(fmtBytes(int64(m.Length)), width-timeW-lenW, lenW)
	put(m.Time.Format("15:04:05.000"), width-timeW, timeW)
	fill(gtx.Ops, image.Rect(0, rh-1, width, rh), alpha(th.Border, 0x80))
	return layout.Dimensions{Size: image.Pt(width, rh)}
}

var _ = time.Second
