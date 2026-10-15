package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nlrcapture/internal/capture"
)

// h1result is a response read by roundTripH1, with its body unread.
type h1result struct {
	up        *upConn
	head      *head
	proto     string
	code      int
	reason    string
	body      io.Reader
	tm        capture.Timings
	sendDone  time.Time
	firstByte time.Time
}

func (r *h1result) response() *http.Response {
	h := http.Header{}
	for _, f := range r.head.header {
		h.Add(f.Name, f.Value)
	}
	major, minor := 1, 1
	if r.proto == "HTTP/1.0" {
		minor = 0
	}
	return &http.Response{StatusCode: r.code, Status: strconv.Itoa(r.code) + " " + r.reason, Proto: r.proto,
		ProtoMajor: major, ProtoMinor: minor, Header: h, Body: io.NopCloser(r.body), Trailer: http.Header{}}
}

// roundTripH1 sends one request over a new HTTP/1.1 connection. The caller
// reads the body and closes res.up.
func (e *Engine) roundTripH1(ctx context.Context, method, rawURL string, h capture.Header, body io.Reader, length int64) (*h1result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("不支持的协议：%s", u.Scheme)
	}
	hn, hp := splitHostPort(u.Host, defaultPort(scheme))
	hostport := net.JoinHostPort(hn, hp)
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var up *upConn
	if scheme == "https" {
		up, err = e.dialTLS(dctx, hostport, "", hn, []string{"http/1.1"})
	} else {
		up, err = e.dialHTTP(dctx, hostport, "")
	}
	cancel()
	if err != nil {
		return nil, err
	}
	target := u.RequestURI()
	if up.viaProxy {
		target = rawURL
	}
	hdr := capture.Header{}
	hasHost := false
	for _, f := range h {
		n := strings.ToLower(f.Name)
		if strings.HasPrefix(n, ":") || n == "content-length" || n == "transfer-encoding" || n == "connection" {
			continue
		}
		if n == "host" {
			hasHost = true
		}
		hdr = append(hdr, f)
	}
	if !hasHost {
		hdr = append(capture.Header{{Name: "Host", Value: u.Host}}, hdr...)
	}
	chunked := length < 0
	if chunked {
		hdr = append(hdr, capture.Field{Name: "Transfer-Encoding", Value: "chunked"})
	} else if length > 0 || method == "POST" || method == "PUT" || method == "PATCH" {
		hdr = append(hdr, capture.Field{Name: "Content-Length", Value: strconv.FormatInt(length, 10)})
	}
	res := &h1result{up: up, tm: up.take(time.Time{})}
	sendStart := time.Now()
	writeHead(up.bw, method+" "+target+" HTTP/1.1", hdr)
	if body != nil && length != 0 {
		if chunked {
			cw := chunkedWriter{w: up.bw}
			_, err = io.Copy(cw, body)
			cw.Close()
		} else {
			_, err = io.CopyN(up.bw, body, length)
		}
	}
	if err == nil {
		err = up.bw.Flush()
	}
	res.sendDone = time.Now()
	res.tm.Send = res.sendDone.Sub(sendStart)
	if err == nil {
		res.head, res.proto, res.code, res.reason, err = readResponseHead(up.br)
	}
	if err != nil {
		up.Close()
		return nil, err
	}
	res.firstByte = time.Now()
	res.tm.Wait = res.firstByte.Sub(res.sendDone)
	res.body = bodyReader(up.br, responseFraming(method, res.code, res.head.header))
	return res, nil
}

// Send issues a request built in the composer, or replays a captured one,
// and records it as a new session. It returns at once.
func (e *Engine) Send(method, rawURL string, h capture.Header, body []byte) {
	go e.send(method, rawURL, h, body)
}

func (e *Engine) send(method, rawURL string, h capture.Header, body []byte) {
	started := time.Now()
	u, _ := url.Parse(rawURL)
	s := &capture.Session{
		Kind: capture.KindHTTP, State: capture.StatePending, Source: capture.SourceCompose,
		Started: started, Method: method, URL: rawURL, Proto: "HTTP/1.1",
		ReqHeader: h.Clone(), ReqBody: body, ReqBodySize: int64(len(body)), ReqHeaderSize: h.Size(),
		Timings: capture.NoTimings(), Process: "NLR Capture",
	}
	if u != nil {
		s.Scheme, s.Host = u.Scheme, u.Host
		s.Decrypted = u.Scheme == "https"
	}
	// Composed requests are always kept, even while recording is off.
	e.Store.Add(s)
	r := &rec{e: e, id: s.ID}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := e.roundTripH1(ctx, method, rawURL, h, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		r.fail(err)
		return
	}
	defer res.up.Close()
	r.update(func(s *capture.Session) {
		s.ServerAddr, s.TLS = res.up.addr, res.up.tls
		s.Status, s.Reason, s.RespProto = res.code, res.reason, res.proto
		s.RespHeader, s.RespHeaderSize = res.head.header, res.head.size
		s.Timings = res.tm
		s.State = capture.StateReceiving
	})
	cfg := e.config()
	capt := &capBuffer{limit: cfg.MaxBody}
	_, err = io.Copy(capt, res.body)
	res.tm.Receive = time.Since(res.firstByte)
	r.update(func(s *capture.Session) {
		s.RespBody, s.RespBodySize, s.RespTruncated = capt.bytes(), capt.n, capt.truncated()
		s.Timings = res.tm
		s.Ended = time.Now()
		s.State = capture.StateDone
		if err != nil {
			s.State, s.Error = capture.StateFailed, errText(err)
		}
	})
}

// Replay sends a captured request again, unchanged.
func (e *Engine) Replay(s *capture.Session) {
	if s.Kind == capture.KindTunnel {
		return
	}
	h := capture.Header{}
	for _, f := range s.ReqHeader {
		if strings.HasPrefix(f.Name, ":") {
			if f.Name == ":authority" {
				h = append(h, capture.Field{Name: "Host", Value: f.Value})
			}
			continue
		}
		h = append(h, f)
	}
	u := s.URL
	if strings.HasPrefix(u, "ws://") {
		u = "http://" + u[5:]
	} else if strings.HasPrefix(u, "wss://") {
		u = "https://" + u[6:]
	}
	body := s.ReqBody
	e.Send(s.Method, u, h, body)
}
