package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"nlrcapture/internal/capture"
)

// h2conn serves an intercepted HTTP/2 connection. Every stream goes to
// the server over one HTTP/2 connection of its own, opened during the
// client's TLS handshake.
type h2conn struct {
	e        *Engine
	info     *ClientInfo
	hostport string
	fixed    string
	sni      string

	mu    sync.Mutex
	cc    *http2.ClientConn
	up    *upConn
	err   error
	fresh bool
}

func (e *Engine) serveH2(tc *tls.Conn, up *upConn, upErr error, hostport, fixed, sni string, info *ClientInfo) {
	hc := &h2conn{e: e, info: info, hostport: hostport, fixed: fixed, sni: sni, err: upErr}
	if up != nil {
		if err := hc.attach(up); err != nil {
			hc.err = err
		}
	}
	srv := &http2.Server{MaxConcurrentStreams: 250, IdleTimeout: 5 * time.Minute, MaxReadFrameSize: 1 << 20}
	srv.ServeConn(tc, &http2.ServeConnOpts{Handler: http.HandlerFunc(hc.handle)})
	hc.mu.Lock()
	if hc.cc != nil {
		hc.cc.Close()
	}
	hc.mu.Unlock()
}

func (hc *h2conn) attach(up *upConn) error {
	t := &http2.Transport{DisableCompression: true, ReadIdleTimeout: 60 * time.Second, PingTimeout: 15 * time.Second}
	cc, err := t.NewClientConn(up.Conn)
	if err != nil {
		up.Close()
		return err
	}
	hc.cc, hc.up, hc.fresh = cc, up, true
	return nil
}

// client returns the connection to the server and, for the first stream
// on a new one, its opening timings.
func (hc *h2conn) client(started time.Time) (*http2.ClientConn, *upConn, capture.Timings, error) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.cc != nil && hc.cc.CanTakeNewRequest() {
		tm := hc.up.openTimings(hc.fresh, started)
		hc.fresh = false
		return hc.cc, hc.up, tm, nil
	}
	if hc.cc == nil && hc.err != nil {
		err := hc.err
		hc.err = nil
		return nil, nil, capture.Timings{}, err
	}
	if hc.cc != nil {
		hc.cc.Close()
		hc.cc = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	up, err := hc.e.dialTLS(ctx, hc.hostport, hc.fixed, hc.sni, []string{"h2"})
	if err != nil {
		return nil, nil, capture.Timings{}, err
	}
	if up.alpn != "h2" {
		up.Close()
		return nil, nil, capture.Timings{}, errors.New("服务器不再提供 HTTP/2")
	}
	if err := hc.attach(up); err != nil {
		return nil, nil, capture.Timings{}, err
	}
	hc.fresh = false
	return hc.cc, hc.up, up.timings, nil
}

// h2Header turns net/http headers into the ordered, lowercase form HTTP/2
// uses on the wire, after the given pseudo-headers.
func h2Header(pseudo [][2]string, h http.Header) capture.Header {
	out := make(capture.Header, 0, len(pseudo)+len(h))
	for _, p := range pseudo {
		out = append(out, capture.Field{Name: p[0], Value: p[1]})
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			out = append(out, capture.Field{Name: strings.ToLower(k), Value: v})
		}
	}
	return out
}

var h2Hop = map[string]bool{
	"connection": true, "proxy-connection": true, "keep-alive": true,
	"transfer-encoding": true, "upgrade": true, "host": true,
}

// toHTTPHeader turns recorded fields back into net/http headers, without
// pseudo-headers and connection-specific fields.
func toHTTPHeader(h capture.Header) http.Header {
	out := http.Header{}
	for _, f := range h {
		n := strings.ToLower(f.Name)
		if strings.HasPrefix(n, ":") || h2Hop[n] {
			continue
		}
		if n == "te" && !strings.EqualFold(f.Value, "trailers") {
			continue
		}
		out[http.CanonicalHeaderKey(f.Name)] = append(out[http.CanonicalHeaderKey(f.Name)], f.Value)
	}
	return out
}

func abortStream() { panic(http.ErrAbortHandler) }

func (hc *h2conn) handle(w http.ResponseWriter, req *http.Request) {
	e := hc.e
	cfg := e.config()
	started := time.Now()
	authority := req.Host
	if authority == "" {
		authority = hc.sni
	}
	fullURL := "https://" + authority + req.URL.RequestURI()
	pseudo := [][2]string{{":method", req.Method}, {":authority", authority}, {":scheme", "https"}, {":path", req.URL.RequestURI()}}
	reqHdr := h2Header(pseudo, req.Header)
	s := &capture.Session{
		Kind: capture.KindHTTP, State: capture.StatePending, Source: hc.info.Source,
		Started: started, Method: req.Method, URL: fullURL, Proto: "h2", Scheme: "https", Host: authority,
		ClientAddr: hc.info.Addr, Process: hc.info.Process, PID: hc.info.PID,
		ReqHeader: reqHdr, ReqHeaderSize: reqHdr.Size(), Timings: capture.NoTimings(), Decrypted: true,
	}
	if hc.up != nil {
		s.TLS = hc.up.tls
	}
	r := e.begin(s)
	reqCap := &capBuffer{limit: cfg.MaxBody}
	fwd := reqHdr
	method := req.Method

	v := evaluate(cfg.rules, method, fullURL)
	if v.rule != "" {
		r.update(func(s *capture.Session) { s.Rule = v.rule })
	}
	if v.delay > 0 {
		time.Sleep(v.delay)
	}
	for _, kv := range v.reqSet {
		fwd = fwd.Set(strings.ToLower(kv[0]), kv[1])
	}
	if cfg.DisableCache {
		fwd = fwd.Del("If-None-Match").Del("If-Modified-Since").Set("cache-control", "no-cache").Set("pragma", "no-cache")
	}
	if cfg.Throttle.Offline {
		r.update(func(s *capture.Session) {
			s.State, s.Error, s.Ended = capture.StateFailed, "离线", time.Now()
		})
		abortStream()
	}
	if v.final() {
		io.Copy(reqCap, io.LimitReader(req.Body, 256<<20))
		r.update(func(s *capture.Session) {
			s.ReqBody, s.ReqBodySize, s.ReqTruncated = reqCap.bytes(), reqCap.n, reqCap.truncated()
		})
		if v.drop {
			r.update(func(s *capture.Session) {
				s.State, s.Error, s.Ended = capture.StateBlocked, "已被规则断开", time.Now()
			})
			abortStream()
		}
		hdr := capture.Header{}
		for _, kv := range v.header {
			w.Header().Set(kv[0], kv[1])
		}
		for _, kv := range v.respSet {
			w.Header().Set(kv[0], kv[1])
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(v.body)))
		w.Header().Set("X-NLR-Capture", "AutoResponder")
		w.WriteHeader(v.status)
		w.Write(v.body)
		hdr = h2Header([][2]string{{":status", strconv.Itoa(v.status)}}, w.Header())
		r.update(func(s *capture.Session) {
			s.Status, s.Reason, s.RespProto = v.status, capture.StatusText(v.status), "h2"
			s.RespHeader, s.RespHeaderSize = hdr, hdr.Size()
			s.RespBody, s.RespBodySize = v.body, int64(len(v.body))
			s.Timings = capture.Timings{Blocked: 0, DNS: -1, Connect: -1, TLS: -1}
			s.Ended = time.Now()
			s.State = capture.StateDone
			if v.blocked {
				s.State = capture.StateBlocked
			}
		})
		return
	}

	upURL := fullURL
	if v.redirect != "" {
		upURL = v.redirect
		r.update(func(s *capture.Session) { s.Rule += " → " + v.redirect })
	}
	var body io.Reader = req.Body
	length := req.ContentLength
	buffered := false
	if r.id != 0 && cfg.breakOn(true, fullURL) {
		b, err := io.ReadAll(io.LimitReader(req.Body, 256<<20))
		if err != nil {
			r.fail(err)
			abortStream()
		}
		r.update(func(s *capture.Session) { s.ReqBody, s.ReqBodySize = b, int64(len(b)) })
		res := e.hold(r, "request", req.Context().Done())
		if res.Abort {
			r.update(func(s *capture.Session) {
				s.State, s.Error, s.Ended = capture.StateFailed, "已在断点处终止", time.Now()
			})
			abortStream()
		}
		if res.Edit {
			m2, t2, _, h2, body2, err := capture.ParseRawRequest(res.Raw)
			if err != nil {
				r.fail(err)
				http.Error(w, "NLR Capture: "+err.Error(), http.StatusBadRequest)
				return
			}
			method = m2
			if u, err := url.Parse(t2); err == nil && u.Host != "" {
				upURL = t2
			} else {
				u, _ := url.Parse(upURL)
				upURL = u.Scheme + "://" + u.Host + t2
			}
			fwd = h2
			b = body2
			newURL := upURL
			r.update(func(s *capture.Session) {
				s.Method, s.URL, s.ReqHeader, s.ReqBody, s.ReqBodySize = m2, newURL, h2, body2, int64(len(body2))
			})
		}
		body, length, buffered = bytes.NewReader(b), int64(len(b)), true
		reqCap.Write(b)
	}
	if !buffered {
		body = io.TeeReader(body, reqCap)
	}
	if rate := cfg.Throttle.Up; rate > 0 {
		body = &throttledReader{r: body, rate: rate}
	}
	if t := cfg.Throttle.Latency; t > 0 {
		time.Sleep(t)
	}

	u, err := url.Parse(upURL)
	if err != nil {
		r.fail(err)
		http.Error(w, "NLR Capture: "+err.Error(), http.StatusBadGateway)
		return
	}
	dialStart := time.Now()
	var resp *http.Response
	var tm capture.Timings
	var sendDone, firstByte time.Time
	trace := &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { sendDone = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	if strings.EqualFold(u.Host, authority) && u.Scheme == "https" {
		cc, up, t, err2 := hc.client(started)
		if err2 != nil {
			err = err2
		} else {
			tm = t
			r.update(func(s *capture.Session) { s.ServerAddr, s.TLS = up.addr, up.tls })
			out, _ := http.NewRequestWithContext(httptrace.WithClientTrace(req.Context(), trace), method, upURL, body)
			out.Header = toHTTPHeader(fwd)
			out.Host = authority
			out.ContentLength = length
			if length == 0 && !buffered {
				out.Body = http.NoBody
			}
			resp, err = cc.RoundTrip(out)
		}
	} else {
		// A rule sent the request elsewhere: one HTTP/1.1 exchange.
		var res *h1result
		res, err = e.roundTripH1(req.Context(), method, upURL, fwd, body, length)
		if err == nil {
			tm = res.tm
			sendDone = res.sendDone
			firstByte = res.firstByte
			resp = res.response()
			r.update(func(s *capture.Session) { s.ServerAddr, s.TLS = res.up.addr, res.up.tls })
			defer res.up.Close()
		}
	}
	if err != nil {
		r.update(func(s *capture.Session) {
			s.ReqBody, s.ReqBodySize, s.ReqTruncated = reqCap.bytes(), reqCap.n, reqCap.truncated()
		})
		r.fail(err)
		http.Error(w, "NLR Capture: "+errText(err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	now := time.Now()
	if sendDone.IsZero() {
		sendDone = now
	}
	if firstByte.IsZero() {
		firstByte = now
	}
	tm.Blocked = dialStart.Sub(started)
	connectEnd := dialStart
	if tm.Connect > 0 {
		connectEnd = connectEnd.Add(tm.Connect)
	}
	if tm.DNS > 0 {
		connectEnd = connectEnd.Add(tm.DNS)
	}
	tm.Send = max(sendDone.Sub(connectEnd), 0)
	tm.Wait = max(firstByte.Sub(sendDone), 0)

	respPseudo := [][2]string{{":status", strconv.Itoa(resp.StatusCode)}}
	respHdr := h2Header(respPseudo, resp.Header)
	if resp.ProtoMajor == 1 {
		respHdr = h2Header(nil, resp.Header)
	}
	for _, kv := range v.respSet {
		respHdr = respHdr.Set(strings.ToLower(kv[0]), kv[1])
	}
	respProto := "h2"
	if resp.ProtoMajor == 1 {
		respProto = fmt.Sprintf("HTTP/%d.%d", resp.ProtoMajor, resp.ProtoMinor)
	}
	r.update(func(s *capture.Session) {
		s.ReqBody, s.ReqBodySize, s.ReqTruncated = reqCap.bytes(), reqCap.n, reqCap.truncated()
		s.Status, s.Reason, s.RespProto = resp.StatusCode, capture.StatusText(resp.StatusCode), respProto
		s.RespHeader, s.RespHeaderSize = respHdr, respHdr.Size()
		s.Timings = tm
		s.State = capture.StateReceiving
	})

	writeHeaders := func(h capture.Header, code int) {
		for k, vs := range toHTTPHeader(h) {
			w.Header()[k] = vs
		}
		for k := range resp.Trailer {
			w.Header().Add("Trailer", k)
		}
		w.WriteHeader(code)
	}

	if r.id != 0 && cfg.breakOn(false, fullURL) {
		b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		if err != nil {
			r.fail(err)
			abortStream()
		}
		r.update(func(s *capture.Session) {
			s.RespBody, s.RespBodySize = b, int64(len(b))
			s.Timings.Receive = time.Since(firstByte)
		})
		res := e.hold(r, "response", req.Context().Done())
		if res.Abort {
			r.update(func(s *capture.Session) {
				s.State, s.Error, s.Ended = capture.StateFailed, "已在断点处终止", time.Now()
			})
			abortStream()
		}
		code := resp.StatusCode
		hdr := respHdr
		if res.Edit {
			c2, reason2, h2, body2, err := capture.ParseRawResponse(res.Raw)
			if err != nil {
				r.fail(err)
				abortStream()
			}
			code, hdr, b = c2, h2, body2
			r.update(func(s *capture.Session) {
				s.Status, s.Reason, s.RespHeader, s.RespBody, s.RespBodySize = c2, reason2, h2, body2, int64(len(body2))
				s.BodyDecoded = false
			})
		}
		hdr = hdr.Del("content-length")
		hdr = append(hdr, capture.Field{Name: "content-length", Value: strconv.Itoa(len(b))})
		writeHeaders(hdr, code)
		_, err = w.Write(b)
		r.update(func(s *capture.Session) {
			s.Ended, s.State = time.Now(), capture.StateDone
			if err != nil {
				s.State, s.Error = capture.StateFailed, "向客户端写入失败："+errText(err)
			}
		})
		return
	}

	writeHeaders(respHdr, resp.StatusCode)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	respCap := &capBuffer{limit: cfg.MaxBody}
	var dst io.Writer = h2FlushWriter{w: w, f: fl}
	if rate := cfg.Throttle.Down; rate > 0 {
		dst = &throttled{w: dst, rate: rate}
	}
	recvStart := time.Now()
	rerr, werr := copyBody(dst, resp.Body, respCap, &liveBody{r: r, cap: respCap})
	for k, vs := range resp.Trailer {
		w.Header()[k] = vs
	}
	trailers := h2Header(nil, resp.Trailer)
	tm.Receive = time.Since(recvStart)
	r.update(func(s *capture.Session) {
		s.RespBody, s.RespBodySize, s.RespTruncated = respCap.bytes(), respCap.n, respCap.truncated()
		s.RespHeader = append(s.RespHeader.Clone(), trailers...)
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
	if rerr != nil {
		abortStream()
	}
}

type h2FlushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (h h2FlushWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	if err == nil && h.f != nil {
		h.f.Flush()
	}
	return n, err
}

// throttledReader limits reads to rate bytes per second.
type throttledReader struct {
	r     io.Reader
	rate  int64
	start time.Time
	n     int64
}

func (t *throttledReader) Read(p []byte) (int, error) {
	if t.start.IsZero() {
		t.start = time.Now()
	}
	if max := int(t.rate / 10); max > 0 && len(p) > max {
		p = p[:max]
	}
	n, err := t.r.Read(p)
	t.n += int64(n)
	want := time.Duration(float64(t.n) / float64(t.rate) * float64(time.Second))
	if d := want - time.Since(t.start); d > 0 {
		time.Sleep(d)
	}
	return n, err
}
