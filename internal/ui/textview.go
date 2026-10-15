package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/config"
)

// editorLimit is the largest text shown in a selectable editor; longer
// text goes to the virtualized viewer, which draws only visible lines.
const editorLimit = 256 << 10

// textView shows read-only text: in an editor (selectable) when small, as
// a virtualized list of lines when large.
type textView struct {
	a    *App
	key  string
	text string
	big  bool
	ed   widget.Editor
	rc   rightClick

	list widget.List
	// lines are the logical lines; vis the visual lines after wrapping,
	// cached for visW.
	lines   []string
	vis     []string
	visW    int
	visWrap bool
}

func newTextView(a *App) *textView {
	t := &textView{a: a}
	t.ed.ReadOnly = true
	t.list.Axis = layout.Vertical
	return t
}

// set replaces the text when key differs from the last call's.
func (t *textView) set(text, key string) {
	if key == t.key {
		return
	}
	t.key, t.text = key, text
	t.big = len(text) > editorLimit
	t.vis, t.visW = nil, 0
	if t.big {
		t.lines = strings.Split(text, "\n")
		t.ed.SetText("")
	} else {
		t.lines = nil
		t.ed.SetText(text)
	}
	t.list.Position = layout.Position{}
}

// cells is the display width of s in monospace cells.
func cellWidth(r rune) int {
	if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe30 && r <= 0xfe4f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x20000) {
		return 2
	}
	return 1
}

func (t *textView) wrapLines(cols int, wrap bool) {
	if t.vis != nil && t.visW == cols && t.visWrap == wrap {
		return
	}
	t.visW, t.visWrap = cols, wrap
	t.vis = t.vis[:0]
	for _, l := range t.lines {
		l = strings.TrimSuffix(l, "\r")
		if !wrap || len(l) <= cols {
			if !wrap && len(l) > 4096 {
				l = l[:4096]
			}
			t.vis = append(t.vis, l)
			continue
		}
		w, start := 0, 0
		for i, r := range l {
			cw := cellWidth(r)
			if w+cw > cols {
				t.vis = append(t.vis, l[start:i])
				start, w = i, 0
			}
			w += cw
		}
		t.vis = append(t.vis, l[start:])
	}
}

func (t *textView) Layout(gtx layout.Context, wrap bool) layout.Dimensions {
	th := t.a.th
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	if !t.big {
		d := layout.Inset{Left: 12, Right: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			es := editorStyle{th: th, e: &t.ed, size: 12, mono: true}
			return es.Layout(gtx)
		})
		editMenuArea(gtx, th, &t.rc, &t.ed, image.Rectangle{Max: size})
		return d
	}
	// Measure a monospace cell.
	rec := op.Record(gtx.Ops)
	mg := gtx
	mg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(400), gtx.Dp(40))}
	cd := th.mono(mg, "MMMMMMMMMM", 12, th.Text)
	rec.Stop()
	cw := max(cd.Size.X/10, 1)
	lh := max(cd.Size.Y, 1)
	cols := max((size.X-gtx.Dp(20))/cw, 20)
	t.wrapLines(cols, wrap)
	return layout.Inset{Left: 12, Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return th.list(gtx, &t.list, len(t.vis), func(gtx layout.Context, i int) layout.Dimensions {
			gtx.Constraints = layout.Constraints{Min: image.Pt(0, lh), Max: image.Pt(gtx.Constraints.Max.X, lh)}
			return th.mono(gtx, t.vis[i], 12, th.Text)
		})
	})
}

// ---- Hex --------------------------------------------------------------

type hexView struct {
	a    *App
	data []byte
	list widget.List
}

func (h *hexView) Layout(gtx layout.Context) layout.Dimensions {
	th := h.a.th
	h.list.Axis = layout.Vertical
	n := (len(h.data) + 15) / 16
	return layout.Inset{Left: 12, Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return th.list(gtx, &h.list, n, func(gtx layout.Context, i int) layout.Dimensions {
			row := h.data[i*16 : min(i*16+16, len(h.data))]
			var b strings.Builder
			fmt.Fprintf(&b, "%08x  ", i*16)
			for j := 0; j < 16; j++ {
				if j < len(row) {
					fmt.Fprintf(&b, "%02x ", row[j])
				} else {
					b.WriteString("   ")
				}
				if j == 7 {
					b.WriteByte(' ')
				}
			}
			b.WriteString(" ")
			for _, c := range row {
				if c >= 0x20 && c < 0x7f {
					b.WriteByte(c)
				} else {
					b.WriteByte('.')
				}
			}
			return th.mono(gtx, b.String(), 12, th.Text2)
		})
	})
}

// ---- Body viewer --------------------------------------------------------

type bodyMode int

const (
	modeText bodyMode = iota
	modeHex
)

// bodyView shows a request or response body with a small toolbar: pretty
// printing, hex view, copying and saving. In preview mode it shows images
// and a JSON tree instead.
type bodyView struct {
	a           *App
	resp        bool
	previewMode bool

	key       string
	data      []byte
	text      string
	isText    bool
	isJSON    bool
	decodeErr error
	pretty    bool
	// prettyText is the text indented, for JSON.
	prettyText string
	prep       lazy[bodyData]
	mode       bodyMode

	tv   *textView
	hex  *hexView
	tree *jsonTree
	img  *imageBox

	prettyClk, hexClk, copyClk, saveClk, wrapClk widget.Clickable
}

func newBodyView(a *App, resp bool) *bodyView {
	b := &bodyView{a: a, resp: resp, tv: newTextView(a), hex: &hexView{a: a}, tree: newJSONTree(a), pretty: true}
	return b
}

// bodyData is a body made ready for showing.
type bodyData struct {
	data      []byte
	text      string
	pretty    string // the text indented, for JSON
	isText    bool
	isJSON    bool
	decodeErr error
	img       *imageBox
	tree      *jnode
	treeOK    bool
}

// prepareBody decodes and formats a body. It only reads s, so it can run
// in the background.
func prepareBody(s *capture.Session, resp, preview bool) bodyData {
	var b bodyData
	h := s.ReqHeader
	if resp {
		h = s.RespHeader
		b.data, b.decodeErr = capture.ResponseBody(s)
	} else {
		b.data, b.decodeErr = capture.RequestBody(s)
	}
	mt := mediaTypeOf(h)
	b.isText = capture.IsTextual(mt, b.data)
	if b.isText {
		b.text = string(capture.ToUTF8(b.data, capture.Charset(h.Get("Content-Type"))))
		t := strings.TrimSpace(b.text)
		b.isJSON = strings.Contains(mt, "json") || (len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)))
		if !utf8.ValidString(b.text) {
			b.text = strings.ToValidUTF8(b.text, "�")
		}
	}
	switch {
	case strings.HasPrefix(mt, "image/") && mt != "image/svg+xml":
		b.img = newImageBox(b.data)
	case b.isJSON && preview:
		b.tree, b.treeOK = prepareTree([]byte(b.text))
	case b.isJSON:
		if p, ok := prettyJSON([]byte(b.text)); ok {
			b.pretty = p
		}
	}
	return b
}

// load prepares the body of s, and reports whether it is ready.
func (b *bodyView) load(s *capture.Session) bool {
	raw := s.ReqBody
	if b.resp {
		raw = s.RespBody
	}
	key := fmt.Sprintf("%d-%d-%p-%v", s.ID, len(raw), raw, s.BodyDecoded)
	d, ok := b.prep.get(b.a, key, len(raw) > bigWork, func() bodyData { return prepareBody(s, b.resp, b.previewMode) })
	if !ok || key == b.key {
		return ok
	}
	b.key = key
	b.data, b.text, b.prettyText, b.isText, b.isJSON, b.decodeErr = d.data, d.text, d.pretty, d.isText, d.isJSON, d.decodeErr
	b.hex.data = b.data
	b.hex.list.Position = layout.Position{}
	b.tv.key = ""
	b.img = d.img
	if b.previewMode && b.isJSON {
		b.tree.setRoot(d.tree, d.treeOK)
	}
	return true
}

func (b *bodyView) Layout(gtx layout.Context, s *capture.Session) layout.Dimensions {
	a := b.a
	th := a.th
	ready := b.load(s)
	if b.prettyClk.Clicked(gtx) {
		b.pretty = !b.pretty
		b.tv.key = ""
	}
	if b.hexClk.Clicked(gtx) {
		if b.mode == modeHex {
			b.mode = modeText
		} else {
			b.mode = modeHex
		}
	}
	if b.wrapClk.Clicked(gtx) {
		a.updateSettings(func(st *config.Settings) { st.WrapText = !st.WrapText })
	}
	if b.copyClk.Clicked(gtx) {
		if b.isText {
			a.copyText(b.text, "正文")
		} else {
			a.Toast(toastInfo, "正文不是文本")
		}
	}
	if b.saveClk.Clicked(gtx) {
		b.save(s)
	}
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()

	bodyLen := len(s.ReqBody)
	if b.resp {
		bodyLen = len(s.RespBody)
	}
	if bodyLen == 0 {
		msg := "无响应数据"
		switch {
		case !b.resp:
			msg = "无请求正文"
		case s.Running():
			msg = "正在等待响应…"
		case s.Failed():
			msg = "请求失败：" + s.Error
		}
		return b.message(gtx, msg)
	}
	if !ready {
		return th.loading(gtx)
	}

	toolbar := func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(30)
		w := gtx.Constraints.Max.X
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			info := fmtBytes(int64(len(b.data)))
			trunc := s.ReqTruncated
			if b.resp {
				trunc = s.RespTruncated
			}
			if trunc {
				info += " · 已截断"
			}
			if ce := s.RespHeader.Get("Content-Encoding"); b.resp && ce != "" && !s.BodyDecoded {
				info += " · 已解码 " + ce
			}
			children := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.txt(gtx, info, 11, th.Text3) }),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 1)}
				}),
			}
			btn := func(clk *widget.Clickable, ic *Icon, on bool, title string) layout.FlexChild {
				return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return th.iconButton(gtx, clk, ic, 26, 16, th.Text2, on, title)
				})
			}
			if !b.previewMode && b.isJSON {
				children = append(children, btn(&b.prettyClk, icFormat, b.pretty, "格式化"))
			}
			if !b.previewMode {
				children = append(children, btn(&b.wrapClk, icWrap, a.set.WrapText, "自动换行"))
				children = append(children, btn(&b.hexClk, icCode, b.mode == modeHex, "十六进制"))
			}
			children = append(children, btn(&b.copyClk, icCopy, false, "复制"), btn(&b.saveClk, icSave, false, "另存为"))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
		})
		fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), w, h), th.Border)
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(toolbar),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if b.decodeErr == nil {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: 12, Top: 6, Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, "解码失败："+b.decodeErr.Error(), 12, th.Danger)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			switch {
			case b.previewMode && b.img != nil:
				return b.img.Layout(gtx, th)
			case b.previewMode && b.isJSON && b.tree.ok:
				return b.tree.Layout(gtx)
			case b.previewMode && !b.isText:
				return b.message(gtx, "无法预览")
			case b.mode == modeHex || !b.isText:
				return b.hex.Layout(gtx)
			}
			text := b.text
			key := b.key + fmt.Sprint(b.pretty)
			if b.isJSON && b.pretty && !b.previewMode && b.prettyText != "" {
				text = b.prettyText
			}
			b.tv.set(text, key)
			return b.tv.Layout(gtx, a.set.WrapText)
		}),
	)
}

func orDash(s string) string {
	if s == "" {
		return "未知类型"
	}
	return s
}

func (b *bodyView) message(gtx layout.Context, msg string) layout.Dimensions {
	th := b.a.th
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(420))
		return Label{Text: msg, Size: 12, Color: th.Text3, MaxLines: 4}.Layout(gtx, th)
	})
}

func (b *bodyView) save(s *capture.Session) {
	a := b.a
	data := b.data
	name := path.Base(s.ParsedURL().Path)
	if name == "" || name == "/" || name == "." {
		name = "body"
	}
	if path.Ext(name) == "" {
		if exts := extFor(s.ContentType()); exts != "" {
			name += exts
		}
	}
	a.choosePath(true, "保存正文", name, "", nil, func(p string) {
		err := os.WriteFile(p, data, 0o644)
		a.Post(func() {
			if err != nil {
				a.Toast(toastError, "保存失败："+err.Error())
			} else {
				a.Toast(toastOK, "已保存")
			}
		})
	})
}

func extFor(mt string) string {
	switch {
	case strings.Contains(mt, "json"):
		return ".json"
	case mt == "text/html":
		return ".html"
	case strings.Contains(mt, "javascript"):
		return ".js"
	case mt == "text/css":
		return ".css"
	case mt == "image/png":
		return ".png"
	case mt == "image/jpeg":
		return ".jpg"
	case mt == "image/gif":
		return ".gif"
	case mt == "image/webp":
		return ".webp"
	case mt == "image/svg+xml":
		return ".svg"
	case strings.Contains(mt, "xml"):
		return ".xml"
	case strings.HasPrefix(mt, "text/"):
		return ".txt"
	}
	return ""
}

// ---- Images -------------------------------------------------------------

type imageBox struct {
	img    image.Image
	format string
	err    error
	op     paint.ImageOp
	ready  bool
	size   int
}

func newImageBox(data []byte) *imageBox {
	b := &imageBox{size: len(data)}
	img, format, err := image.Decode(bytes.NewReader(data))
	b.img, b.format, b.err = img, format, err
	return b
}

func (b *imageBox) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	if b.err != nil {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return th.txt(gtx, "无法解码图片："+b.err.Error(), 12, th.Text3)
		})
	}
	if !b.ready {
		b.op = paint.NewImageOp(b.img)
		b.ready = true
	}
	bounds := b.img.Bounds()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(16).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					// A checkerboard shows transparency.
					img := widget.Image{Src: b.op, Fit: widget.ScaleDown, Scale: 1 / gtx.Metric.PxPerDp}
					rec := op.Record(gtx.Ops)
					d := img.Layout(gtx)
					call := rec.Stop()
					checker(gtx, d.Size, th)
					call.Add(gtx.Ops)
					return d
				})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.txt(gtx, fmt.Sprintf("%d × %d · %s · %s", bounds.Dx(), bounds.Dy(), strings.ToUpper(b.format), fmtBytes(int64(b.size))), 12, th.Text2)
				})
			})
		}),
	)
}

func checker(gtx layout.Context, size image.Point, th *Theme) {
	s := gtx.Dp(8)
	a, b := th.Bg2, th.Bg3
	fill(gtx.Ops, image.Rectangle{Max: size}, a)
	for y := 0; y < size.Y; y += s {
		for x := (y / s % 2) * s; x < size.X; x += 2 * s {
			fill(gtx.Ops, image.Rect(x, y, min(x+s, size.X), min(y+s, size.Y)), b)
		}
	}
}

var _ = unit.Dp(0)
var _ = font.Normal
