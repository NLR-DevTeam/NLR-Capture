package ui

import (
	"image"
	"math"
	"slices"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"nlrcapture/internal/config"
)

// Columns of the request list.
const (
	colName        = "name"
	colURL         = "url"
	colStatus      = "status"
	colMethod      = "method"
	colDomain      = "domain"
	colType        = "type"
	colContentType = "contenttype"
	colProcess     = "process"
	colProtocol    = "protocol"
	colScheme      = "scheme"
	colRemote      = "remote"
	colSize        = "size"
	colTime        = "time"
	colStart       = "start"
	colComment     = "comment"
	colWaterfall   = "waterfall"
)

type columnDef struct {
	id    string
	title string
	width int // dp
	right bool
}

var columnDefs = []columnDef{
	{colDomain, "域名", 170, false},
	{colName, "名称", 260, false},
	{colURL, "URL", 360, false},
	{colStatus, "状态", 64, false},
	{colMethod, "方法", 64, false},
	{colType, "类型", 84, false},
	{colContentType, "内容类型", 150, false},
	{colProcess, "进程", 120, false},
	{colProtocol, "协议", 70, false},
	{colScheme, "协议类型", 70, false},
	{colRemote, "远程地址", 150, false},
	{colSize, "大小", 76, true},
	{colTime, "时间", 76, true},
	{colStart, "开始时间", 96, false},
	{colComment, "备注", 140, false},
	{colWaterfall, "瀑布", 220, false},
}

// The columns shown by default, in order: in the full list, and in the
// narrow list beside the details.
var (
	listDefaults   = []string{colDomain, colName, colStatus, colMethod, colProcess, colSize, colWaterfall}
	detailDefaults = []string{colDomain, colName}
)

type listCol struct {
	def     columnDef
	visible bool
	width   int // dp
	resize  struct {
		dragging       bool
		startX, startW int
	}
	// ax is where the column is drawn, in pixels; it eases towards the
	// column's place so that moved columns slide.
	ax     float32
	placed bool
}

// colSet is one arrangement of columns: the full list and the list beside
// the details each have their own.
type colSet struct {
	cols     []*listCol
	defaults []string
	tag      struct{}
	hover    *listCol
	drag     struct {
		col             *listCol
		grab, startX, x int
		pressed, active bool
	}
	lastAnim time.Time
	// animating is set when columns change places, so they slide there;
	// otherwise they follow resizing at once.
	animating bool
	// last is where the columns were drawn, for hit-testing.
	last []placedCol
}

func newColSet(saved []config.Column, defaults []string) *colSet {
	cs := &colSet{defaults: defaults}
	if len(saved) == 0 {
		cs.reset()
		return cs
	}
	byID := map[string]columnDef{}
	for _, d := range columnDefs {
		byID[d.id] = d
	}
	seen := map[string]bool{}
	for _, s := range saved {
		d, ok := byID[s.ID]
		if !ok || seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		c := &listCol{def: d, visible: s.Visible, width: d.width}
		if s.Width >= 40 {
			c.width = s.Width
		}
		cs.cols = append(cs.cols, c)
	}
	for _, d := range columnDefs {
		if !seen[d.id] {
			cs.cols = append(cs.cols, &listCol{def: d, width: d.width})
		}
	}
	cs.ensureName()
	return cs
}

// reset restores the default columns.
func (cs *colSet) reset() {
	old := map[string]*listCol{}
	for _, c := range cs.cols {
		old[c.def.id] = c
	}
	get := func(d columnDef) *listCol {
		if c, ok := old[d.id]; ok {
			c.width = d.width
			return c
		}
		return &listCol{def: d, width: d.width}
	}
	cs.cols = nil
	for _, id := range cs.defaults {
		for _, d := range columnDefs {
			if d.id == id {
				c := get(d)
				c.visible = true
				cs.cols = append(cs.cols, c)
			}
		}
	}
	for _, d := range columnDefs {
		if !slices.Contains(cs.defaults, d.id) {
			c := get(d)
			c.visible = false
			cs.cols = append(cs.cols, c)
		}
	}
}

// ensureName keeps the name column visible: it identifies the rows.
func (cs *colSet) ensureName() {
	for _, c := range cs.cols {
		if c.def.id == colName {
			c.visible = true
		}
	}
}

func (cs *colSet) save() []config.Column {
	var out []config.Column
	for _, c := range cs.cols {
		out = append(out, config.Column{ID: c.def.id, Visible: c.visible, Width: c.width})
	}
	return out
}

func (cs *colSet) visible() []*listCol {
	var v []*listCol
	for _, c := range cs.cols {
		if c.visible {
			v = append(v, c)
		}
	}
	return v
}

// flexIndex is the visible column that takes the space the others leave:
// the waterfall, or else the name column.
func flexIndex(vis []*listCol) int {
	if i := slices.IndexFunc(vis, func(c *listCol) bool { return c.def.id == colWaterfall }); i >= 0 {
		return i
	}
	if i := slices.IndexFunc(vis, func(c *listCol) bool { return c.def.id == colName }); i >= 0 {
		return i
	}
	return len(vis) - 1
}

// widths returns the pixel widths of the visible columns in a list w
// pixels wide.
func widths(gtx layout.Context, vis []*listCol, w int) []int {
	ws := make([]int, len(vis))
	used := 0
	for i, c := range vis {
		ws[i] = gtx.Dp(unit.Dp(c.width))
		used += ws[i]
	}
	if f := flexIndex(vis); f >= 0 {
		used -= ws[f]
		ws[f] = max(w-used, gtx.Dp(80))
	}
	return ws
}

// placedCol is a visible column at the position it is drawn this frame.
type placedCol struct {
	c       *listCol
	x, w    int
	flex    bool
	dragged bool
}

// place works out where each visible column goes and where it is drawn
// this frame: columns ease towards their place, and a dragged column
// follows the pointer. The dragged column comes last, to be drawn on top.
func (l *netList) place(gtx layout.Context, cs *colSet, w int) []placedCol {
	vis := cs.visible()
	ws := widths(gtx, vis, w)
	flex := flexIndex(vis)
	dt := gtx.Now.Sub(cs.lastAnim)
	cs.lastAnim = gtx.Now
	dt = min(max(dt, 0), 100*time.Millisecond)
	k := float32(1 - math.Exp(-float64(dt)/float64(70*time.Millisecond)))
	moving := false
	var out []placedCol
	var dragged *placedCol
	x := 0
	for i, c := range vis {
		target := float32(x)
		x += ws[i]
		p := placedCol{c: c, w: ws[i], flex: i == flex}
		switch {
		case cs.drag.active && cs.drag.col == c:
			c.ax = float32(min(max(cs.drag.x-cs.drag.grab, 0), max(w-ws[i], 0)))
			c.placed = true
			p.dragged = true
		case !c.placed || !cs.animating || !l.a.set.Animations:
			c.ax, c.placed = target, true
		default:
			c.ax += (target - c.ax) * k
			if d := target - c.ax; d > -0.5 && d < 0.5 {
				c.ax = target
			} else {
				moving = true
			}
		}
		p.x = int(c.ax + 0.5)
		if p.dragged {
			dragged = &p
			continue
		}
		out = append(out, p)
	}
	if dragged != nil {
		out = append(out, *dragged)
	}
	if !moving && !cs.drag.active {
		cs.animating = false
	}
	if moving || cs.drag.active {
		gtx.Execute(op.InvalidateCmd{})
	}
	return out
}

// reorder moves the dragged column to the slot nearest to where it is
// being held, so the table rearranges itself while the column moves.
func (l *netList) reorder(gtx layout.Context, cs *colSet, w int) {
	d := cs.drag.col
	vis := cs.visible()
	ws := widths(gtx, vis, w)
	var others []*listCol
	var ow []int
	for i, c := range vis {
		if c != d {
			others, ow = append(others, c), append(ow, ws[i])
		}
	}
	want := cs.drag.x - cs.drag.grab
	best, bestD := 0, math.MaxInt
	x := 0
	for j := 0; j <= len(others); j++ {
		if dd := abs(x - want); dd < bestD {
			best, bestD = j, dd
		}
		if j < len(others) {
			x += ow[j]
		}
	}
	order := slices.Clone(others[:best])
	order = append(order, d)
	order = append(order, others[best:]...)
	if slices.Equal(order, vis) {
		return
	}
	for _, c := range cs.cols {
		if !c.visible {
			order = append(order, c)
		}
	}
	cs.cols = order
	cs.animating = true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// headerEvents handles the pointer over the column titles: a click sorts,
// a drag moves the column, a right click picks the columns. It hit-tests
// against where the columns were drawn last frame.
func (l *netList) headerEvents(gtx layout.Context, cs *colSet, w int) {
	cols := cs.last
	at := func(x int) *placedCol {
		for i := len(cols) - 1; i >= 0; i-- {
			if x >= cols[i].x && x < cols[i].x+cols[i].w {
				return &cols[i]
			}
		}
		return nil
	}
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &cs.tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		x := int(pe.Position.X)
		switch pe.Kind {
		case pointer.Move:
			cs.hover = nil
			if p := at(x); p != nil {
				cs.hover = p.c
			}
		case pointer.Leave:
			cs.hover = nil
		case pointer.Press:
			p := at(x)
			if p == nil {
				break
			}
			if pe.Buttons.Contain(pointer.ButtonSecondary) {
				l.columnMenu(cs)
				break
			}
			cs.drag.col, cs.drag.grab, cs.drag.startX, cs.drag.x = p.c, x-p.x, x, x
			cs.drag.pressed, cs.drag.active = true, false
		case pointer.Drag:
			if !cs.drag.pressed {
				break
			}
			cs.drag.x = x
			if !cs.drag.active && abs(x-cs.drag.startX) > gtx.Dp(5) {
				cs.drag.active = true
			}
			if cs.drag.active {
				l.reorder(gtx, cs, w)
			}
		case pointer.Release, pointer.Cancel:
			if !cs.drag.pressed {
				break
			}
			if cs.drag.active {
				l.saveColumns()
			} else if pe.Kind == pointer.Release {
				l.toggleSort(cs.drag.col.def.id)
			}
			cs.drag.pressed, cs.drag.active, cs.drag.col = false, false, nil
			cs.animating = true
		}
	}
}

// header draws the column titles and the resize handles.
func (l *netList) header(gtx layout.Context, cs *colSet, cols []placedCol, w, h, sepH int) {
	th := l.a.th

	area := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &cs.tag)
	area.Pop()
	one := max(gtx.Dp(1), 1)
	for _, p := range cols {
		st := op.Offset(image.Pt(p.x, 0)).Push(gtx.Ops)
		r := image.Rect(0, 0, p.w, h)
		switch {
		case p.dragged:
			fill(gtx.Ops, r, th.Bg3)
			fill(gtx.Ops, image.Rect(0, h-gtx.Dp(2), p.w, h), th.Accent)
			fill(gtx.Ops, image.Rect(0, 0, one, sepH), th.Accent)
			fill(gtx.Ops, image.Rect(p.w-one, 0, p.w, sepH), th.Accent)
		case cs.hover == p.c && !cs.drag.active:
			fill(gtx.Ops, r, th.Bg3)
		}
		label := p.c.def.title
		if l.sortCol == p.c.def.id {
			if l.sortDesc {
				label += " ▼"
			} else {
				label += " ▲"
			}
		}
		cg := gtx
		cg.Constraints = layout.Exact(r.Max)
		cl := clip.Rect(r).Push(gtx.Ops)
		layout.Inset{Left: 8, Right: 8}.Layout(cg, func(gtx layout.Context) layout.Dimensions {
			dir := layout.W
			if p.c.def.right {
				dir = layout.E
			}
			return dir.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return th.txtW(gtx, label, 12, th.Text2, font.Medium)
			})
		})
		cl.Pop()
		if p.x > 0 && !p.dragged {
			fill(gtx.Ops, image.Rect(0, 0, one, sepH), th.Border)
		}
		st.Pop()
	}
	fill(gtx.Ops, image.Rect(0, h-one, w, h), th.Border)
	// Resize handles go on top of the title area.
	if !cs.drag.active {
		for _, p := range cols {
			if !p.flex {
				l.resizeHandle(gtx, p.c, p.x+p.w, h)
			}
		}
	}
}

// toggleSort cycles a column through ascending, descending and arrival
// order. The waterfall sorts by start time.
func (l *netList) toggleSort(id string) {
	switch {
	case l.sortCol != id:
		l.sortCol, l.sortDesc = id, false
	case !l.sortDesc:
		l.sortDesc = true
	default:
		l.sortCol, l.sortDesc = "", false
	}
	l.dirty = true
	l.saveColumns()
}

func (l *netList) resizeHandle(gtx layout.Context, c *listCol, x, h int) {
	tag := &c.resize
	for {
		e, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			tag.dragging, tag.startX, tag.startW = true, l.a.mouse.X, c.width
		case pointer.Drag:
			if tag.dragging {
				d := float32(l.a.mouse.X-tag.startX) / gtx.Metric.PxPerDp
				c.width = max(tag.startW+int(d), 40)
			}
		case pointer.Release, pointer.Cancel:
			if tag.dragging {
				tag.dragging = false
				l.saveColumns()
			}
		}
	}
	hw := gtx.Dp(6)
	r := image.Rect(x-hw/2, 0, x+hw/2, h)
	ar := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	pointer.CursorColResize.Add(gtx.Ops)
	ar.Pop()
}

func (l *netList) saveColumns() {
	list, detail := l.full.save(), l.detail.save()
	l.a.updateSettings(func(s *config.Settings) {
		s.ListColumns, s.DetailColumns = list, detail
		s.SortCol, s.SortDesc = l.sortCol, l.sortDesc
	})
}

// columnMenu shows or hides columns of the set the header belongs to.
func (l *netList) columnMenu(cs *colSet) {
	var items []MenuItem
	for _, d := range columnDefs {
		if d.id == colName {
			continue
		}
		var c *listCol
		for _, x := range cs.cols {
			if x.def.id == d.id {
				c = x
			}
		}
		if c == nil {
			continue
		}
		items = append(items, MenuItem{Label: d.title, Checked: c.visible, Do: func() {
			c.visible = !c.visible
			c.placed = false
			cs.animating = true
			l.saveColumns()
		}})
	}
	items = append(items, MenuItem{Sep: true}, MenuItem{Label: "重置列", Icon: icRefresh, Do: func() {
		cs.reset()
		for _, c := range cs.cols {
			c.placed = false
		}
		l.saveColumns()
	}})
	l.a.Menu(items...)
}
