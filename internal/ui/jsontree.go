package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// jnode is one value in a JSON document, in source order.
type jnode struct {
	key    string
	index  bool // key is an array index
	kind   byte // 'o' object, 'a' array, 's' string, 'n' number, 'b' bool, 'z' null
	val    string
	kids   []*jnode
	open   bool
	depth  int
	parent *jnode
}

const maxJSONNodes = 300000

func parseJSON(data []byte) (*jnode, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	count := 0
	var parse func(key string, index bool, depth int, parent *jnode) (*jnode, error)
	parse = func(key string, index bool, depth int, parent *jnode) (*jnode, error) {
		count++
		if count > maxJSONNodes {
			return nil, fmt.Errorf("too large")
		}
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		n := &jnode{key: key, index: index, depth: depth, parent: parent}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				n.kind = 'o'
				for dec.More() {
					kt, err := dec.Token()
					if err != nil {
						return nil, err
					}
					k, _ := kt.(string)
					c, err := parse(k, false, depth+1, n)
					if err != nil {
						return nil, err
					}
					n.kids = append(n.kids, c)
				}
				dec.Token()
			case '[':
				n.kind = 'a'
				i := 0
				for dec.More() {
					c, err := parse(strconv.Itoa(i), true, depth+1, n)
					if err != nil {
						return nil, err
					}
					n.kids = append(n.kids, c)
					i++
				}
				dec.Token()
			}
		case string:
			n.kind, n.val = 's', v
		case json.Number:
			n.kind, n.val = 'n', v.String()
		case bool:
			n.kind, n.val = 'b', strconv.FormatBool(v)
		case nil:
			n.kind, n.val = 'z', "null"
		}
		return n, nil
	}
	root, err := parse("", false, 0, nil)
	if err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return root, true
}

// summary renders a compact preview of a node, like the DevTools
// console's object previews.
func (n *jnode) summary(limit int) string {
	var b strings.Builder
	var write func(n *jnode, top bool)
	write = func(n *jnode, top bool) {
		if b.Len() > limit {
			return
		}
		switch n.kind {
		case 's':
			s := n.val
			if !top && len(s) > 40 {
				s = s[:40] + "…"
			}
			b.WriteString(strconv.Quote(s))
		case 'o':
			if !top {
				b.WriteString("{…}")
				return
			}
			b.WriteString("{")
			for i, c := range n.kids {
				if i > 0 {
					b.WriteString(", ")
				}
				if b.Len() > limit {
					b.WriteString("…")
					break
				}
				b.WriteString(c.key + ": ")
				write(c, false)
			}
			b.WriteString("}")
		case 'a':
			if !top {
				b.WriteString(fmt.Sprintf("Array(%d)", len(n.kids)))
				return
			}
			b.WriteString(fmt.Sprintf("(%d) [", len(n.kids)))
			for i, c := range n.kids {
				if i > 0 {
					b.WriteString(", ")
				}
				if b.Len() > limit {
					b.WriteString("…")
					break
				}
				write(c, false)
			}
			b.WriteString("]")
		default:
			b.WriteString(n.val)
		}
	}
	write(n, true)
	return b.String()
}

// path returns the property path of n, like $.data.items[2].name.
func (n *jnode) path() string {
	var parts []string
	for x := n; x != nil && x.parent != nil; x = x.parent {
		if x.index {
			parts = append(parts, "["+x.key+"]")
		} else if isIdent(x.key) {
			parts = append(parts, "."+x.key)
		} else {
			parts = append(parts, "["+strconv.Quote(x.key)+"]")
		}
	}
	var b strings.Builder
	b.WriteString("$")
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteString(parts[i])
	}
	return b.String()
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// marshal renders n back to indented JSON.
func (n *jnode) marshal() string {
	var b strings.Builder
	var write func(n *jnode, ind string)
	write = func(n *jnode, ind string) {
		switch n.kind {
		case 'o', 'a':
			open, close := "{", "}"
			if n.kind == 'a' {
				open, close = "[", "]"
			}
			if len(n.kids) == 0 {
				b.WriteString(open + close)
				return
			}
			b.WriteString(open + "\n")
			for i, c := range n.kids {
				b.WriteString(ind + "  ")
				if n.kind == 'o' {
					b.WriteString(strconv.Quote(c.key) + ": ")
				}
				write(c, ind+"  ")
				if i < len(n.kids)-1 {
					b.WriteString(",")
				}
				b.WriteString("\n")
			}
			b.WriteString(ind + close)
		case 's':
			q, _ := json.Marshal(n.val)
			b.Write(q)
		default:
			b.WriteString(n.val)
		}
	}
	write(n, "")
	return b.String()
}

// jsonTree is the JSON preview: a tree that opens and closes.
type jsonTree struct {
	a     *App
	root  *jnode
	ok    bool
	rows  []*jnode
	dirty bool
	list  widget.List
	tag   struct{}
	hover int
}

func newJSONTree(a *App) *jsonTree {
	t := &jsonTree{a: a, hover: -1}
	t.list.Axis = layout.Vertical
	return t
}

func (t *jsonTree) set(data []byte) {
	t.setRoot(prepareTree(data))
}

// setRoot shows a tree made by prepareTree.
func (t *jsonTree) setRoot(root *jnode, ok bool) {
	t.root, t.ok = root, ok
	t.list.Position = layout.Position{}
	t.dirty = true
}

// prepareTree parses JSON into a tree, opened as far as is useful. It
// does not touch the UI, so it can run in the background.
func prepareTree(data []byte) (*jnode, bool) {
	root, ok := parseJSON(data)
	if !ok {
		return root, false
	}
	root.open = true
	// Small documents open fully.
	n := 0
	var walk func(x *jnode)
	walk = func(x *jnode) {
		n++
		for _, c := range x.kids {
			walk(c)
		}
	}
	walk(root)
	if n < 60 {
		var open func(x *jnode)
		open = func(x *jnode) {
			x.open = true
			for _, c := range x.kids {
				open(c)
			}
		}
		open(root)
	} else {
		for _, c := range root.kids {
			if len(root.kids) < 30 && len(c.kids) < 30 {
				c.open = false
			}
		}
	}
	return root, true
}

func (t *jsonTree) flatten() {
	t.rows = t.rows[:0]
	var walk func(x *jnode)
	walk = func(x *jnode) {
		t.rows = append(t.rows, x)
		if x.open {
			for _, c := range x.kids {
				walk(c)
			}
		}
	}
	walk(t.root)
	t.dirty = false
}

func (t *jsonTree) Layout(gtx layout.Context) layout.Dimensions {
	th := t.a.th
	if t.dirty || t.rows == nil {
		t.flatten()
	}
	rh := gtx.Dp(20)
	size := gtx.Constraints.Max
	padTop := gtx.Dp(6)
	for {
		e, ok := gtx.Event(pointer.Filter{Target: &t.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		y := int(pe.Position.Y) - padTop
		idx := t.list.Position.First + (y+t.list.Position.Offset)/rh
		if y < 0 || idx < 0 || idx >= len(t.rows) {
			idx = -1
		}
		switch pe.Kind {
		case pointer.Leave:
			t.hover = -1
		case pointer.Move:
			t.hover = idx
		case pointer.Press:
			if idx < 0 {
				continue
			}
			n := t.rows[idx]
			if pe.Buttons.Contain(pointer.ButtonSecondary) {
				t.menu(n)
			} else if n.kind == 'o' || n.kind == 'a' {
				n.open = !n.open
				if pe.Modifiers.Contain(key.ModAlt) { // Alt opens the whole subtree
					var set func(x *jnode)
					set = func(x *jnode) {
						x.open = n.open
						for _, c := range x.kids {
							set(c)
						}
					}
					set(n)
				}
				t.dirty = true
			}
		}
	}
	if t.dirty {
		t.flatten()
	}
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &t.tag)
	d := layout.Inset{Top: unit.Dp(float32(padTop) / gtx.Metric.PxPerDp)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return th.list(gtx, &t.list, len(t.rows), func(gtx layout.Context, i int) layout.Dimensions {
			return t.row(gtx, i, rh)
		})
	})
	area.Pop()
	return d
}

func (t *jsonTree) row(gtx layout.Context, i, rh int) layout.Dimensions {
	th := t.a.th
	n := t.rows[i]
	w := gtx.Constraints.Max.X
	if t.hover == i {
		fill(gtx.Ops, image.Rect(0, 0, w, rh), th.Bg2)
	}
	x := gtx.Dp(8) + n.depth*gtx.Dp(16)
	if n.kind == 'o' || n.kind == 'a' {
		ic := icChevron
		if n.open {
			ic = icExpand
		}
		st := op.Offset(image.Pt(x, (rh-gtx.Dp(14))/2)).Push(gtx.Ops)
		drawIcon(gtx, ic, 14, th.Text3)
		st.Pop()
	}
	x += gtx.Dp(16)
	put := func(s string, c color.NRGBA, flex bool) {
		if x >= w || s == "" {
			return
		}
		cg := gtx
		cg.Constraints = layout.Constraints{Max: image.Pt(w-x-gtx.Dp(8), rh)}
		rec := op.Record(gtx.Ops)
		d := Label{Text: s, Size: 12, Color: c, Mono: true}.Layout(cg, th)
		call := rec.Stop()
		st := op.Offset(image.Pt(x, (rh-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		x += d.Size.X
	}
	if n.parent != nil {
		kc := th.SynKey
		if n.index {
			kc = th.Text3
		}
		put(n.key, kc, false)
		put(": ", th.Text2, false)
	}
	switch n.kind {
	case 'o', 'a':
		put(n.summary(160), th.Text2, true)
	case 's':
		v := n.val
		if len(v) > 2000 {
			v = v[:2000] + "…"
		}
		v = strings.ReplaceAll(v, "\n", "↵")
		put(strconv.Quote(v), th.SynString, true)
	case 'n':
		put(n.val, th.SynNumber, true)
	default:
		put(n.val, th.SynKeyword, true)
	}
	return layout.Dimensions{Size: image.Pt(w, rh)}
}

func (t *jsonTree) menu(n *jnode) {
	a := t.a
	val := n.val
	if n.kind == 'o' || n.kind == 'a' {
		val = n.marshal()
	}
	a.Menu(
		MenuItem{Label: "复制值", Icon: icCopy, Do: func() { a.copyText(val, "值") }},
		MenuItem{Label: "复制属性路径", Do: func() { a.copyText(n.path(), "属性路径") }},
		MenuItem{Label: "复制为 JSON", Do: func() { a.copyText(n.marshal(), " JSON") }},
		MenuItem{Sep: true},
		MenuItem{Label: "展开全部", Do: func() {
			var set func(x *jnode)
			set = func(x *jnode) {
				x.open = true
				for _, c := range x.kids {
					set(c)
				}
			}
			set(n)
			t.dirty = true
		}},
		MenuItem{Label: "折叠全部", Do: func() {
			var set func(x *jnode)
			set = func(x *jnode) {
				x.open = false
				for _, c := range x.kids {
					set(c)
				}
			}
			set(n)
			if n == t.root {
				n.open = true
			}
			t.dirty = true
		}},
	)
}
