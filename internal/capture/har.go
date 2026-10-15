package capture

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// HAR 1.2 structures (http://www.softwareishard.com/blog/har-12-spec/),
// with the DevTools extensions this program reads and writes.

type harFile struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Version string     `json:"version"`
	Creator harCreator `json:"creator"`
	Pages   []harPage  `json:"pages"`
	Entries []harEntry `json:"entries"`
}

type harCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type harPage struct {
	StartedDateTime string         `json:"startedDateTime"`
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	PageTimings     map[string]any `json:"pageTimings"`
}

type harEntry struct {
	Pageref         string         `json:"pageref,omitempty"`
	StartedDateTime string         `json:"startedDateTime"`
	Time            float64        `json:"time"`
	Request         harRequest     `json:"request"`
	Response        harResponse    `json:"response"`
	Cache           map[string]any `json:"cache"`
	Timings         harTimings     `json:"timings"`
	ServerIPAddress string         `json:"serverIPAddress,omitempty"`
	Connection      string         `json:"connection,omitempty"`
	Comment         string         `json:"comment,omitempty"`
	ResourceType    string         `json:"_resourceType,omitempty"`
	WebSocket       []harWSMessage `json:"_webSocketMessages,omitempty"`
	Process         string         `json:"_process,omitempty"`
	PID             int            `json:"_pid,omitempty"`
	ClientAddr      string         `json:"_clientAddress,omitempty"`
	Error           string         `json:"_error,omitempty"`
	Tunnel          bool           `json:"_tunnel,omitempty"`
	BytesUp         int64          `json:"_bytesUp,omitempty"`
	BytesDown       int64          `json:"_bytesDown,omitempty"`
}

type harNV struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

type harCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	SameSite string `json:"sameSite,omitempty"`
}

type harRequest struct {
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	HTTPVersion string       `json:"httpVersion"`
	Cookies     []harCookie  `json:"cookies"`
	Headers     []harNV      `json:"headers"`
	QueryString []harNV      `json:"queryString"`
	PostData    *harPostData `json:"postData,omitempty"`
	HeadersSize int          `json:"headersSize"`
	BodySize    int64        `json:"bodySize"`
}

type harPostData struct {
	MimeType string     `json:"mimeType"`
	Text     string     `json:"text"`
	Params   []harParam `json:"params,omitempty"`
	Encoding string     `json:"_encoding,omitempty"`
}

type harParam struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

type harResponse struct {
	Status       int         `json:"status"`
	StatusText   string      `json:"statusText"`
	HTTPVersion  string      `json:"httpVersion"`
	Cookies      []harCookie `json:"cookies"`
	Headers      []harNV     `json:"headers"`
	Content      harContent  `json:"content"`
	RedirectURL  string      `json:"redirectURL"`
	HeadersSize  int         `json:"headersSize"`
	BodySize     int64       `json:"bodySize"`
	TransferSize int64       `json:"_transferSize,omitempty"`
	Error        string      `json:"_error,omitempty"`
}

type harContent struct {
	Size        int64  `json:"size"`
	Compression int64  `json:"compression,omitempty"`
	MimeType    string `json:"mimeType"`
	Text        string `json:"text,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
}

type harTimings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	SSL     float64 `json:"ssl"`
}

type harWSMessage struct {
	Type   string  `json:"type"` // "send" or "receive"
	Time   float64 `json:"time"` // seconds since the epoch
	Opcode int     `json:"opcode"`
	Data   string  `json:"data"`
}

func ms(d time.Duration) float64 {
	if d < 0 {
		return -1
	}
	return float64(d.Microseconds()) / 1000
}

func fromMS(v float64) time.Duration {
	if v < 0 {
		return -1
	}
	return time.Duration(v * float64(time.Millisecond))
}

func harHeaders(h Header) []harNV {
	out := make([]harNV, 0, len(h))
	for _, f := range h {
		out = append(out, harNV{Name: f.Name, Value: f.Value})
	}
	return out
}

func harVersion(p string) string {
	switch p {
	case "":
		return "HTTP/1.1"
	case "h2", "HTTP/2", "HTTP/2.0":
		return "h2"
	}
	return p
}

// textOrBase64 returns body as HAR text, base64 encoded when it is not
// valid UTF-8.
func textOrBase64(b []byte, mt string) (text, enc string) {
	if len(b) == 0 {
		return "", ""
	}
	if utf8.Valid(b) && IsTextual(mt, b) {
		return string(b), ""
	}
	return base64.StdEncoding.EncodeToString(b), "base64"
}

// WriteHAR writes the sessions as a HAR 1.2 log.
func WriteHAR(w io.Writer, sessions []*Session, creatorVersion string) error {
	f := harFile{Log: harLog{
		Version: "1.2",
		Creator: harCreator{Name: "NLR Capture", Version: creatorVersion},
		Pages:   []harPage{},
		Entries: make([]harEntry, 0, len(sessions)),
	}}
	for _, s := range sessions {
		f.Log.Entries = append(f.Log.Entries, toHAR(s))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(f)
}

func toHAR(s *Session) harEntry {
	e := harEntry{
		StartedDateTime: s.Started.Format("2006-01-02T15:04:05.000Z07:00"),
		Time:            ms(s.Duration()),
		Cache:           map[string]any{},
		ServerIPAddress: hostOnly(s.ServerAddr),
		Connection:      portOnly(s.ServerAddr),
		Comment:         s.Comment,
		ResourceType:    ResourceType(s),
		Process:         s.Process,
		PID:             s.PID,
		ClientAddr:      s.ClientAddr,
		Error:           s.Error,
		Tunnel:          s.Kind == KindTunnel,
		BytesUp:         s.BytesUp,
		BytesDown:       s.BytesDown,
	}
	if e.ResourceType == TypeTunnel {
		e.ResourceType = TypeOther
	}
	// DevTools names API calls "fetch" in HAR files.
	if e.ResourceType == TypeAPI {
		e.ResourceType = "fetch"
	}
	t := s.Timings
	e.Timings = harTimings{Blocked: ms(t.Blocked), DNS: ms(t.DNS), Connect: ms(t.Connect), Send: max(ms(t.Send), 0), Wait: max(ms(t.Wait), 0), Receive: max(ms(t.Receive), 0), SSL: ms(t.TLS)}

	req := harRequest{
		Method:      s.Method,
		URL:         s.URL,
		HTTPVersion: harVersion(s.Proto),
		Cookies:     []harCookie{},
		Headers:     harHeaders(s.ReqHeader),
		QueryString: []harNV{},
		HeadersSize: s.ReqHeaderSize,
		BodySize:    s.ReqBodySize,
	}
	if s.Kind == KindTunnel {
		req.URL = "https://" + s.Host + "/"
	}
	if req.HeadersSize == 0 {
		req.HeadersSize = -1
	}
	for _, c := range ParseCookies(s.ReqHeader) {
		req.Cookies = append(req.Cookies, harCookie{Name: c.Name, Value: c.Value})
	}
	if u, err := url.Parse(s.URL); err == nil {
		for _, kv := range strings.Split(u.RawQuery, "&") {
			if kv == "" {
				continue
			}
			k, v, _ := strings.Cut(kv, "=")
			k2, _ := url.QueryUnescape(k)
			v2, _ := url.QueryUnescape(v)
			req.QueryString = append(req.QueryString, harNV{Name: k2, Value: v2})
		}
	}
	if len(s.ReqBody) > 0 {
		body, _ := RequestBody(s)
		mt := s.ReqHeader.Get("Content-Type")
		text, enc := textOrBase64(body, mediaType(mt))
		req.PostData = &harPostData{MimeType: mt, Text: text, Encoding: enc}
		if mediaType(mt) == "application/x-www-form-urlencoded" {
			if vals, err := url.ParseQuery(string(body)); err == nil {
				for k, vs := range vals {
					for _, v := range vs {
						req.PostData.Params = append(req.PostData.Params, harParam{Name: k, Value: v})
					}
				}
			}
		}
	}
	e.Request = req

	resp := harResponse{
		Status:       s.Status,
		StatusText:   s.Reason,
		HTTPVersion:  harVersion(s.RespProto),
		Cookies:      []harCookie{},
		Headers:      harHeaders(s.RespHeader),
		RedirectURL:  s.RespHeader.Get("Location"),
		HeadersSize:  s.RespHeaderSize,
		BodySize:     s.RespBodySize,
		TransferSize: s.Transferred(),
		Error:        s.Error,
	}
	if s.Status == 0 {
		resp.HeadersSize, resp.BodySize = -1, -1
		resp.HTTPVersion = harVersion(s.Proto)
	}
	for _, c := range ParseSetCookies(s.RespHeader) {
		resp.Cookies = append(resp.Cookies, harCookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, Expires: c.Expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: c.SameSite})
	}
	body, _ := ResponseBody(s)
	mt := s.RespHeader.Get("Content-Type")
	text, enc := textOrBase64(body, mediaType(mt))
	resp.Content = harContent{Size: int64(len(body)), MimeType: mt, Text: text, Encoding: enc}
	if !s.BodyDecoded && s.RespHeader.Get("Content-Encoding") != "" {
		resp.Content.Compression = int64(len(body)) - s.RespBodySize
	}
	if resp.Content.MimeType == "" {
		resp.Content.MimeType = "x-unknown"
	}
	e.Response = resp

	for _, m := range s.WS {
		typ := "receive"
		if m.FromClient {
			typ = "send"
		}
		data := string(m.Data)
		if m.Opcode != 1 || !utf8.Valid(m.Data) {
			data = base64.StdEncoding.EncodeToString(m.Data)
		}
		e.WebSocket = append(e.WebSocket, harWSMessage{Type: typ, Time: float64(m.Time.UnixMicro()) / 1e6, Opcode: int(m.Opcode), Data: data})
	}
	return e
}

func hostOnly(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 && !strings.Contains(addr[i:], "]") {
		return strings.Trim(addr[:i], "[]")
	}
	return addr
}

func portOnly(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 && !strings.Contains(addr[i:], "]") {
		return addr[i+1:]
	}
	return ""
}

// ReadHAR parses a HAR log into sessions, which are not yet in a store.
func ReadHAR(r io.Reader) ([]*Session, error) {
	var f harFile
	dec := json.NewDecoder(r)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	if f.Log.Entries == nil {
		return nil, errors.New("不是有效的 HAR 文件：缺少 log.entries")
	}
	out := make([]*Session, 0, len(f.Log.Entries))
	for _, e := range f.Log.Entries {
		out = append(out, fromHAR(e))
	}
	return out, nil
}

func fromHAR(e harEntry) *Session {
	s := &Session{
		Kind:        KindHTTP,
		State:       StateDone,
		Source:      SourceImport,
		Method:      e.Request.Method,
		URL:         e.Request.URL,
		Proto:       e.Request.HTTPVersion,
		RespProto:   e.Response.HTTPVersion,
		Status:      e.Response.Status,
		Reason:      e.Response.StatusText,
		Comment:     e.Comment,
		Process:     e.Process,
		PID:         e.PID,
		ClientAddr:  e.ClientAddr,
		Error:       e.Error,
		BodyDecoded: true,
		BytesUp:     e.BytesUp,
		BytesDown:   e.BytesDown,
	}
	if e.Error == "" && e.Response.Error != "" {
		s.Error = e.Response.Error
	}
	if t, err := time.Parse(time.RFC3339Nano, e.StartedDateTime); err == nil {
		s.Started = t
	} else {
		s.Started = time.Now()
	}
	s.Ended = s.Started.Add(fromMS(max(e.Time, 0)))
	if e.ServerIPAddress != "" {
		s.ServerAddr = e.ServerIPAddress
		if p := e.Connection; p != "" && isDigits(p) {
			if strings.Contains(s.ServerAddr, ":") {
				s.ServerAddr = "[" + s.ServerAddr + "]"
			}
			s.ServerAddr += ":" + p
		}
	}
	if u, err := url.Parse(s.URL); err == nil {
		s.Scheme = u.Scheme
		s.Host = u.Host
	}
	if e.Tunnel {
		s.Kind = KindTunnel
		s.Method = "CONNECT"
		s.URL = s.Host
	}
	if e.ResourceType == TypeWebSocket || len(e.WebSocket) > 0 {
		s.Kind = KindWebSocket
	}
	for _, h := range e.Request.Headers {
		s.ReqHeader = append(s.ReqHeader, Field{h.Name, h.Value})
	}
	for _, h := range e.Response.Headers {
		s.RespHeader = append(s.RespHeader, Field{h.Name, h.Value})
	}
	s.ReqHeaderSize = max(e.Request.HeadersSize, 0)
	s.RespHeaderSize = max(e.Response.HeadersSize, 0)
	if pd := e.Request.PostData; pd != nil {
		if pd.Encoding == "base64" {
			s.ReqBody, _ = base64.StdEncoding.DecodeString(pd.Text)
		} else {
			s.ReqBody = []byte(pd.Text)
		}
		if !s.ReqHeader.Has("Content-Type") && pd.MimeType != "" {
			s.ReqHeader = append(s.ReqHeader, Field{"Content-Type", pd.MimeType})
		}
	}
	s.ReqBodySize = max(e.Request.BodySize, int64(len(s.ReqBody)))
	c := e.Response.Content
	if c.Encoding == "base64" {
		s.RespBody, _ = base64.StdEncoding.DecodeString(c.Text)
	} else {
		s.RespBody = []byte(c.Text)
	}
	if !s.RespHeader.Has("Content-Type") && c.MimeType != "" && c.MimeType != "x-unknown" {
		s.RespHeader = append(s.RespHeader, Field{"Content-Type", c.MimeType})
	}
	s.RespBodySize = e.Response.BodySize
	if s.RespBodySize < 0 {
		s.RespBodySize = int64(len(s.RespBody))
	}
	if e.Response.TransferSize > 0 && s.RespHeaderSize == 0 {
		s.RespHeaderSize = int(max(e.Response.TransferSize-s.RespBodySize, 0))
	}
	t := e.Timings
	s.Timings = Timings{Blocked: fromMS(t.Blocked), DNS: fromMS(t.DNS), Connect: fromMS(t.Connect), TLS: fromMS(t.SSL), Send: fromMS(t.Send), Wait: fromMS(t.Wait), Receive: fromMS(t.Receive)}
	if s.Status == 0 && s.Error == "" && s.Kind != KindTunnel {
		s.State = StateFailed
		s.Error = "无响应"
	} else if s.Error != "" && s.Status == 0 {
		s.State = StateFailed
	}
	s.Decrypted = s.Scheme == "https" || s.Scheme == "wss"
	for _, m := range e.WebSocket {
		msg := WSMessage{
			Time:       time.UnixMicro(int64(m.Time * 1e6)),
			FromClient: m.Type == "send",
			Opcode:     byte(m.Opcode),
		}
		if m.Opcode == 1 {
			msg.Data = []byte(m.Data)
		} else if b, err := base64.StdEncoding.DecodeString(m.Data); err == nil {
			msg.Data = b
		} else {
			msg.Data = []byte(m.Data)
		}
		msg.Length = len(msg.Data)
		s.WS = append(s.WS, msg)
	}
	return s
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
