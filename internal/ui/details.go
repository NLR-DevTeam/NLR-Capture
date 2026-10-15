package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"mime"
	"mime/multipart"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/proxy"
)

// Detail tabs.
const (
	tabHeaders  = "headers"
	tabPayload  = "payload"
	tabPreview  = "preview"
	tabResponse = "response"
	tabTiming   = "timing"
	tabCookies  = "cookies"
	tabMessages = "messages"
	tabTLS      = "tls"
	tabRaw      = "raw"
	tabBreak    = "break"
)

var tabTitles = map[string]string{
	tabHeaders: "标头", tabPayload: "载荷", tabPreview: "预览", tabResponse: "响应",
	tabTiming: "时间", tabCookies: "Cookie", tabMessages: "消息", tabTLS: "证书",
	tabRaw: "原始", tabBreak: "断点",
}

// detailView shows one session, in tabs like the DevTools request view.
type detailView struct {
	a      *App
	tab    string
	tabClk map[string]*widget.Clickable
	close  widget.Clickable

	id   int64
	sess *capture.Session

	scroll  widget.List
	sels    []*widget.Selectable
	selN    int
	secOpen map[string]bool
	secClk  map[string]*widget.Clickable
	rawClk  map[string]*widget.Clickable
	rawOn   map[string]bool

	resp        *bodyView
	payKey      string
	payTree     *jnode
	payClk      map[*jnode]*widget.Clickable
	preview     *bodyView
	rawReq      *textView
	rawResp     *textView
	rawReqZ     lazy[string]
	rawRespZ    lazy[string]
	payBody     lazy[payBody]
	rawDecode   widget.Bool
	rawSplit    int
	rawSplitter Splitter

	ws *wsView

	brkEd                    widget.Editor
	brkKey                   string
	brkGo, brkAsIs, brkAbort widget.Clickable
	brkRC                    rightClick

	// shown is false until the pane has been drawn since it opened. The
	// first frame after opening it or picking another request shows only
	// a spinner, so the click is answered at once while the content is
	// laid out.
	shown bool
}

func newDetailView(a *App) *detailView {
	d := &detailView{
		a: a, tab: tabHeaders, tabClk: map[string]*widget.Clickable{},
		secOpen: map[string]bool{}, secClk: map[string]*widget.Clickable{},
		rawClk: map[string]*widget.Clickable{}, rawOn: map[string]bool{},
		resp: newBodyView(a, true), preview: newBodyView(a, true),
		rawReq: newTextView(a), rawResp: newTextView(a), ws: newWSView(a),
	}
	d.preview.previewMode = true
	d.rawDecode.Value = true
	d.scroll.Axis = layout.Vertical
	d.brkEd.WrapPolicy = 0
	return d
}

func (d *detailView) reset() {
	d.id, d.sess = 0, nil
	d.shown = false
}

func (d *detailView) showBreak() { d.tab = tabBreak }

func clickable(m map[string]*widget.Clickable, k string) *widget.Clickable {
	c, ok := m[k]
	if !ok {
		c = new(widget.Clickable)
		m[k] = c
	}
	return c
}

// tabs returns the tabs that apply to s.
func (d *detailView) tabs(s *capture.Session) []string {
	var t []string
	if s.State == capture.StatePaused {
		t = append(t, tabBreak)
	}
	t = append(t, tabHeaders)
	if s.Kind == capture.KindTunnel {
		if s.TLS != nil {
			t = append(t, tabTLS)
		}
		return append(t, tabTiming)
	}
	if s.ParsedURL().RawQuery != "" || len(s.ReqBody) > 0 {
		t = append(t, tabPayload)
	}
	if s.Kind == capture.KindWebSocket {
		t = append(t, tabMessages)
	} else {
		t = append(t, tabPreview, tabResponse)
	}
	if s.ReqHeader.Has("Cookie") || s.RespHeader.Has("Set-Cookie") {
		t = append(t, tabCookies)
	}
	t = append(t, tabTiming)
	if s.TLS != nil && s.TLS.Subject != "" {
		t = append(t, tabTLS)
	}
	return append(t, tabRaw)
}

// sel returns the next selectable label slot for this frame.
func (d *detailView) sel() *widget.Selectable {
	if d.selN >= len(d.sels) {
		d.sels = append(d.sels, new(widget.Selectable))
	}
	s := d.sels[d.selN]
	d.selN++
	return s
}

func (d *detailView) Layout(gtx layout.Context, s *capture.Session) layout.Dimensions {
	a := d.a
	th := a.th
	if d.close.Clicked(gtx) {
		a.list.clearSelection()
	}
	switched := s.ID != d.id
	if switched {
		// Another session: keep the tab if it applies.
		d.id = s.ID
		d.scroll.Position = layout.Position{}
		for _, x := range d.sels {
			x.SetText("")
		}
		if s.State == capture.StatePaused {
			d.tab = tabBreak
		}
	}
	spin := !d.shown || switched
	d.shown = true
	d.sess = s
	d.selN = 0
	tabs := d.tabs(s)
	if !containsStr(tabs, d.tab) {
		d.tab = tabHeaders
	}
	for _, t := range tabs {
		if clickable(d.tabClk, t).Clicked(gtx) {
			d.tab = t
			d.scroll.Position = layout.Position{}
		}
	}
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	fill(gtx.Ops, image.Rectangle{Max: size}, th.Bg0)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d.tabBar(gtx, tabs) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if spin {
				return th.loading(gtx)
			}
			return d.content(gtx, s)
		}),
	)
}

// content draws the current tab.
func (d *detailView) content(gtx layout.Context, s *capture.Session) layout.Dimensions {
	switch d.tab {
	case tabBreak:
		return d.breakTab(gtx, s)
	case tabPayload:
		return d.payloadTab(gtx, s)
	case tabPreview:
		return d.preview.Layout(gtx, s)
	case tabResponse:
		return d.resp.Layout(gtx, s)
	case tabTiming:
		return d.scrolled(gtx, d.timingRows(s))
	case tabCookies:
		return d.scrolled(gtx, d.cookieRows(s))
	case tabMessages:
		return d.ws.Layout(gtx, s)
	case tabTLS:
		return d.scrolled(gtx, d.tlsRows(s))
	case tabRaw:
		return d.rawTab(gtx, s)
	}
	return d.scrolled(gtx, d.headerRows(s))
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (d *detailView) tabBar(gtx layout.Context, tabs []string) layout.Dimensions {
	th := d.a.th
	h := gtx.Dp(30)
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	fill(gtx.Ops, image.Rect(0, 0, w, h), th.Bg1)
	layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 4, Right: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.iconButton(gtx, &d.close, icClose, 24, 15, th.Text2, false, "关闭  Esc")
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, 0, len(tabs))
			for _, t := range tabs {
				t := t
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					clk := clickable(d.tabClk, t)
					return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min = image.Point{}
						m := op.Record(gtx.Ops)
						dd := layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							c, wt := th.Text2, font.Normal
							if d.tab == t {
								c, wt = th.Text, font.Medium
							}
							if t == tabBreak {
								c = th.Warn
							}
							return th.txtW(gtx, tabTitles[t], 12, c, wt)
						})
						call := m.Stop()
						r := image.Rectangle{Max: image.Pt(dd.Size.X, h)}
						if clk.Hovered() && d.tab != t {
							fill(gtx.Ops, r, th.Bg3)
						}
						st := op.Offset(image.Pt(0, (h-dd.Size.Y)/2)).Push(gtx.Ops)
						call.Add(gtx.Ops)
						st.Pop()
						if d.tab == t {
							fill(gtx.Ops, image.Rect(0, h-gtx.Dp(2), dd.Size.X, h), th.Accent)
						}
						return layout.Dimensions{Size: r.Max}
					})
				}))
			}
			return layout.Flex{}.Layout(gtx, children...)
		}),
	)
	fill(gtx.Ops, image.Rect(0, h-max(gtx.Dp(1), 1), w, h), th.Border)
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// scrolled lays out rows in a scrolling column with padding.
func (d *detailView) scrolled(gtx layout.Context, rows []layout.Widget) layout.Dimensions {
	th := d.a.th
	return th.list(gtx, &d.scroll, len(rows)+1, func(gtx layout.Context, i int) layout.Dimensions {
		if i == len(rows) {
			return layout.Spacer{Height: 16}.Layout(gtx)
		}
		return layout.Inset{Left: 12, Right: 12}.Layout(gtx, rows[i])
	})
}

// section returns a collapsible section title row; the rows below it are
// only added while it is open.
func (d *detailView) section(key, title string, extra layout.Widget) (layout.Widget, bool) {
	th := d.a.th
	open, ok := d.secOpen[key]
	if !ok {
		open = true
		d.secOpen[key] = true
	}
	clk := clickable(d.secClk, key)
	w := func(gtx layout.Context) layout.Dimensions {
		if clk.Clicked(gtx) {
			d.secOpen[key] = !d.secOpen[key]
		}
		return layout.Inset{Top: 8, Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								ic := icChevron
								if d.secOpen[key] {
									ic = icExpand
								}
								return drawIcon(gtx, ic, 16, th.Text2)
							}),
							hspace(4),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return th.txtW(gtx, title, 12, th.Text, font.SemiBold)
							}),
						)
					})
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 1)}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if extra == nil {
						return layout.Dimensions{}
					}
					return extra(gtx)
				}),
			)
		})
	}
	return w, open
}

// kv draws a name: value row with a selectable value.
func (d *detailView) kv(name, value string, nameW unit.Dp, vc color.NRGBA, mono bool) layout.Widget {
	th := d.a.th
	sel := d.sel()
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 3, Bottom: 3, Left: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					w := gtx.Dp(nameW)
					gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Dp(200))}
					return Label{Text: name, Size: 12, Color: th.Text2, Weight: font.Medium, MaxLines: 3}.Layout(gtx, th)
				}),
				hspace(8),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return selLabel(gtx, th, sel, value, 12, vc, mono)
				}),
			)
		})
	}
}

// selLabel draws text the user can select and copy.
func selLabel(gtx layout.Context, th *Theme, sel *widget.Selectable, text string, size unit.Sp, c color.NRGBA, mono bool) layout.Dimensions {
	if sel.Text() != text {
		sel.SetText(text)
	}
	f := font.Font{Typeface: th.Face}
	if mono {
		f.Typeface = th.Mono
	}
	gtx.Constraints.Min.X = 0
	return sel.Layout(gtx, th.Shaper, f, size, colorMat(gtx, c), colorMat(gtx, th.Select))
}

// headerRowsOf draws header fields, or their source text.
func (d *detailView) fieldRows(key string, h capture.Header) []layout.Widget {
	th := d.a.th
	if d.rawOn[key] {
		text := capture.HeaderText(h)
		sel := d.sel()
		return []layout.Widget{func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 20, Top: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return selLabel(gtx, th, sel, strings.TrimRight(text, "\n"), 12, th.Text, true)
			})
		}}
	}
	nameW := unit.Dp(160)
	var rows []layout.Widget
	for _, f := range h {
		rows = append(rows, d.kv(f.Name, f.Value, nameW, th.Text, false))
	}
	if len(h) == 0 {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 20, Top: 3, Bottom: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, "无", 12, th.Text3)
			})
		})
	}
	return rows
}

func (d *detailView) rawToggle(key string) layout.Widget {
	th := d.a.th
	clk := clickable(d.rawClk, key)
	return func(gtx layout.Context) layout.Dimensions {
		if clk.Clicked(gtx) {
			d.rawOn[key] = !d.rawOn[key]
		}
		label := "查看源代码"
		if d.rawOn[key] {
			label = "查看解析结果"
		}
		return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			c := th.AccentText
			if clk.Hovered() {
				c = th.Text
			}
			return th.txt(gtx, label, 12, c)
		})
	}
}

func statusColor(th *Theme, s *capture.Session) color.NRGBA {
	switch {
	case s.Failed() || s.Status >= 500:
		return th.Danger
	case s.Status >= 400:
		return th.Orange
	case s.Status >= 300:
		return th.Warn
	case s.Status >= 200:
		return th.Green
	}
	return th.Text3
}

func (d *detailView) headerRows(s *capture.Session) []layout.Widget {
	th := d.a.th
	var rows []layout.Widget
	title, open := d.section("general", "常规", nil)
	rows = append(rows, title)
	if open {
		nw := unit.Dp(120)
		u := s.URL
		rows = append(rows, d.kv("请求网址", u, nw, th.Text, false))
		rows = append(rows, d.kv("请求方法", s.Method, nw, th.Text, false))
		if s.Kind != capture.KindTunnel {
			st := statusText(s)
			if s.Status > 0 {
				reason := s.Reason
				if reason == "" {
					reason = capture.StatusText(s.Status)
				}
				st = strconv.Itoa(s.Status) + " " + reason
			}
			sel := d.sel()
			sc := statusColor(th, s)
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 3, Bottom: 3, Left: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							w := gtx.Dp(nw)
							gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Dp(40))}
							return th.txtW(gtx, "状态代码", 12, th.Text2, font.Medium)
						}),
						hspace(8),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							dd := dot(gtx, gtx.Dp(8), sc)
							dd.Size.X += gtx.Dp(6)
							return dd
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return selLabel(gtx, th, sel, st, 12, th.Text, false)
						}),
					)
				})
			})
		}
		if s.ServerAddr != "" {
			rows = append(rows, d.kv("远程地址", s.ServerAddr, nw, th.Text, false))
		}
		if p := protoLabel(s); p != "" {
			rows = append(rows, d.kv("协议", p, nw, th.Text, false))
		}
		if s.Process != "" || s.PID > 0 {
			p := s.Process
			if s.PID > 0 {
				p += fmt.Sprintf(" · PID %d", s.PID)
			}
			rows = append(rows, d.kv("进程", p, nw, th.Text, false))
		}
		if s.ClientAddr != "" {
			src := map[string]string{capture.SourceProxy: "代理", capture.SourceTUN: "虚拟网卡", capture.SourceImport: "导入", capture.SourceCompose: "构造"}[s.Source]
			rows = append(rows, d.kv("客户端", s.ClientAddr+" · "+src, nw, th.Text, false))
		}
		if s.Scheme == "https" || s.Scheme == "wss" || s.TLS != nil {
			tl := "未解密"
			if s.Decrypted {
				tl = "已解密"
			}
			if s.TLS != nil && s.TLS.Version != "" {
				tl += " · " + s.TLS.Version + " · " + s.TLS.Cipher
			}
			rows = append(rows, d.kv("HTTPS", tl, nw, th.Text, false))
		}
		rows = append(rows, d.kv("开始时间", s.Started.Format("2006-01-02 15:04:05.000"), nw, th.Text, false))
		if s.Kind == capture.KindTunnel {
			rows = append(rows, d.kv("流量", "上行 "+fmtBytes(s.BytesUp)+" · 下行 "+fmtBytes(s.BytesDown), nw, th.Text, false))
		}
		if s.Rule != "" {
			rows = append(rows, d.kv("规则", s.Rule, nw, th.AccentText, false))
		}
		if s.Error != "" {
			rows = append(rows, d.kv("错误", s.Error, nw, th.Danger, false))
		}
		if s.TLS != nil && s.TLS.ClientError != "" {
			rows = append(rows, d.kv("客户端错误", s.TLS.ClientError, nw, th.Danger, true))
		}
		if s.Comment != "" {
			rows = append(rows, d.kv("备注", s.Comment, nw, th.Text, false))
		}
	}
	if s.Kind == capture.KindTunnel && len(s.ReqHeader) == 0 {
		return rows
	}
	if s.Status > 0 || len(s.RespHeader) > 0 {
		title, open = d.section("resp", fmt.Sprintf("响应标头 · %d", len(s.RespHeader)), d.rawToggle("resp"))
		rows = append(rows, title)
		if open {
			rows = append(rows, d.fieldRows("resp", s.RespHeader)...)
		}
	}
	title, open = d.section("req", fmt.Sprintf("请求标头 · %d", len(s.ReqHeader)), d.rawToggle("req"))
	rows = append(rows, title)
	if open {
		rows = append(rows, d.fieldRows("req", s.ReqHeader)...)
	}
	return rows
}

func protoLabel(s *capture.Session) string {
	p := s.Proto
	if p == "h2" {
		return "HTTP/2"
	}
	if s.RespProto != "" {
		p = s.RespProto
	}
	return p
}

// ---- Payload ----------------------------------------------------------

// payloadPart is one section of the payload tab: its parsed rows and its
// source text. Parts without a parsed form only show the source.
type payloadPart struct {
	key, title string
	source     string
	parsed     func() []layout.Widget
}

// payloadParts splits a request into the sections of the payload tab, or
// reports false while its body is still being decoded.
func (d *detailView) payloadParts(s *capture.Session) ([]payloadPart, bool) {
	th := d.a.th
	var parts []payloadPart
	pairs := func(raw string) func() []layout.Widget {
		return func() []layout.Widget {
			var rows []layout.Widget
			for _, kv := range strings.Split(raw, "&") {
				if kv == "" {
					continue
				}
				k, v, _ := strings.Cut(kv, "=")
				k2, err := url.QueryUnescape(k)
				if err != nil {
					k2 = k
				}
				v2, err := url.QueryUnescape(v)
				if err != nil {
					v2 = v
				}
				rows = append(rows, d.kv(k2, v2, 160, th.Text, false))
			}
			return rows
		}
	}
	if q := s.ParsedURL().RawQuery; q != "" {
		parts = append(parts, payloadPart{key: "query", title: "查询字符串参数", source: q, parsed: pairs(q)})
	}
	if len(s.ReqBody) == 0 {
		return parts, true
	}
	key := fmt.Sprintf("%d-%d-%p", s.ID, len(s.ReqBody), s.ReqBody)
	pb, ok := d.payBody.get(d.a, key, len(s.ReqBody) > bigWork, func() payBody { return preparePayload(s) })
	if !ok {
		return parts, false
	}
	body, mt, text, isText := pb.body, s.ReqContentType(), pb.text, pb.isText
	switch {
	case mt == "application/x-www-form-urlencoded":
		parts = append(parts, payloadPart{key: "form", title: "表单数据", source: text, parsed: pairs(string(body))})
	case strings.HasPrefix(mt, "multipart/"):
		_, params, _ := mime.ParseMediaType(s.ReqHeader.Get("Content-Type"))
		src := text
		if !capture.LooksText(body) {
			src = "二进制 · " + fmtBytes(int64(len(body)))
		}
		parts = append(parts, payloadPart{key: "multipart", title: "表单数据", source: src, parsed: func() []layout.Widget {
			var rows []layout.Widget
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				var buf bytes.Buffer
				n, _ := buf.ReadFrom(p)
				val := buf.String()
				if p.FileName() != "" || !capture.LooksText(buf.Bytes()) {
					val = p.FileName() + " · " + p.Header.Get("Content-Type") + " · " + fmtBytes(n)
				} else if len(val) > 4096 {
					val = val[:4096] + "…"
				}
				rows = append(rows, d.kv(p.FormName(), val, 160, th.Text, false))
			}
			return rows
		}})
	default:
		part := payloadPart{key: "payload", title: "请求载荷", source: text}
		if isText {
			if d.payKey != key {
				d.payKey = key
				d.payTree = pb.tree
				d.payClk = map[*jnode]*widget.Clickable{}
			}
			if d.payTree != nil {
				part.parsed = d.treeRows
			}
		}
		parts = append(parts, part)
	}
	return parts, true
}

// payBody is a request body made ready for the payload tab.
type payBody struct {
	body   []byte
	text   string
	isText bool
	tree   *jnode
}

// preparePayload decodes a request body. It only reads s, so it can run
// in the background.
func preparePayload(s *capture.Session) payBody {
	body, err := capture.RequestBody(s)
	mt := s.ReqContentType()
	p := payBody{body: body, isText: capture.IsTextual(mt, body)}
	switch {
	case err != nil:
		p.text = "解码失败：" + err.Error()
	case p.isText:
		p.text = string(capture.ToUTF8(body, capture.Charset(s.ReqHeader.Get("Content-Type"))))
	default:
		p.text = "二进制 · " + fmtBytes(int64(len(body)))
	}
	if p.isText && mt != "application/x-www-form-urlencoded" && !strings.HasPrefix(mt, "multipart/") {
		if t, ok := parseJSON([]byte(p.text)); ok {
			t.open = true
			p.tree = t
		}
	}
	return p
}

func (d *detailView) payloadTab(gtx layout.Context, s *capture.Session) layout.Dimensions {
	th := d.a.th
	parts, ok := d.payloadParts(s)
	if !ok {
		return th.loading(gtx)
	}
	var rows []layout.Widget
	for _, p := range parts {
		var toggle layout.Widget
		if p.parsed != nil {
			toggle = d.rawToggle(p.key)
		}
		title, open := d.section(p.key, p.title, toggle)
		rows = append(rows, title)
		if !open {
			continue
		}
		if p.parsed != nil && !d.rawOn[p.key] {
			rows = append(rows, p.parsed()...)
			continue
		}
		src := p.source
		if len(src) > 64<<10 {
			src = src[:64<<10] + "…"
		}
		sel := d.sel()
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 20, Top: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return selLabel(gtx, th, sel, src, 12, th.Text, true)
			})
		})
	}
	return d.scrolled(gtx, rows)
}

// treeRows lays the request's JSON payload out as tree rows that open and
// close, inside the payload section.
func (d *detailView) treeRows() []layout.Widget {
	var rows []layout.Widget
	count := 0
	var walk func(n *jnode)
	walk = func(n *jnode) {
		if count > 5000 {
			return
		}
		count++
		clk, ok := d.payClk[n]
		if !ok {
			clk = new(widget.Clickable)
			d.payClk[n] = clk
		}
		node := n
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			if clk.Clicked(gtx) && (node.kind == 'o' || node.kind == 'a') {
				node.open = !node.open
			}
			return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.jsonRow(gtx, node, clk.Hovered())
			})
		})
		if n.open {
			for _, c := range n.kids {
				walk(c)
			}
		}
	}
	walk(d.payTree)
	return rows
}

// jsonRow draws one JSON node: key, then value or summary.
func (d *detailView) jsonRow(gtx layout.Context, n *jnode, hover bool) layout.Dimensions {
	th := d.a.th
	rh := gtx.Dp(20)
	w := gtx.Constraints.Max.X
	if hover && (n.kind == 'o' || n.kind == 'a') {
		fill(gtx.Ops, image.Rect(0, 0, w, rh), th.Bg2)
	}
	x := gtx.Dp(20) + n.depth*gtx.Dp(16)
	if n.kind == 'o' || n.kind == 'a' {
		ic := icChevron
		if n.open {
			ic = icExpand
		}
		st := op.Offset(image.Pt(x-gtx.Dp(16), (rh-gtx.Dp(14))/2)).Push(gtx.Ops)
		drawIcon(gtx, ic, 14, th.Text3)
		st.Pop()
	}
	put := func(s string, c color.NRGBA) {
		if x >= w || s == "" {
			return
		}
		cg := gtx
		cg.Constraints = layout.Constraints{Max: image.Pt(w-x, rh)}
		rec := op.Record(gtx.Ops)
		dd := Label{Text: s, Size: 12, Color: c, Mono: true}.Layout(cg, th)
		call := rec.Stop()
		st := op.Offset(image.Pt(x, (rh-dd.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		x += dd.Size.X
	}
	if n.parent != nil {
		kc := th.SynKey
		if n.index {
			kc = th.Text3
		}
		put(n.key, kc)
		put(": ", th.Text2)
	}
	switch n.kind {
	case 'o', 'a':
		put(n.summary(160), th.Text2)
	case 's':
		v := n.val
		if len(v) > 2000 {
			v = v[:2000] + "…"
		}
		put(strconv.Quote(strings.ReplaceAll(v, "\n", "↵")), th.SynString)
	case 'n':
		put(n.val, th.SynNumber)
	default:
		put(n.val, th.SynKeyword)
	}
	return layout.Dimensions{Size: image.Pt(w, rh)}
}

// ---- Timing -----------------------------------------------------------

func (d *detailView) timingRows(s *capture.Session) []layout.Widget {
	th := d.a.th
	t := s.Timings
	total := s.Duration()
	type phase struct {
		group, name string
		dur         time.Duration
		c           color.NRGBA
	}
	connect := t.Connect
	if t.TLS > 0 && connect > 0 {
		connect -= t.TLS
	}
	phases := []phase{
		{"资源调度", "排队", t.Blocked, th.Text3},
		{"连接", "DNS 查找", t.DNS, th.Teal},
		{"连接", "初始连接", connect, th.Orange},
		{"连接", "SSL", t.TLS, th.Purple},
		{"请求/响应", "已发送请求", t.Send, th.Text2},
		{"请求/响应", "等待服务器响应 (TTFB)", t.Wait, th.Green},
		{"请求/响应", "内容下载", t.Receive, th.Blue},
	}
	// The scale covers the whole session, or the phases if they add up to
	// more, so no bar runs off the end of the track.
	var sum time.Duration
	for _, p := range phases {
		sum += max(p.dur, 0)
	}
	total = max(total, sum, time.Millisecond)
	var rows []layout.Widget
	rows = append(rows, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return th.txt(gtx, "开始于 "+s.Started.Format("15:04:05.000"), 12, th.Text2)
		})
	})
	group := ""
	offset := time.Duration(0)
	for _, p := range phases {
		p := p
		if p.dur < 0 {
			continue
		}
		if p.group != group {
			group = p.group
			g := group
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 10, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.txtW(gtx, g, 12, th.Text, font.SemiBold)
				})
			})
		}
		// Phases follow one another; the initial connection above already
		// leaves out the SSL handshake that comes after it.
		start := offset
		offset += p.dur
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(22)
			w := gtx.Constraints.Max.X
			nameW := min(gtx.Dp(170), w/3)
			durW := gtx.Dp(80)
			barW := max(w-nameW-durW-gtx.Dp(16), gtx.Dp(40))
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			cg := gtx
			cg.Constraints = layout.Exact(image.Pt(nameW, h))
			layout.W.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.txt(gtx, p.name, 12, th.Text2)
				})
			})
			x0 := nameW + gtx.Dp(8) + int(float64(start)/float64(total)*float64(barW))
			x1 := nameW + gtx.Dp(8) + int(float64(start+p.dur)/float64(total)*float64(barW))
			end := nameW + gtx.Dp(8) + barW
			x1 = min(x1, end)
			x0 = min(x0, x1)
			if x1-x0 < 2 {
				x1 = x0 + 2
			}
			if x1 > end {
				x0, x1 = end-2, end
			}
			fill(gtx.Ops, image.Rect(nameW+gtx.Dp(8), h/2, nameW+gtx.Dp(8)+barW, h/2+1), alpha(th.Border, 0xff))
			fill(gtx.Ops, image.Rect(x0, h/2-gtx.Dp(5), x1, h/2+gtx.Dp(5)), p.c)
			st := op.Offset(image.Pt(w-durW, 0)).Push(gtx.Ops)
			cg.Constraints = layout.Exact(image.Pt(durW, h))
			layout.E.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, fmtDuration(p.dur), 12, th.Text)
			})
			st.Pop()
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
	}
	rows = append(rows, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			w := gtx.Constraints.Max.X
			fill(gtx.Ops, image.Rect(0, 0, w, max(gtx.Dp(1), 1)), th.Border)
			return layout.Inset{Top: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, "总计", 12, th.Text, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						txt := fmtDuration(s.Duration())
						if s.Running() {
							txt = "进行中"
						}
						return th.txtW(gtx, txt, 12, th.Text, font.SemiBold)
					}),
				)
			})
		})
	})
	if s.Kind != capture.KindTunnel && s.Timings.Connect < 0 && s.Timings.DNS < 0 && s.Source != capture.SourceImport {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txt(gtx, "复用连接", 12, th.Text3)
			})
		})
	}
	return rows
}

// ---- Cookies ----------------------------------------------------------

func (d *detailView) cookieRows(s *capture.Session) []layout.Widget {
	th := d.a.th
	var rows []layout.Widget
	if cs := capture.ParseCookies(s.ReqHeader); len(cs) > 0 {
		title, open := d.section("reqcookies", fmt.Sprintf("请求 Cookie · %d", len(cs)), nil)
		rows = append(rows, title)
		if open {
			for _, c := range cs {
				rows = append(rows, d.kv(c.Name, c.Value, 180, th.Text, true))
			}
		}
	}
	if cs := capture.ParseSetCookies(s.RespHeader); len(cs) > 0 {
		title, open := d.section("respcookies", fmt.Sprintf("响应 Cookie · %d", len(cs)), nil)
		rows = append(rows, title)
		if open {
			for _, c := range cs {
				var attrs []string
				add := func(k, v string) {
					if v != "" {
						attrs = append(attrs, k+"="+v)
					}
				}
				add("Domain", c.Domain)
				add("Path", c.Path)
				add("Expires", c.Expires)
				add("Max-Age", c.MaxAge)
				add("SameSite", c.SameSite)
				if c.HTTPOnly {
					attrs = append(attrs, "HttpOnly")
				}
				if c.Secure {
					attrs = append(attrs, "Secure")
				}
				if c.Partitioned {
					attrs = append(attrs, "Partitioned")
				}
				v := c.Value
				if len(attrs) > 0 {
					v += "\n" + strings.Join(attrs, " · ")
				}
				rows = append(rows, d.kv(c.Name, v, 180, th.Text, true))
			}
		}
	}
	return rows
}

// ---- TLS --------------------------------------------------------------

func (d *detailView) tlsRows(s *capture.Session) []layout.Widget {
	th := d.a.th
	t := s.TLS
	var rows []layout.Widget
	title, open := d.section("tls", "连接", nil)
	rows = append(rows, title)
	nw := unit.Dp(120)
	if open {
		rows = append(rows, d.kv("服务器名称 (SNI)", t.ServerName, nw, th.Text, false))
		if t.Version != "" {
			rows = append(rows, d.kv("TLS 版本", t.Version, nw, th.Text, false))
			rows = append(rows, d.kv("加密套件", t.Cipher, nw, th.Text, false))
		}
		if t.ALPN != "" {
			rows = append(rows, d.kv("ALPN", t.ALPN, nw, th.Text, false))
		}
		dec := "否"
		if s.Decrypted {
			dec = "是"
		}
		rows = append(rows, d.kv("已解密", dec, nw, th.Text, false))
	}
	if t.Subject != "" {
		title, open = d.section("cert", "服务器证书", nil)
		rows = append(rows, title)
		if open {
			rows = append(rows, d.kv("使用者", t.Subject, nw, th.Text, false))
			rows = append(rows, d.kv("颁发者", t.Issuer, nw, th.Text, false))
			rows = append(rows, d.kv("有效期", t.NotBefore.Format("2006-01-02")+" 至 "+t.NotAfter.Format("2006-01-02"), nw, th.Text, false))
			if len(t.SANs) > 0 {
				rows = append(rows, d.kv("备用名称", strings.Join(t.SANs, "\n"), nw, th.Text, true))
			}
		}
	}
	return rows
}

// ---- Raw --------------------------------------------------------------

func rawText(s *capture.Session, resp, decoded bool) string {
	var head, body []byte
	var raw []byte
	if resp {
		raw = capture.RawResponse(s, false)
	} else {
		raw = capture.RawRequest(s, false)
	}
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		head = raw[:i+4]
	} else {
		head = raw
	}
	if resp {
		body = s.RespBody
		if decoded {
			body, _ = capture.ResponseBody(s)
		}
	} else {
		body = s.ReqBody
		if decoded {
			body, _ = capture.RequestBody(s)
		}
	}
	text := strings.ReplaceAll(string(head), "\r\n", "\n")
	if len(body) == 0 {
		return text
	}
	h := s.ReqHeader
	if resp {
		h = s.RespHeader
	}
	if decoded && capture.IsTextual(mediaTypeOf(h), body) {
		text += string(capture.ToUTF8(body, capture.Charset(h.Get("Content-Type"))))
	} else if !decoded && capture.LooksText(body) && h.Get("Content-Encoding") == "" {
		text += string(body)
	} else {
		text += "二进制 · " + fmtBytes(int64(len(body)))
	}
	trunc := s.ReqTruncated
	if resp {
		trunc = s.RespTruncated
	}
	if trunc {
		text += "\n已截断"
	}
	return text
}

func mediaTypeOf(h capture.Header) string {
	ct := h.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func (d *detailView) rawTab(gtx layout.Context, s *capture.Session) layout.Dimensions {
	th := d.a.th
	if d.rawDecode.Update(gtx) {
		d.rawReq.key, d.rawResp.key = "", ""
	}
	dec := d.rawDecode.Value
	reqKey := fmt.Sprintf("%d-%p-%d-%v", s.ID, s.ReqBody, len(s.ReqHeader), dec)
	reqText, reqOK := d.rawReqZ.get(d.a, reqKey, len(s.ReqBody) > bigWork, func() string { return rawText(s, false, dec) })
	if reqOK {
		d.rawReq.set(reqText, reqKey)
	}
	respOK := true
	if s.Status > 0 {
		respKey := fmt.Sprintf("%d-%p-%d-%d-%v", s.ID, s.RespBody, len(s.RespBody), len(s.RespHeader), dec)
		var respText string
		respText, respOK = d.rawRespZ.get(d.a, respKey, len(s.RespBody) > bigWork, func() string { return rawText(s, true, dec) })
		if respOK {
			d.rawResp.set(respText, respKey)
		}
	} else {
		d.rawResp.set("", "none")
	}
	size := gtx.Constraints.Max
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12, Right: 12, Top: 6, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txtW(gtx, "请求", 12, th.Text, font.SemiBold)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d.a.smallCheck(gtx, &d.rawDecode, "解码正文") }),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			avail := size.Y - gtx.Dp(70)
			if d.rawSplit <= 0 {
				d.rawSplit = int(float32(avail/2) / gtx.Metric.PxPerDp)
			}
			h := max(min(gtx.Dp(unit.Dp(d.rawSplit)), avail-gtx.Dp(60)), gtx.Dp(60))
			gtx.Constraints = layout.Exact(image.Pt(size.X, h))
			if !reqOK {
				return th.loading(gtx)
			}
			return d.rawReq.Layout(gtx, d.a.set.WrapText)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return d.rawSplitter.Layout(gtx, th, layout.Vertical, &d.rawSplit, 60, 4000, 1, d.a.mouse)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12, Right: 12, Top: 4, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.txtW(gtx, "响应", 12, th.Text, font.SemiBold)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if !respOK {
				return th.loading(gtx)
			}
			return d.rawResp.Layout(gtx, d.a.set.WrapText)
		}),
	)
}

// ---- Breakpoint -------------------------------------------------------

func (d *detailView) breakTab(gtx layout.Context, s *capture.Session) layout.Dimensions {
	a := d.a
	th := a.th
	eng := a.svc.engine
	key := fmt.Sprintf("%d-%s", s.ID, s.Paused)
	if d.brkKey != key {
		d.brkKey = key
		d.brkEd.SetText(proxy.BreakText(s))
	}
	if d.brkGo.Clicked(gtx) {
		eng.Continue(s.ID, proxy.Resume{Raw: d.brkEd.Text(), Edit: true})
	}
	if d.brkAsIs.Clicked(gtx) {
		eng.Continue(s.ID, proxy.Resume{})
	}
	if d.brkAbort.Clicked(gtx) {
		eng.Continue(s.ID, proxy.Resume{Abort: true})
	}
	what := "请求"
	if s.Paused == "response" {
		what = "响应"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12, Right: 12, Top: 8, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icPause, 18, th.Warn) }),
					hspace(8),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return th.txt(gtx, "已在"+what+"断点处暂停", 12, th.Text)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return buttonRow(gtx,
							func(gtx layout.Context) layout.Dimensions {
								return th.button(gtx, &d.brkAbort, "终止", nil, btnDanger)
							},
							func(gtx layout.Context) layout.Dimensions {
								return th.button(gtx, &d.brkAsIs, "原样继续", nil, btnDefault)
							},
							func(gtx layout.Context) layout.Dimensions {
								return th.button(gtx, &d.brkGo, "继续", icPlay, btnPrimary)
							},
						)
					}),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12, Right: 12, Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				r := image.Rectangle{Max: gtx.Constraints.Max}
				fillRR(gtx.Ops, r, gtx.Dp(6), th.Bg1)
				border := th.Border
				if gtx.Focused(&d.brkEd) {
					border = th.Accent
				}
				strokeRR(gtx.Ops, r, gtx.Dp(6), float32(gtx.Dp(1)), border)
				layout.UniformInset(8).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = gtx.Constraints.Max
					return editorStyle{th: th, e: &d.brkEd, size: 12, mono: true}.Layout(gtx)
				})
				editMenuArea(gtx, th, &d.brkRC, &d.brkEd, r)
				return layout.Dimensions{Size: r.Max}
			})
		}),
	)
}

// prettyJSON indents JSON text, or returns ok false if it is not JSON.
func prettyJSON(b []byte) (string, bool) {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, t, "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}
