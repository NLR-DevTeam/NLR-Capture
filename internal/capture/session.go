// Package capture holds the captured traffic: sessions, the store that
// keeps them, body decoding, filtering, searching and HAR import/export.
package capture

import (
	"net/url"
	"strings"
	"time"
)

// Kind is what a session represents.
type Kind int

const (
	KindHTTP      Kind = iota // a request and its response
	KindTunnel                // a CONNECT tunnel that was not decrypted
	KindWebSocket             // an upgraded request with its messages
)

// State is where a session stands.
type State int

const (
	StatePending   State = iota // waiting for the response
	StatePaused                 // held at a breakpoint
	StateReceiving              // response headers arrived, body streaming
	StateDone
	StateFailed
	StateBlocked // refused by a rule
)

// Source says how a session entered the store.
const (
	SourceProxy   = "proxy"
	SourceTUN     = "tun"
	SourceImport  = "import"
	SourceCompose = "compose"
)

// Field is one header line. Names keep their original case and order.
type Field struct {
	Name, Value string
}

// Header is an ordered list of header fields.
type Header []Field

// Get returns the first value of name, ignoring case.
func (h Header) Get(name string) string {
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

// Values returns every value of name, ignoring case.
func (h Header) Values(name string) []string {
	var out []string
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			out = append(out, f.Value)
		}
	}
	return out
}

// Has reports whether name is present.
func (h Header) Has(name string) bool {
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			return true
		}
	}
	return false
}

// Clone returns a copy that can be changed independently.
func (h Header) Clone() Header {
	if h == nil {
		return nil
	}
	return append(Header(nil), h...)
}

// Set replaces every value of name with one value, appending it if absent.
func (h Header) Set(name, value string) Header {
	out := h[:0:0]
	done := false
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			if !done {
				out = append(out, Field{f.Name, value})
				done = true
			}
			continue
		}
		out = append(out, f)
	}
	if !done {
		out = append(out, Field{name, value})
	}
	return out
}

// Del removes every value of name.
func (h Header) Del(name string) Header {
	out := h[:0:0]
	for _, f := range h {
		if !strings.EqualFold(f.Name, name) {
			out = append(out, f)
		}
	}
	return out
}

// Size returns the size of the header block on the wire, counting
// "Name: Value\r\n" per field.
func (h Header) Size() int {
	n := 0
	for _, f := range h {
		n += len(f.Name) + len(f.Value) + 4
	}
	return n
}

// Timings are the phases of a request, as in the DevTools timing view.
// A negative value means the phase did not happen (a reused connection has
// no DNS, connect or TLS phase).
type Timings struct {
	Blocked time.Duration // queued before a connection was available
	DNS     time.Duration
	Connect time.Duration // TCP connect, including TLS
	TLS     time.Duration // TLS handshake, part of Connect
	Send    time.Duration
	Wait    time.Duration // time to first byte
	Receive time.Duration
}

// NoTimings returns timings where every phase is absent.
func NoTimings() Timings {
	return Timings{Blocked: -1, DNS: -1, Connect: -1, TLS: -1, Send: -1, Wait: -1, Receive: -1}
}

// Total returns the sum of the phases that happened, not counting TLS twice.
func (t Timings) Total() time.Duration {
	var d time.Duration
	for _, v := range []time.Duration{t.Blocked, t.DNS, t.Connect, t.Send, t.Wait, t.Receive} {
		if v > 0 {
			d += v
		}
	}
	return d
}

// TLSInfo describes the TLS connection to the server.
type TLSInfo struct {
	Version     string
	Cipher      string
	ServerName  string
	ALPN        string
	Subject     string
	Issuer      string
	NotBefore   time.Time
	NotAfter    time.Time
	SANs        []string
	ClientError string // why the client rejected our certificate, if it did
}

// WSMessage is one WebSocket frame (reassembled message).
type WSMessage struct {
	Time       time.Time
	FromClient bool
	Opcode     byte // 1 text, 2 binary, 8 close, 9 ping, 10 pong
	Data       []byte
	Length     int // full payload length; Data may be truncated
}

// Session is one captured exchange. A session in the store is never
// modified: updates replace it with a changed copy (see Store.Update), so
// a *Session handed to the UI can be read without locking.
type Session struct {
	ID      int64
	Kind    Kind
	State   State
	Source  string
	Started time.Time
	// Ended is when the last byte of the response arrived.
	Ended time.Time

	Method string
	URL    string // absolute URL
	Proto  string // HTTP/1.1, h2, HTTP/1.0
	Scheme string
	Host   string // host[:port] the request was for

	ClientAddr string
	ServerAddr string // remote IP:port
	Process    string
	PID        int

	ReqHeader     Header
	ReqBody       []byte // as sent, after removing chunked framing
	ReqBodySize   int64  // full size; ReqBody may be truncated
	ReqTruncated  bool
	ReqHeaderSize int

	Status         int
	Reason         string
	RespProto      string
	RespHeader     Header
	RespBody       []byte // as received (still content-encoded)
	RespBodySize   int64
	RespTruncated  bool
	RespHeaderSize int
	// BodyDecoded marks bodies that are already decoded although the
	// headers name a content encoding, as with imported HAR files.
	BodyDecoded bool

	Timings Timings
	TLS     *TLSInfo
	// Decrypted is set for HTTPS sessions that were intercepted.
	Decrypted bool
	Error     string
	// Rule names the rule that answered or changed the session.
	Rule string

	WS []WSMessage

	// BytesUp and BytesDown count tunnel traffic.
	BytesUp, BytesDown int64

	Comment string
	Mark    int // highlight color index, 0 for none

	// Paused is set while a breakpoint holds the session: "request" or
	// "response".
	Paused string
}

// parsed URL cache is not kept on the session: the URL is parsed on use.

// ParsedURL returns the URL parsed, or an empty URL on error.
func (s *Session) ParsedURL() *url.URL {
	u, err := url.Parse(s.URL)
	if err != nil || u == nil {
		return &url.URL{}
	}
	return u
}

// Hostname returns the host without port.
func (s *Session) Hostname() string {
	h := s.Host
	if h == "" {
		h = s.ParsedURL().Host
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.Trim(h, "[]")
}

// Path returns the path and query of the URL.
func (s *Session) Path() string {
	u := s.ParsedURL()
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// Name returns the short name shown in the request list: the last path
// segment with the query, like the DevTools Name column.
func (s *Session) Name() string {
	if s.Kind == KindTunnel {
		return s.Host
	}
	u := s.ParsedURL()
	p := u.EscapedPath()
	p = strings.TrimSuffix(p, "/")
	name := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		name = p[i+1:]
	}
	if name == "" {
		name = u.Host
		if u.Path != "" && u.Path != "/" {
			name = u.Path
		}
	}
	if u.RawQuery != "" {
		name += "?" + u.RawQuery
	}
	if name == "" {
		name = s.URL
	}
	return name
}

// Duration returns the total time of the session.
func (s *Session) Duration() time.Duration {
	if !s.Ended.IsZero() {
		return s.Ended.Sub(s.Started)
	}
	return s.Timings.Total()
}

// ContentType returns the media type of the response without parameters.
func (s *Session) ContentType() string {
	return mediaType(s.RespHeader.Get("Content-Type"))
}

// ReqContentType returns the media type of the request body.
func (s *Session) ReqContentType() string {
	return mediaType(s.ReqHeader.Get("Content-Type"))
}

func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// Transferred returns the bytes of the response on the wire, headers
// included, the DevTools "Size" column.
func (s *Session) Transferred() int64 {
	if s.Kind == KindTunnel {
		return s.BytesDown
	}
	if s.Status == 0 {
		return 0
	}
	return int64(s.RespHeaderSize) + s.RespBodySize
}

// Running reports whether the session has not finished.
func (s *Session) Running() bool {
	return s.State == StatePending || s.State == StateReceiving || s.State == StatePaused
}

// Failed reports whether the session ended in an error.
func (s *Session) Failed() bool {
	return s.State == StateFailed || s.State == StateBlocked
}
