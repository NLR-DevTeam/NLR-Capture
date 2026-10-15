package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"nlrcapture/internal/capture"
)

type atomicInt64 = atomic.Int64

// LocalHost is the name that reaches the proxy's own page, from which the
// root certificate can be downloaded.
const LocalHost = "nlr.capture"

// h1conn serves HTTP/1.x requests from one client connection: to the
// explicit proxy, inside an intercepted TLS connection, or redirected by
// the virtual adapter.
type h1conn struct {
	e        *Engine
	c        net.Conn
	br       *bufio.Reader
	bw       *bufio.Writer
	info     *ClientInfo
	scheme   string
	explicit bool
	// defHost is the Host used when a request has none; fixed is the
	// address the virtual adapter caught the connection going to; sni is
	// the name the client asked for in TLS.
	defHost string
	fixed   string
	sni     string

	up       *upConn
	upScheme string
	upErr    error
}

func (h *h1conn) closeUp() {
	if h.up != nil {
		h.up.Close()
		h.up = nil
	}
}

func defaultPort(scheme string) string {
	if scheme == "https" || scheme == "wss" {
		return "443"
	}
	return "80"
}

func (h *h1conn) serve() {
	for {
		h.c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		hd, err := readHead(h.br)
		h.c.SetReadDeadline(time.Time{})
		if err != nil {
			if errors.Is(err, errHeaderTooLarge) {
				writeSimple(h.bw, 431, nil, []byte("NLR Capture: header too large\n"), true)
			}
			return
		}
		method, target, proto, err := requestLine(hd.line)
		if err != nil {
			writeSimple(h.bw, 400, nil, []byte("NLR Capture: "+err.Error()+"\n"), true)
			return
		}
		if !h.handle(hd, method, target, proto) {
			return
		}
	}
}

func (h *h1conn) handle(hd *head, method, target, proto string) bool {
	e := h.e
	started := time.Now()
	scheme := h.scheme
	host := hd.header.Get("Host")
	path := target
	if method == "CONNECT" {
		if !h.explicit {
			writeSimple(h.bw, 405, nil, []byte("CONNECT not allowed here\n"), true)
			return false
		}
		if err := connectOK(h.bw); err != nil {
			return false
		}
		e.handleConnect(h.c, h.br, target, h.info)
		return false
	}
	absolute := false
	if i := strings.Index(target, "://"); i > 0 && i < 6 {
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			writeSimple(h.bw, 400, nil, []byte("NLR Capture: invalid URL\n"), true)
			return false
		}
		absolute = true
		scheme = strings.ToLower(u.Scheme)
		switch scheme {
		case "ws":
			scheme = "http"
		case "wss":
			scheme = "https"
		case "http", "https":
		default:
			writeSimple(h.bw, 400, nil, []byte("NLR Capture: unsupported scheme "+scheme+"\n"), true)
			return false
		}
		host = u.Host
		path = u.RequestURI()
	}
	if host == "" {
		host = h.defHost
	}
	hn, hp := splitHostPort(host, defaultPort(scheme))
	// Requests for the proxy itself: its page with the certificate.
	if strings.EqualFold(hn, LocalHost) || (h.explicit && !absolute) || (absolute && e.isSelf(hn, atoi(hp))) {
		return h.servePage(hd, method, path)
	}
	if host == "" {
		writeSimple(h.bw, 400, nil, []byte("NLR Capture: missing Host\n"), true)
		return false
	}
	return h.exchange(hd, method, path, proto, scheme, host, net.JoinHostPort(hn, hp), started)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// upstream returns a connection for scheme://hostport, reusing the
// current one when it fits.
func (h *h1conn) upstream(scheme, hostport string, useFixed bool) (*upConn, error) {
	if h.up != nil && !h.up.closed && h.up.host == hostport && h.upScheme == scheme {
		return h.up, nil
	}
	if h.up == nil && h.upErr != nil {
		// The connection opened during the TLS handshake failed.
		err := h.upErr
		h.upErr = nil
		return nil, err
	}
	h.closeUp()
	fixed := ""
	if useFixed {
		fixed = h.fixed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var up *upConn
	var err error
	if scheme == "https" {
		sni, _ := splitHostPort(hostport, "443")
		up, err = h.e.dialTLS(ctx, hostport, fixed, sni, []string{"http/1.1"})
	} else {
		up, err = h.e.dialHTTP(ctx, hostport, fixed)
	}
	if err != nil {
		return nil, err
	}
	h.up, h.upScheme = up, scheme
	return up, nil
}

// readResponseHead reads a final response head, skipping interim ones.
func readResponseHead(br *bufio.Reader) (*head, string, int, string, error) {
	for {
		rh, err := readHead(br)
		if err != nil {
			return nil, "", 0, "", err
		}
		p, code, reason, err := statusLine(rh.line)
		if err != nil {
			return nil, "", 0, "", err
		}
		if code >= 100 && code < 200 && code != 101 {
			continue
		}
		return rh, p, code, reason, nil
	}
}

// exchange forwards one request and its response, recording both.
func (h *h1conn) exchange(hd *head, method, path, proto, scheme, host, hostport string, started time.Time) bool {
	e := h.e
	cfg := e.config()
	fullURL := scheme + "://" + host + path
	upgrade := hasToken(hd.header, "Connection", "upgrade") && hd.header.Get("Upgrade") != ""
	ws := upgrade && strings.EqualFold(hd.header.Get("Upgrade"), "websocket")
	s := &capture.Session{
		Kind: capture.KindHTTP, State: capture.StatePending, Source: h.info.Source,
		Started: started, Method: method, URL: fullURL, Proto: proto, Scheme: scheme, Host: host,
		ClientAddr: h.info.Addr, Process: h.info.Process, PID: h.info.PID,
		ReqHeader: hd.header.Clone(), ReqHeaderSize: hd.size,
		Timings: capture.NoTimings(), Decrypted: scheme == "https",
	}
	if ws {
		s.Kind = capture.KindWebSocket
		s.URL = map[string]string{"http": "ws", "https": "wss"}[scheme] + "://" + host + path
	}
	if scheme == "https" && h.up != nil {
		s.TLS = h.up.tls
	}
	r := e.begin(s)

	fr, err := requestFraming(hd.header)
	if err != nil {
		r.fail(err)
		writeSimple(h.bw, 400, nil, []byte("NLR Capture: "+err.Error()+"\n"), true)
		return false
	}
	reqBody := bodyReader(h.br, fr)
	reqCap := &capBuffer{limit: cfg.MaxBody}
	clientClose := hasToken(hd.header, "Connection", "close") ||
		(proto == "HTTP/1.0" && !hasToken(hd.header, "Connection", "keep-alive"))

	fwd := stripHopHeaders(hd.header)
	if hasToken(hd.header, "Expect", "100-continue") {
		// Answer the client ourselves: the body is streamed on at once.
		fwd = fwd.Del("Expect")
		h.bw.WriteString("HTTP/1.1 100 Continue\r\n\r\n")
		h.bw.Flush()
	}

	v := evaluate(cfg.rules, method, fullURL)
	if v.rule != "" {
		r.update(func(s *capture.Session) { s.Rule = v.rule })
	}
	if v.delay > 0 {
		time.Sleep(v.delay)
	}
	for _, kv := range v.reqSet {
		fwd = fwd.Set(kv[0], kv[1])
	}
	if cfg.DisableCache {
		fwd = fwd.Del("If-None-Match").Del("If-Modified-Since").Set("Cache-Control", "no-cache").Set("Pragma", "no-cache")
	}
	if cfg.Throttle.Offline {
		r.update(func(s *capture.Session) {
			s.State, s.Error, s.Ended = capture.StateFailed, "离线", time.Now()
		})
		return false
	}
	if v.final() {
		io.Copy(reqCap, io.LimitReader(reqBody, 256<<20))
		r.update(func(s *capture.Session) {
			s.ReqBody, s.ReqBodySize, s.ReqTruncated = reqCap.bytes(), reqCap.n, reqCap.truncated()
		})
		if v.drop {
			r.update(func(s *capture.Session) {
				s.State, s.Error, s.Ended = capture.StateBlocked, "已被规则断开", time.Now()
			})
			return false
		}
		return h.answer(r, v, clientClose)
	}

	upHost, upHostport, upScheme, upPath := host, hostport, scheme, path
	redirected := false
	if v.redirect != "" {
		if u, err := url.Parse(v.redirect); err == nil && u.Host != "" {
			upScheme, upHost, upPath = strings.ToLower(u.Scheme), u.Host, u.RequestURI()
			hn, hp := splitHostPort(u.Host, defaultPort(upScheme))
			upHostport = net.JoinHostPort(hn, hp)
			fwd = fwd.Set("Host", u.Host)
			redirected = true
			r.update(func(s *capture.Session) { s.Rule += " → " + v.redirect })
		}
	}

	// Request breakpoint: the body is read in full so it can be edited.
	reqBuffered := false
	if r.id != 0 && cfg.breakOn(true, fullURL) {
		b, err := io.ReadAll(io.LimitReader(reqBody, 256<<20))
		if err != nil {
			r.fail(err)
			return false
		}
		r.update(func(s *capture.Session) {
			s.ReqBody, s.ReqBodySize = b, int64(len(b))
		})
		res := e.hold(r, "request", nil)
		if res.Abort {
			r.update(func(s *capture.Session) {
				s.State, s.Error, s.Ended = capture.StateFailed, "已在断点处终止", time.Now()
			})
			return false
		}
		if res.Edit {
			m2, t2, _, h2, body2, err := capture.ParseRawRequest(res.Raw)
			if err != nil {
				r.fail(err)
				writeSimple(h.bw, 400, nil, []byte("NLR Capture: "+err.Error()+"\n"), true)
				return false
			}
			method = m2
			if u, err := url.Parse(t2); err == nil && u.Host != "" {
				upScheme, upHost, upPath = strings.ToLower(u.Scheme), u.Host, u.RequestURI()
				hn, hp := splitHostPort(u.Host, defaultPort(upScheme))
				upHostport = net.JoinHostPort(hn, hp)
				redirected = upHostport != hostport
			} else {
				upPath = t2
			}
			if hv := h2.Get("Host"); hv != "" && hv != upHost {
				upHost = hv
				hn, hp := splitHostPort(hv, defaultPort(upScheme))
				upHostport = net.JoinHostPort(hn, hp)
				redirected = true
			}
			fwd = stripHopHeaders(h2)
			b = body2
			newURL := upScheme + "://" + upHost + upPath
			r.update(func(s *capture.Session) {
				s.Method, s.URL, s.ReqHeader, s.ReqBody, s.ReqBodySize = m2, newURL, h2, body2, int64(len(body2))
				s.Host, s.Scheme = upHost, upScheme
			})
		}
		if len(b) > 0 || fr.chunked || fr.length > 0 {
			fwd = setLength(fwd, len(b))
		}
		reqBody = bytes.NewReader(b)
		fr = framing{length: int64(len(b))}
		reqCap.Write(b)
		reqBuffered = true
	}

	if t := cfg.Throttle; t.Latency > 0 {
		time.Sleep(t.Latency)
	}

	dialStart := time.Now()
	up, err := h.upstream(upScheme, upHostport, !redirected)
	if err != nil {
		r.fail(err)
		writeSimple(h.bw, 502, nil, []byte("NLR Capture: "+errText(err)+"\n"), true)
		return false
	}
	reused := !up.fresh
	tm := up.take(started)
	tm.Blocked = dialStart.Sub(started)
	r.update(func(s *capture.Session) {
		s.ServerAddr = up.addr
		if up.tls != nil {
			s.TLS = up.tls
		}
	})

	target := upPath
	if up.viaProxy {
		target = upScheme + "://" + upHost + upPath
	}
	line := method + " " + target + " " + proto
	send := func(up *upConn, body io.Reader) error {
		writeHead(up.bw, line, fwd)
		if fr.chunked {
			cw := chunkedWriter{w: up.bw}
			if _, err := io.Copy(cw, body); err != nil {
				return err
			}
			cw.Close()
		} else if fr.length > 0 {
			var w io.Writer = up.bw
			if rate := cfg.Throttle.Up; rate > 0 {
				w = &throttled{w: up.bw, rate: rate}
			}
			if _, err := io.Copy(w, body); err != nil {
				return err
			}
		}
		return up.bw.Flush()
	}
	var body io.Reader = reqBody
	if !reqBuffered {
		body = io.TeeReader(reqBody, reqCap)
	}
	sendStart := time.Now()
	err = send(up, body)
	sendDur := time.Since(sendStart)
	var rh *head
	var rproto, reason string
	var code int
	if err == nil {
		up.Conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		rh, rproto, code, reason, err = readResponseHead(up.br)
		up.Conn.SetReadDeadline(time.Time{})
	}
	// A reused connection the server had closed: try once on a new one if
	// the request can be sent again.
	if err != nil && reused && (fr.length == 0 || (!reqCap.truncated() && reqCap.n == fr.length)) {
		h.closeUp()
		up, err = h.upstream(upScheme, upHostport, !redirected)
		if err == nil {
			tm = up.take(started)
			tm.Blocked = dialStart.Sub(started)
			sendStart = time.Now()
			err = send(up, bytes.NewReader(reqCap.bytes()))
			sendDur = time.Since(sendStart)
			if err == nil {
				rh, rproto, code, reason, err = readResponseHead(up.br)
			}
		}
	}
	ttfb := time.Since(sendStart) - sendDur
	if err != nil {
		h.closeUp()
		r.update(func(s *capture.Session) {
			s.ReqBody, s.ReqBodySize, s.ReqTruncated = reqCap.bytes(), reqCap.n, reqCap.truncated()
			s.Timings = tm
		})
		r.fail(err)
		writeSimple(h.bw, 502, nil, []byte("NLR Capture: "+errText(err)+"\n"), true)
		return false
	}
	tm.Send, tm.Wait = sendDur, ttfb

	respHdr := rh.header.Clone()
	for _, kv := range v.respSet {
		respHdr = respHdr.Set(kv[0], kv[1])
	}
	rfr := responseFraming(method, code, rh.header)
	r.update(func(s *capture.Session) {
		s.ReqBody, s.ReqBodySize, s.ReqTruncated = reqCap.bytes(), reqCap.n, reqCap.truncated()
		s.Status, s.Reason, s.RespProto = code, reason, rproto
		s.RespHeader, s.RespHeaderSize = respHdr, rh.size
		s.Timings = tm
		s.State = capture.StateReceiving
	})

	if code == 101 && upgrade {
		// The connection now carries another protocol; relay it.
		writeHead(h.bw, rh.line, respHdr)
		if err := h.bw.Flush(); err != nil {
			r.fail(err)
			return false
		}
		if !ws {
			r.update(func(s *capture.Session) { s.State, s.Ended = capture.StateDone, time.Now() })
		}
		e.relayUpgraded(r, h.c, h.br, up, respHdr, ws)
		h.closeUp()
		return false
	}

	serverClose := hasToken(rh.header, "Connection", "close") || (rfr.length < 0 && !rfr.chunked) ||
		(rproto == "HTTP/1.0" && !hasToken(rh.header, "Connection", "keep-alive"))
	closeAfter := serverClose || clientClose
	respBody := bodyReader(up.br, rfr)

	// Response breakpoint.
	if r.id != 0 && cfg.breakOn(false, fullURL) {
		b, err := io.ReadAll(io.LimitReader(respBody, 256<<20))
		if err != nil {
			h.closeUp()
			r.fail(err)
			writeSimple(h.bw, 502, nil, []byte("NLR Capture: "+errText(err)+"\n"), true)
			return false
		}
		recvDur := time.Since(sendStart) - sendDur - ttfb
		r.update(func(s *capture.Session) {
			s.RespBody, s.RespBodySize = b, int64(len(b))
			s.Timings.Receive = recvDur
		})
		res := e.hold(r, "response", nil)
		if res.Abort {
			h.closeUp()
			r.update(func(s *capture.Session) {
				s.State, s.Error, s.Ended = capture.StateFailed, "已在断点处终止", time.Now()
			})
			return false
		}
		statusLine := rh.line
		if res.Edit {
			c2, reason2, h2, body2, err := capture.ParseRawResponse(res.Raw)
			if err != nil {
				r.fail(err)
				writeSimple(h.bw, 502, nil, []byte("NLR Capture: "+err.Error()+"\n"), true)
				return false
			}
			respHdr, b = h2, body2
			statusLine = fmt.Sprintf("HTTP/1.1 %d %s", c2, reason2)
			r.update(func(s *capture.Session) {
				s.Status, s.Reason, s.RespHeader, s.RespBody, s.RespBodySize = c2, reason2, h2, body2, int64(len(body2))
				s.BodyDecoded = false
			})
		}
		out := stripHopHeaders(respHdr)
		if method != "HEAD" {
			out = setLength(out, len(b))
		}
		if closeAfter {
			out = out.Set("Connection", "close")
		}
		writeHead(h.bw, statusLine, out)
		if method != "HEAD" {
			h.bw.Write(b)
		}
		err = h.bw.Flush()
		r.update(func(s *capture.Session) {
			s.Ended = time.Now()
			s.State = capture.StateDone
			if err != nil {
				s.State, s.Error = capture.StateFailed, "向客户端写入失败："+errText(err)
			}
		})
		if closeAfter {
			h.closeUp()
		}
		return !closeAfter && err == nil
	}

	out := stripHopHeaders(respHdr)
	if closeAfter && !hasToken(out, "Connection", "close") {
		out = out.Set("Connection", "close")
	}
	writeHead(h.bw, rh.line, out)
	if err := h.bw.Flush(); err != nil {
		h.closeUp()
		r.fail(fmt.Errorf("向客户端写入失败：%w", err))
		return false
	}
	recvStart := time.Now()
	respCap := &capBuffer{limit: cfg.MaxBody}
	var dst io.Writer = flushWriter{w: h.bw}
	var cw *chunkedWriter
	if rfr.chunked {
		cw = &chunkedWriter{w: h.bw}
		dst = flushWriter{w: h.bw, inner: cw}
	}
	if rate := cfg.Throttle.Down; rate > 0 {
		dst = &throttled{w: dst, rate: rate}
	}
	live := &liveBody{r: r, cap: respCap}
	rerr, werr := copyBody(dst, respBody, respCap, live)
	if cw != nil && rerr == nil && werr == nil {
		cw.Close()
		werr = h.bw.Flush()
	}
	recv := time.Since(recvStart)
	tm.Receive = recv
	r.update(func(s *capture.Session) {
		s.RespBody, s.RespBodySize, s.RespTruncated = respCap.bytes(), respCap.n, respCap.truncated()
		s.Timings = tm
		s.Ended = time.Now()
		s.State = capture.StateDone
		switch {
		case rerr != nil:
			s.State, s.Error = capture.StateFailed, "读取响应失败："+errText(rerr)
		case werr != nil:
			s.State, s.Error = capture.StateFailed, "客户端提前断开："+errText(werr)
		}
	})
	if rerr != nil || werr != nil || closeAfter {
		h.closeUp()
		return false
	}
	return true
}

// answer sends a response decided by a rule.
func (h *h1conn) answer(r *rec, v verdict, closeConn bool) bool {
	hdr := capture.Header{}
	for _, kv := range v.header {
		hdr = append(hdr, capture.Field{Name: kv[0], Value: kv[1]})
	}
	for _, kv := range v.respSet {
		hdr = hdr.Set(kv[0], kv[1])
	}
	hdr = setLength(hdr, len(v.body))
	hdr = append(hdr, capture.Field{Name: "X-NLR-Capture", Value: "AutoResponder"})
	t := time.Now()
	n, err := writeSimple(h.bw, v.status, hdr, v.body, closeConn)
	r.update(func(s *capture.Session) {
		s.Status, s.Reason, s.RespProto = v.status, capture.StatusText(v.status), "HTTP/1.1"
		s.RespHeader, s.RespHeaderSize = hdr, n
		s.RespBody, s.RespBodySize = v.body, int64(len(v.body))
		s.Timings = capture.Timings{Blocked: 0, DNS: -1, Connect: -1, TLS: -1, Send: 0, Wait: 0, Receive: time.Since(t)}
		s.Ended = time.Now()
		s.State = capture.StateDone
		if v.blocked {
			s.State = capture.StateBlocked
		}
		if err != nil {
			s.State, s.Error = capture.StateFailed, errText(err)
		}
	})
	return err == nil && !closeConn
}

// flushWriter writes through inner (or w) and flushes w after each write,
// so streamed responses reach the client as they arrive.
type flushWriter struct {
	w     *bufio.Writer
	inner io.Writer
}

func (f flushWriter) Write(p []byte) (int, error) {
	var n int
	var err error
	if f.inner != nil {
		n, err = f.inner.Write(p)
	} else {
		n, err = f.w.Write(p)
	}
	if err != nil {
		return n, err
	}
	return n, f.w.Flush()
}

// throttled limits writes to rate bytes per second.
type throttled struct {
	w     io.Writer
	rate  int64
	start time.Time
	n     int64
}

func (t *throttled) Write(p []byte) (int, error) {
	if t.start.IsZero() {
		t.start = time.Now()
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if max := int(t.rate / 10); max > 0 && len(chunk) > max {
			chunk = chunk[:max]
		}
		n, err := t.w.Write(chunk)
		written += n
		t.n += int64(n)
		if err != nil {
			return written, err
		}
		p = p[n:]
		want := time.Duration(float64(t.n) / float64(t.rate) * float64(time.Second))
		if d := want - time.Since(t.start); d > 0 {
			time.Sleep(d)
		}
	}
	return written, nil
}

// liveBody publishes a streaming body to the session now and then, so a
// long download or an event stream can be watched while it runs.
type liveBody struct {
	r    *rec
	cap  *capBuffer
	last time.Time
}

func (l *liveBody) tick() {
	if l.r.id == 0 || time.Since(l.last) < 700*time.Millisecond {
		return
	}
	if l.last.IsZero() {
		l.last = time.Now()
		return
	}
	l.last = time.Now()
	b, n := l.cap.bytes(), l.cap.n
	l.r.update(func(s *capture.Session) {
		s.RespBody, s.RespBodySize, s.RespTruncated = b, n, l.cap.truncated()
	})
}

// copyBody copies src to dst, capturing what passes, and tells read
// errors (from the server) apart from write errors (to the client).
func copyBody(dst io.Writer, src io.Reader, capt *capBuffer, live *liveBody) (rerr, werr error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			capt.Write(buf[:n])
			if _, werr = dst.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			if live != nil {
				live.tick()
			}
		}
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return err, nil
		}
	}
}
