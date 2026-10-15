package ui

import (
	"cmp"
	"image"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/config"
	"nlrcapture/internal/proxy"
	"nlrcapture/internal/winutil"
)

const rowH = unit.Dp(22)

type listStats struct {
	total, shown           int
	totalBytes, shownBytes int64
	shownRes               int64
	span                   time.Duration
	selected               int
}

// netList is the request table.
type netList struct {
	a *App
	// The full list and the narrow list beside the details arrange their
	// columns separately.
	full, detail *colSet

	view    []*capture.Session
	viewVer uint64
	dirty   bool
	st      listStats
	tmin    time.Time
	tmax    time.Time
	running bool

	sortCol  string
	sortDesc bool

	selID  int64
	multi  map[int64]bool
	anchor int64

	list    widget.List
	listTag struct{}
	hover   int
	focused bool
	// press is the row a button went down on; the row is selected when
	// the button comes up on it without the pointer having moved off.
	press struct {
		idx     int
		pos     image.Point
		buttons pointer.Buttons
		mods    key.Modifiers
		active  bool
	}
	prevN int
	split Splitter

	lastHit  int
	lastTime time.Time
}

func newNetList(a *App) *netList {
	l := &netList{a: a, multi: map[int64]bool{}, hover: -1, lastHit: -1, sortCol: a.set.SortCol, sortDesc: a.set.SortDesc}
	l.full = newColSet(a.set.ListColumns, listDefaults)
	l.detail = newColSet(a.set.DetailColumns, detailDefaults)
	l.list.Axis = layout.Vertical
	return l
}

func (l *netList) invalidate() { l.dirty = true }

// refresh rebuilds the visible list when the store or the filter changed.
func (l *netList) refresh() {
	st := l.a.store
	v := st.Version()
	if v == l.viewVer && !l.dirty {
		return
	}
	l.viewVer, l.dirty = v, false
	all := st.All()
	f := &l.a.filter
	view := l.view[:0]
	if f.Empty() {
		view = append(view, all...)
	} else {
		for _, s := range all {
			if f.Match(s) {
				view = append(view, s)
			}
		}
	}
	if l.sortCol != "" {
		l.sortView(view)
	}
	l.view = view
	var stt listStats
	stt.total, stt.shown = len(all), len(view)
	for _, s := range all {
		stt.totalBytes += s.Transferred()
	}
	l.tmin, l.tmax, l.running = time.Time{}, time.Time{}, false
	now := time.Now()
	for _, s := range view {
		stt.shownBytes += s.Transferred()
		stt.shownRes += s.RespBodySize
		if l.tmin.IsZero() || s.Started.Before(l.tmin) {
			l.tmin = s.Started
		}
		end := s.Ended
		if s.Running() {
			end = now
			l.running = true
		}
		if end.After(l.tmax) {
			l.tmax = end
		}
	}
	if !l.tmin.IsZero() {
		stt.span = l.tmax.Sub(l.tmin)
	}
	stt.selected = len(l.multi)
	l.st = stt
	// Drop selections of sessions that are gone.
	for id := range l.multi {
		if st.Get(id) == nil {
			delete(l.multi, id)
		}
	}
	if l.selID != 0 && st.Get(l.selID) == nil {
		l.selID = 0
	}
}

func (l *netList) stats() listStats {
	l.refresh()
	s := l.st
	s.selected = len(l.multi)
	return s
}

func sortKey(s *capture.Session, col string) any {
	switch col {
	case colName:
		return strings.ToLower(s.Name())
	case colURL:
		return s.URL
	case colStatus:
		return s.Status
	case colMethod:
		return s.Method
	case colDomain:
		return s.Hostname()
	case colType:
		return capture.TypeLabel(s)
	case colContentType:
		return s.ContentType()
	case colProcess:
		return strings.ToLower(s.Process)
	case colProtocol:
		return s.Proto
	case colScheme:
		return s.Scheme
	case colRemote:
		return s.ServerAddr
	case colSize:
		return s.Transferred()
	case colTime:
		return int64(s.Duration())
	case colComment:
		return s.Comment
	}
	return s.Started.UnixNano()
}

func (l *netList) sortView(v []*capture.Session) {
	col, desc := l.sortCol, l.sortDesc
	slices.SortStableFunc(v, func(x, y *capture.Session) int {
		var c int
		switch kx := sortKey(x, col).(type) {
		case string:
			c = strings.Compare(kx, sortKey(y, col).(string))
		case int:
			c = cmp.Compare(kx, sortKey(y, col).(int))
		case int64:
			c = cmp.Compare(kx, sortKey(y, col).(int64))
		}
		if c == 0 {
			c = cmp.Compare(x.ID, y.ID)
		}
		if desc {
			c = -c
		}
		return c
	})
}

// ---- Selection --------------------------------------------------------

func (l *netList) selected() *capture.Session {
	if l.selID == 0 {
		return nil
	}
	return l.a.store.Get(l.selID)
}

func (l *netList) selectID(id int64) {
	l.selID, l.anchor = id, id
	l.multi = map[int64]bool{id: true}
	l.refresh()
	for i, s := range l.view {
		if s.ID == id {
			l.ensureVisible(i)
			break
		}
	}
}

func (l *netList) clearSelection() {
	l.selID = 0
	l.multi = map[int64]bool{}
}

// selection returns the selected sessions in list order, or the primary
// one alone.
func (l *netList) selection() []*capture.Session {
	var out []*capture.Session
	for _, s := range l.view {
		if l.multi[s.ID] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		if s := l.selected(); s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (l *netList) indexOf(id int64) int {
	for i, s := range l.view {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func (l *netList) selectRange(from, to int) {
	if from > to {
		from, to = to, from
	}
	l.multi = map[int64]bool{}
	for i := max(from, 0); i <= to && i < len(l.view); i++ {
		l.multi[l.view[i].ID] = true
	}
}

func (l *netList) ensureVisible(i int) {
	first, count := l.list.Position.First, l.list.Position.Count
	if i <= first {
		l.list.ScrollTo(i)
	} else if count > 0 && i >= first+count-1 {
		l.list.ScrollTo(max(i-count+2, 0))
	}
}

// ---- Events -----------------------------------------------------------

func (l *netList) key(gtx layout.Context, e key.Event) {
	n := len(l.view)
	cur := l.indexOf(l.selID)
	move := func(to int) {
		if n == 0 {
			return
		}
		to = min(max(to, 0), n-1)
		id := l.view[to].ID
		if e.Modifiers.Contain(key.ModShift) && l.anchor != 0 {
			a := l.indexOf(l.anchor)
			l.selectRange(a, to)
			l.selID = id
		} else {
			l.selID, l.anchor = id, id
			l.multi = map[int64]bool{id: true}
		}
		l.ensureVisible(to)
	}
	page := max(l.list.Position.Count-1, 1)
	switch e.Name {
	case key.NameUpArrow:
		if cur < 0 {
			move(n - 1)
		} else {
			move(cur - 1)
		}
	case key.NameDownArrow:
		move(cur + 1)
	case key.NameHome:
		move(0)
	case key.NameEnd:
		move(n - 1)
	case key.NamePageUp:
		move(cur - page)
	case key.NamePageDown:
		move(cur + page)
	case key.NameDeleteForward:
		l.removeSelected()
	case "A":
		l.selectRange(0, n-1)
	case "C":
		if s := l.selected(); s != nil {
			var urls []string
			for _, x := range l.selection() {
				urls = append(urls, x.URL)
			}
			l.a.copyText(strings.Join(urls, "\n"), "网址")
		}
	case "R":
		for _, s := range l.selection() {
			l.a.svc.engine.Replay(s)
		}
	}
}

func (l *netList) removeSelected() {
	ids := map[int64]bool{}
	for _, s := range l.selection() {
		ids[s.ID] = true
	}
	l.a.store.Remove(ids)
	l.clearSelection()
}

func (l *netList) events(gtx layout.Context) {
	for {
		e, ok := gtx.Event(
			key.FocusFilter{Target: l},
			key.Filter{Focus: l, Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Focus: l, Name: key.NameDownArrow, Optional: key.ModShift},
			key.Filter{Focus: l, Name: key.NameHome, Optional: key.ModShift},
			key.Filter{Focus: l, Name: key.NameEnd, Optional: key.ModShift},
			key.Filter{Focus: l, Name: key.NamePageUp, Optional: key.ModShift},
			key.Filter{Focus: l, Name: key.NamePageDown, Optional: key.ModShift},
			key.Filter{Focus: l, Name: key.NameDeleteForward},
			key.Filter{Focus: l, Name: "A", Required: key.ModCtrl},
			key.Filter{Focus: l, Name: "C", Required: key.ModCtrl},
			key.Filter{Focus: l, Name: "R"},
		)
		if !ok {
			break
		}
		switch e := e.(type) {
		case key.FocusEvent:
			l.focused = e.Focus
		case key.Event:
			if e.State == key.Press {
				l.key(gtx, e)
			}
		}
	}
}

// Layout draws the list; compact is the narrow list beside the details,
// which has its own columns.
func (l *netList) Layout(gtx layout.Context, compact bool) layout.Dimensions {
	a := l.a
	th := a.th
	l.refresh()
	l.events(gtx)
	// Follow new requests while the list is scrolled to its end. The
	// list itself is laid out from the top.
	if n := len(l.view); a.set.AutoScroll && l.sortCol == "" && n > l.prevN && !l.list.Position.BeforeEnd {
		l.list.Position.First = n + 1
	}
	l.prevN = len(l.view)
	if l.running {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(500 * time.Millisecond)})
	}
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg0)
	cs := l.full
	if compact {
		cs = l.detail
	}
	hdrH := gtx.Dp(26)
	rh := gtx.Dp(rowH)
	listH := size.Y - hdrH

	l.headerEvents(gtx, cs, size.X)
	cols := l.place(gtx, cs, size.X)
	cs.last = cols
	sepH := size.Y
	if len(l.view) == 0 {
		sepH = hdrH
	}
	l.header(gtx, cs, cols, size.X, hdrH, sepH)

	l.rowEvents(gtx, size.X, listH, rh)
	body := op.Offset(image.Pt(0, hdrH)).Push(gtx.Ops)
	area := clip.Rect{Max: image.Pt(size.X, listH)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &l.listTag)
	event.Op(gtx.Ops, l)
	if len(l.view) == 0 {
		l.empty(gtx, image.Pt(size.X, listH))
	} else {
		lg := gtx
		lg.Constraints = layout.Exact(image.Pt(size.X, listH))
		th.list(lg, &l.list, len(l.view), func(gtx layout.Context, i int) layout.Dimensions {
			return l.row(gtx, i, cols, rh)
		})
	}
	area.Pop()
	body.Pop()
	return layout.Dimensions{Size: size}
}

// rowEvents handles the pointer over the rows. A row is selected when the
// button comes up on the row it went down on, so dragging the scrollbar or
// scrolling never selects anything.
func (l *netList) rowEvents(gtx layout.Context, w, listH, rh int) {
	a := l.a
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &l.listTag, Kinds: pointer.Press | pointer.Release | pointer.Drag | pointer.Cancel | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		pos := pe.Position.Round()
		idx := l.list.Position.First + (pos.Y+l.list.Position.Offset)/rh
		if idx < 0 || idx >= len(l.view) || pos.Y < 0 || pos.Y >= listH {
			idx = -1
		}
		p := &l.press
		switch pe.Kind {
		case pointer.Leave:
			l.hover = -1
		case pointer.Move:
			l.hover = idx
		case pointer.Press:
			gtx.Execute(key.FocusCmd{Tag: l})
			l.focused = true
			p.idx, p.pos, p.buttons, p.mods = idx, pos, pe.Buttons, pe.Modifiers
			// The scrollbar runs along the right edge.
			p.active = pos.X < w-gtx.Dp(14)
		case pointer.Drag:
			if d := pos.Sub(p.pos); abs(d.X)+abs(d.Y) > gtx.Dp(4) {
				p.active = false
			}
		case pointer.Cancel:
			p.active = false
		case pointer.Release:
			if !p.active || idx != p.idx {
				p.active = false
				break
			}
			p.active = false
			if idx < 0 {
				if p.buttons.Contain(pointer.ButtonPrimary) {
					l.clearSelection()
				}
				break
			}
			s := l.view[idx]
			switch {
			case p.buttons.Contain(pointer.ButtonSecondary):
				if !l.multi[s.ID] {
					l.selID, l.anchor = s.ID, s.ID
					l.multi = map[int64]bool{s.ID: true}
				}
				l.rowMenu()
			case p.buttons.Contain(pointer.ButtonPrimary):
				double := idx == l.lastHit && gtx.Now.Sub(l.lastTime) < 450*time.Millisecond
				l.lastHit, l.lastTime = idx, gtx.Now
				switch {
				case p.mods.Contain(key.ModCtrl):
					if l.multi[s.ID] {
						delete(l.multi, s.ID)
						if l.selID == s.ID {
							l.selID = 0
							for _, x := range l.view {
								if l.multi[x.ID] {
									l.selID = x.ID
								}
							}
						}
					} else {
						l.multi[s.ID] = true
						l.selID = s.ID
					}
					l.anchor = s.ID
				case p.mods.Contain(key.ModShift) && l.anchor != 0:
					l.selectRange(l.indexOf(l.anchor), idx)
					l.selID = s.ID
				default:
					l.selID, l.anchor = s.ID, s.ID
					l.multi = map[int64]bool{s.ID: true}
					if double && s.Kind != capture.KindTunnel {
						l.lastHit = -1
						a.details.tab = tabResponse
					}
				}
			}
		}
	}
}

func (l *netList) empty(gtx layout.Context, size image.Point) {
	th := l.a.th
	gtx.Constraints = layout.Exact(size)
	msg := "正在录制网络活动…"
	switch {
	case l.a.store.Len() > 0:
		msg = "没有匹配的请求"
	case !l.a.svc.engine.Recording():
		msg = "录制已暂停"
	}
	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return th.txt(gtx, msg, 13, th.Text3)
	})
}

// typeIcon returns the icon and color for a session's resource type.
func (th *Theme) typeIcon(s *capture.Session) (*Icon, color.NRGBA) {
	switch capture.ResourceType(s) {
	case capture.TypeDocument:
		return icDoc, th.Blue
	case capture.TypeScript:
		return icCode, th.Warn
	case capture.TypeStylesheet:
		return icStyle, th.Purple
	case capture.TypeImage:
		return icImage, th.Green
	case capture.TypeMedia:
		return icMedia, th.Teal
	case capture.TypeFont:
		return icFont, th.Orange
	case capture.TypeAPI:
		return icFetch, th.Text2
	case capture.TypeWebSocket:
		return icWS, th.Teal
	case capture.TypeWasm:
		return icWasm, th.Purple
	case capture.TypeManifest:
		return icManifest, th.Text2
	case capture.TypeTunnel:
		if s.Failed() {
			return icLock, th.Danger
		}
		return icLock, th.Text3
	}
	return icFile, th.Text3
}

func statusText(s *capture.Session) string {
	switch {
	case s.State == capture.StatePaused:
		return "断点"
	case s.State == capture.StateBlocked && s.Status == 0:
		return "已屏蔽"
	case s.Kind == capture.KindTunnel:
		if s.State == capture.StateFailed {
			return "失败"
		}
		if s.Running() {
			return "隧道"
		}
		return "隧道"
	case s.State == capture.StateFailed && s.Status == 0:
		return "失败"
	case s.Status == 0:
		return "待处理"
	}
	return strconv.Itoa(s.Status)
}

func (l *netList) rowColor(s *capture.Session) color.NRGBA {
	th := l.a.th
	switch {
	case s.State == capture.StatePaused:
		return th.Warn
	case s.Failed() || s.Status >= 400:
		return th.Danger
	case s.Kind == capture.KindTunnel:
		return th.Text2
	}
	return th.Text
}

func (l *netList) cellText(s *capture.Session, col string) string {
	switch col {
	case colName:
		return s.Name()
	case colURL:
		return s.URL
	case colStatus:
		return statusText(s)
	case colMethod:
		return s.Method
	case colDomain:
		return s.Hostname()
	case colType:
		return capture.TypeLabel(s)
	case colContentType:
		return s.ContentType()
	case colProcess:
		if s.PID > 0 {
			return s.Process
		}
		return s.Process
	case colProtocol:
		p := s.Proto
		if s.RespProto != "" && s.Proto != "h2" {
			p = s.RespProto
		}
		return strings.ToLower(strings.TrimPrefix(p, "HTTP/"))
	case colScheme:
		return s.Scheme
	case colRemote:
		return s.ServerAddr
	case colSize:
		if s.Kind == capture.KindTunnel {
			return fmtBytes(s.BytesUp + s.BytesDown)
		}
		if s.Status == 0 {
			return ""
		}
		return fmtBytes(s.Transferred())
	case colTime:
		if s.Running() {
			return "待处理"
		}
		return fmtDuration(s.Duration())
	case colStart:
		return s.Started.Format("15:04:05.000")
	case colComment:
		return s.Comment
	}
	return ""
}

func (l *netList) row(gtx layout.Context, i int, cols []placedCol, rh int) layout.Dimensions {
	th := l.a.th
	w := gtx.Constraints.Max.X
	size := image.Pt(w, rh)
	if i >= len(l.view) {
		return layout.Dimensions{Size: size}
	}
	s := l.view[i]
	r := image.Rectangle{Max: size}
	sel := l.multi[s.ID]
	var bg color.NRGBA
	switch {
	case l.press.active && l.press.idx == i && !sel:
		// Answer the press at once; the row is selected on release.
		bg = alpha(th.Accent, 0x1a)
	case sel && l.focused:
		bg = alpha(th.Accent, 0x30)
	case sel:
		bg = th.Bg4
	case l.hover == i:
		bg = th.Bg3
	case s.Mark != 0:
		bg = markColor(th, s.Mark)
	case i%2 == 1:
		bg = th.Stripe
	}
	fill(gtx.Ops, r, bg)
	if s.ID == l.selID && sel {
		fill(gtx.Ops, image.Rect(0, 0, gtx.Dp(2), rh), th.Accent)
	}
	fg := l.rowColor(s)
	pad := gtx.Dp(8)
	one := max(gtx.Dp(1), 1)
	for _, p := range cols {
		c, cw := p.c, p.w
		st := op.Offset(image.Pt(p.x, 0)).Push(gtx.Ops)
		if p.dragged {
			// The dragged column floats over the others.
			fill(gtx.Ops, image.Rect(0, 0, cw, rh), th.Bg2)
			fill(gtx.Ops, image.Rect(0, 0, cw, rh), bg)
			fill(gtx.Ops, image.Rect(0, 0, one, rh), th.Accent)
			fill(gtx.Ops, image.Rect(cw-one, 0, cw, rh), th.Accent)
		}
		if cw > 2*pad {
			cl := clip.Rect{Max: image.Pt(cw, rh)}.Push(gtx.Ops)
			switch c.def.id {
			case colWaterfall:
				l.waterfall(gtx, s, cw, rh)
			case colName:
				ic, icc := th.typeIcon(s)
				is := gtx.Dp(14)
				io := op.Offset(image.Pt(pad, (rh-is)/2)).Push(gtx.Ops)
				drawIcon(gtx, ic, 14, icc)
				io.Pop()
				l.cell(gtx, image.Pt(pad+is+gtx.Dp(6), 0), cw-pad*2-is-gtx.Dp(6), rh, l.cellText(s, colName), fg, false)
			default:
				c2 := fg
				if c.def.id != colStatus && c.def.id != colMethod && !(s.Failed() || s.Status >= 400) {
					c2 = th.Text2
				}
				l.cell(gtx, image.Pt(pad, 0), cw-2*pad, rh, l.cellText(s, c.def.id), c2, c.def.right)
			}
			cl.Pop()
		}
		if p.x > 0 && !p.dragged {
			fill(gtx.Ops, image.Rect(0, 0, one, rh), alpha(th.Border, 0x99))
		}
		st.Pop()
	}
	return layout.Dimensions{Size: size}
}

// shapeW is the width cell text is shaped for, whatever the column's. The
// text shaper caches by width as well as text, so shaping for the column
// width made every visible cell be shaped again whenever a column changed
// width: when the details opened and the list narrowed, while resizing a
// column or the window. Text that does not fit is cut and ended with an
// ellipsis here instead.
const shapeW = 1 << 14

func (l *netList) cell(gtx layout.Context, at image.Point, w, rh int, text string, c color.NRGBA, right bool) {
	if w <= 0 || text == "" {
		return
	}
	th := l.a.th
	cg := gtx
	cg.Constraints = layout.Constraints{Max: image.Pt(shapeW, rh)}
	rec := op.Record(gtx.Ops)
	d := Label{Text: text, Size: 12, Color: c}.Layout(cg, th)
	call := rec.Stop()
	y := (rh - d.Size.Y) / 2
	if d.Size.X <= w {
		x := at.X
		if right {
			x = at.X + w - d.Size.X
		}
		st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		return
	}
	rec = op.Record(gtx.Ops)
	e := Label{Text: "…", Size: 12, Color: c}.Layout(cg, th)
	ell := rec.Stop()
	tw := max(w-e.Size.X, 0)
	cl := clip.Rect{Min: image.Pt(at.X, 0), Max: image.Pt(at.X+tw, rh)}.Push(gtx.Ops)
	st := op.Offset(image.Pt(at.X, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
	cl.Pop()
	st = op.Offset(image.Pt(at.X+tw, y)).Push(gtx.Ops)
	ell.Add(gtx.Ops)
	st.Pop()
}

// waterfall draws a session's phases on the shared timeline.
func (l *netList) waterfall(gtx layout.Context, s *capture.Session, w, rh int) {
	th := l.a.th
	span := l.tmax.Sub(l.tmin)
	if span <= 0 {
		span = time.Millisecond
	}
	pad := gtx.Dp(6)
	inner := w - 2*pad
	if inner <= 0 {
		return
	}
	pos := func(t time.Time) int {
		return pad + int(float64(t.Sub(l.tmin))/float64(span)*float64(inner))
	}
	bh := gtx.Dp(8)
	y0 := (rh - bh) / 2
	start := s.Started
	end := s.Ended
	if s.Running() {
		end = time.Now()
	}
	tm := s.Timings
	seg := func(from, to time.Time, c color.NRGBA) {
		x0, x1 := pos(from), pos(to)
		if x1-x0 < 2 {
			x1 = x0 + 2
		}
		fill(gtx.Ops, image.Rect(x0, y0, x1, y0+bh), c)
	}
	if s.Kind == capture.KindTunnel || s.Source == capture.SourceImport && tm.Wait < 0 {
		seg(start, end, alpha(th.Text3, 0x99))
		return
	}
	t := start
	pre := max(tm.Blocked, 0) + max(tm.DNS, 0) + max(tm.Connect, 0)
	if pre > 0 {
		seg(t, t.Add(pre), alpha(th.Orange, 0xb0))
		t = t.Add(pre)
	}
	if s.Status == 0 && s.Running() {
		seg(t, end, alpha(th.Green, 0x70))
		return
	}
	wait := max(tm.Send, 0) + max(tm.Wait, 0)
	if wait > 0 {
		seg(t, t.Add(wait), th.Green)
		t = t.Add(wait)
	}
	if t.Before(end) {
		c := th.Blue
		if s.Failed() {
			c = th.Danger
		}
		seg(t, end, c)
	} else if pre == 0 && wait == 0 {
		seg(start, end, th.Blue)
	}
}

func markColor(th *Theme, m int) color.NRGBA {
	switch m {
	case 1:
		return alpha(th.Warn, 0x30)
	case 2:
		return alpha(th.Danger, 0x28)
	case 3:
		return alpha(th.Blue, 0x28)
	case 4:
		return alpha(th.Green, 0x28)
	}
	return color.NRGBA{}
}

// ---- Menus ------------------------------------------------------------

func (l *netList) rowMenu() {
	a := l.a
	sel := l.selection()
	if len(sel) == 0 {
		return
	}
	s := sel[0]
	eng := a.svc.engine
	httpOnly := s.Kind != capture.KindTunnel
	host := s.Hostname()
	copyItems := []MenuItem{
		{Label: "复制链接", Do: func() {
			var urls []string
			for _, x := range sel {
				urls = append(urls, x.URL)
			}
			a.copyText(strings.Join(urls, "\n"), "链接")
		}},
		{Label: "复制 cURL (bash) 格式", Disabled: !httpOnly, Do: func() { a.copyText(capture.CurlBash(s), "") }},
		{Label: "复制 cURL (cmd) 格式", Disabled: !httpOnly, Do: func() { a.copyText(capture.CurlCmd(s), "") }},
		{Label: "复制 PowerShell 格式", Disabled: !httpOnly, Do: func() { a.copyText(capture.PowerShell(s), "") }},
		{Label: "复制 JS fetch 格式", Disabled: !httpOnly, Do: func() { a.copyText(capture.Fetch(s), "") }},
		{Sep: true},
		{Label: "复制响应", Disabled: s.Status == 0, Do: func() {
			if txt, ok, _ := capture.BodyText(s, true); ok {
				a.copyText(txt, "")
			} else {
				a.Toast(toastInfo, "响应不是文本")
			}
		}},
		{Label: "复制请求标头", Do: func() { a.copyText(capture.HeaderText(s.ReqHeader), "") }},
		{Label: "复制响应标头", Disabled: s.Status == 0, Do: func() { a.copyText(capture.HeaderText(s.RespHeader), "") }},
		{Label: "复制为 HAR", Do: func() { a.copyHAR(sel) }},
	}
	exportItems := []MenuItem{
		{Label: "导出选中请求为 .har", Do: func() { a.exportSessions(sel, "selected.har") }},
		{Label: "导出全部请求为 .har", Do: func() { a.pickExport(true) }},
	}
	replayItems := []MenuItem{
		{Label: "重放", Hint: "R", Disabled: !httpOnly, Do: func() {
			for _, x := range sel {
				eng.Replay(x)
			}
		}},
		{Label: "编辑并重放…", Hint: "Ctrl+K", Disabled: !httpOnly, Do: func() { a.Open(newComposeDialog(a, s)) }},
		{Label: "在默认浏览器打开", Disabled: !httpOnly, Do: func() { openInBrowser(s) }},
	}
	debugItems := []MenuItem{
		{Label: "屏蔽请求地址", Disabled: !httpOnly, Do: func() {
			a.addRule(proxy.Rule{Enabled: true, Match: "exact:" + s.URL, Action: "*block"}, "已屏蔽")
		}},
		{Label: "屏蔽域名 " + host, Do: func() {
			a.addRule(proxy.Rule{Enabled: true, Match: "regex:^\\w+://" + regexpQuote(host) + "[:/]", Action: "*block"}, "已屏蔽 "+host)
		}},
		{Label: "添加自动响应规则…", Disabled: !httpOnly, Do: func() {
			d := newRulesDialog(a)
			d.startNew(proxy.Rule{Enabled: true, Match: "exact:" + s.URL, Action: "*200"})
			a.Open(d)
		}},
		{Label: "在此网址设置断点", Disabled: !httpOnly, Do: func() {
			a.updateSettings(func(st *config.Settings) {
				ps := proxy.SplitPatterns(st.Breakpoints.Match)
				if p := "exact:" + s.URL; !slices.Contains(ps, p) {
					ps = append(ps, p)
				}
				st.Breakpoints = proxy.Breakpoints{Request: true, Response: st.Breakpoints.Response, Match: strings.Join(ps, "\n")}
			})
			a.svc.applyConfig()
			a.Toast(toastOK, "已设置断点")
		}},
		{Label: "不解密此主机", Disabled: !s.Decrypted && s.Kind != capture.KindTunnel, Do: func() {
			a.updateSettings(func(st *config.Settings) {
				if !slices.Contains(st.NoDecrypt, host) {
					st.NoDecrypt = append(st.NoDecrypt, host)
				}
			})
			a.svc.applyConfig()
			a.Toast(toastOK, "已不解密 "+host)
		}},
	}
	markLabel := "添加标记"
	if s.Mark != 0 {
		markLabel = "取消标记"
	}
	markItems := []MenuItem{
		{Label: markLabel, Do: func() {
			m := 1
			if s.Mark != 0 {
				m = 0
			}
			for _, x := range sel {
				a.store.Update(x.ID, func(x *capture.Session) { x.Mark = m })
			}
		}},
		{Label: "添加备注", Do: func() {
			a.Open(newInputDialog("备注", "", s.Comment, func(v string) {
				for _, x := range sel {
					a.store.Update(x.ID, func(x *capture.Session) { x.Comment = v })
				}
			}))
		}},
	}
	deleteItems := []MenuItem{
		{Label: "删除所选", Hint: "Del", Do: l.removeSelected},
		{Label: "清空列表", Hint: "Ctrl+L", Danger: true, Do: a.clearAll},
	}
	a.Menu(
		MenuItem{Label: "复制", Icon: icCopy, Sub: copyItems},
		MenuItem{Label: "导出", Icon: icExport, Sub: exportItems},
		MenuItem{Label: "重放", Icon: icReplay, Sub: replayItems},
		MenuItem{Label: "断点和调试", Icon: icPause, Sub: debugItems},
		MenuItem{Label: "标记", Icon: icMark, Sub: markItems},
		MenuItem{Label: "删除", Icon: icDelete, Sub: deleteItems},
	)
}

// openInBrowser opens the request's URL with a plain GET in the default
// browser.
func openInBrowser(s *capture.Session) {
	u := s.URL
	switch {
	case strings.HasPrefix(u, "wss://"):
		u = "https://" + u[6:]
	case strings.HasPrefix(u, "ws://"):
		u = "http://" + u[5:]
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return
	}
	go winutil.Open(u)
}
