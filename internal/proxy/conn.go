package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"nlrcapture/internal/capture"
)

const clientBufSize = 64 << 10

func (e *Engine) clientInfo(c net.Conn, source string) *ClientInfo {
	ci := &ClientInfo{Addr: c.RemoteAddr().String(), Source: source}
	if e.Lookup != nil {
		ci.PID, ci.Process = e.Lookup(c)
	}
	return ci
}

// handleProxyConn serves one connection to the explicit proxy.
func (e *Engine) handleProxyConn(c net.Conn) {
	e.track(c)
	defer e.untrack(c)
	defer c.Close()
	ci := e.clientInfo(c, capture.SourceProxy)
	h := &h1conn{e: e, c: c, br: bufio.NewReaderSize(c, clientBufSize), bw: bufio.NewWriterSize(c, 32<<10), info: ci, scheme: "http", explicit: true}
	defer h.closeUp()
	h.serve()
}

// ServeTransparent serves a connection the virtual adapter redirected; dst
// is the address the client meant to reach.
func (e *Engine) ServeTransparent(c net.Conn, dst string, info *ClientInfo) {
	e.track(c)
	defer e.untrack(c)
	defer c.Close()
	br := bufio.NewReaderSize(c, clientBufSize)
	host, port := splitHostPort(dst, "")
	e.handleStream(c, br, dst, dst, info, port, host)
}

// handleConnect serves the stream after a CONNECT to target was accepted.
func (e *Engine) handleConnect(c net.Conn, br *bufio.Reader, target string, info *ClientInfo) {
	host, port := splitHostPort(target, "443")
	e.handleStream(c, br, net.JoinHostPort(host, port), "", info, port, host)
}

// handleStream looks at the first bytes of a stream and treats it as TLS,
// plain HTTP or anything else. hostport names the destination; fixed, if
// set, is the address to connect to.
func (e *Engine) handleStream(c net.Conn, br *bufio.Reader, hostport, fixed string, info *ClientInfo, port, host string) {
	first, ok := sniff(c, br, 3*time.Second)
	switch {
	case ok && first[0] == 0x16:
		hello, err := peekClientHello(br)
		if err != nil {
			e.tunnel(c, br, hostport, fixed, info, "")
			return
		}
		name := hello.sni
		if name == "" {
			name = host
		}
		if !e.shouldDecrypt(name) {
			e.tunnel(c, br, net.JoinHostPort(name, port), fixed, info, name)
			return
		}
		e.intercept(c, br, net.JoinHostPort(name, port), fixed, hello, info)
	case ok && looksHTTP(first):
		h := &h1conn{e: e, c: c, br: br, bw: bufio.NewWriterSize(c, 32<<10), info: info, scheme: "http", defHost: hostport, fixed: fixed}
		if port == "80" {
			h.defHost = host
		}
		defer h.closeUp()
		h.serve()
	default:
		e.tunnel(c, br, hostport, fixed, info, "")
	}
}

// intercept terminates the client's TLS with a certificate for the host
// and serves the decrypted stream. The connection to the server is opened
// during the handshake, so the client can be offered the protocol (h2 or
// HTTP/1.1) the server picked.
func (e *Engine) intercept(c net.Conn, br *bufio.Reader, hostport, fixed string, hello *clientHello, info *ClientInfo) {
	host, _ := splitHostPort(hostport, "443")
	var up *upConn
	var upErr error
	started := time.Now()
	conf := &tls.Config{
		MinVersion: tls.VersionTLS10,
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			var offer []string
			for _, p := range chi.SupportedProtos {
				if p == "h2" || p == "http/1.1" {
					offer = append(offer, p)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			up, upErr = e.dialTLS(ctx, hostport, fixed, host, offer)
			cancel()
			cert, err := e.CA.Leaf(host)
			if err != nil {
				return nil, err
			}
			cc := &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS10}
			if len(chi.SupportedProtos) > 0 {
				p := "http/1.1"
				if up != nil && up.alpn == "h2" {
					p = "h2"
				} else if upErr != nil && slices.Contains(offer, "h2") && !slices.Contains(offer, "http/1.1") {
					p = "h2"
				}
				cc.NextProtos = []string{p}
			}
			return cc, nil
		},
	}
	tc := tls.Server(&peekedConn{Conn: c, r: br}, conf)
	tc.SetDeadline(time.Now().Add(30 * time.Second))
	err := tc.Handshake()
	tc.SetDeadline(time.Time{})
	if err != nil {
		if up != nil {
			up.Close()
		}
		e.handshakeFailed(hostport, host, info, started, err)
		return
	}
	if tc.ConnectionState().NegotiatedProtocol == "h2" && (up == nil || up.alpn == "h2") {
		e.serveH2(tc, up, upErr, hostport, fixed, host, info)
		return
	}
	if up != nil && up.alpn == "h2" {
		// The client settled on HTTP/1.1 after all; the server spoke h2.
		up.Close()
		up, upErr = nil, nil
	}
	h := &h1conn{
		e: e, c: tc, br: bufio.NewReaderSize(tc, clientBufSize), bw: bufio.NewWriterSize(tc, 32<<10),
		info: info, scheme: "https", defHost: hostport, fixed: fixed, sni: host,
		up: up, upScheme: "https", upErr: upErr,
	}
	if strings.HasSuffix(hostport, ":443") {
		h.defHost = host
	}
	defer h.closeUp()
	h.serve()
}

// handshakeFailed records a client that refused the intercepting
// certificate, and passes the host through for a while if configured.
func (e *Engine) handshakeFailed(hostport, host string, info *ClientInfo, started time.Time, err error) {
	msg := err.Error()
	why := "客户端在 TLS 握手中断开连接"
	switch {
	case strings.Contains(msg, "unknown certificate authority"), strings.Contains(msg, "unknown ca"):
		why = "客户端不信任根证书"
	case strings.Contains(msg, "bad certificate"), strings.Contains(msg, "certificate unknown"):
		why = "客户端拒绝了证书"
	case errors.Is(err, io.EOF), strings.Contains(msg, "connection reset"), strings.Contains(msg, "forcibly closed"):
		why = "客户端在 TLS 握手中断开"
	case strings.Contains(msg, "timeout"):
		why = "TLS 握手超时"
	}
	if e.config().AutoBypass {
		e.addBypass(host)
		why += "，已临时直通"
	}
	s := &capture.Session{
		Kind: capture.KindTunnel, State: capture.StateFailed, Source: info.Source,
		Started: started, Ended: time.Now(), Method: "CONNECT", URL: hostport, Host: hostport,
		Scheme: "https", ClientAddr: info.Addr, Process: info.Process, PID: info.PID,
		Error: why, Timings: capture.NoTimings(),
		TLS: &capture.TLSInfo{ServerName: host, ClientError: msg},
	}
	e.begin(s)
}

// tunnel relays bytes between the client and hostport without looking at
// them, recording the volume.
func (e *Engine) tunnel(c net.Conn, br *bufio.Reader, hostport, fixed string, info *ClientInfo, sni string) {
	started := time.Now()
	scheme := ""
	if sni != "" {
		scheme = "https"
	}
	s := &capture.Session{
		Kind: capture.KindTunnel, State: capture.StatePending, Source: info.Source,
		Started: started, Method: "CONNECT", URL: hostport, Host: hostport, Scheme: scheme,
		ClientAddr: info.Addr, Process: info.Process, PID: info.PID,
		Timings: capture.NoTimings(),
	}
	if sni != "" {
		s.TLS = &capture.TLSInfo{ServerName: sni}
	}
	r := e.begin(s)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	up, err := e.dialRaw(ctx, hostport, fixed)
	cancel()
	if err != nil {
		r.fail(err)
		return
	}
	defer up.Close()
	r.update(func(s *capture.Session) {
		s.ServerAddr = up.addr
		s.Timings.DNS, s.Timings.Connect = up.timings.DNS, up.timings.Connect
		s.State = capture.StateReceiving
	})
	var upN, downN countWriter
	done := make(chan struct{})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	go func() {
		// Buffered bytes from the sniffing go first.
		io.Copy(io.MultiWriter(up.Conn, &upN), br)
		if cw, ok := up.Conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		close(done)
	}()
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.MultiWriter(c, &downN), up.br)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			c.Close()
		}
		copyDone <- err
	}()
	finish := func() {
		r.update(func(s *capture.Session) {
			s.BytesUp, s.BytesDown = upN.n.Load(), downN.n.Load()
			s.RespBodySize = s.BytesDown
			s.ReqBodySize = s.BytesUp
			s.Ended = time.Now()
			if s.State != capture.StateFailed {
				s.State = capture.StateDone
			}
		})
	}
	var last [2]int64
	for {
		select {
		case <-ticker.C:
			u, d := upN.n.Load(), downN.n.Load()
			if u != last[0] || d != last[1] {
				last = [2]int64{u, d}
				r.update(func(s *capture.Session) { s.BytesUp, s.BytesDown = u, d })
			}
		case <-copyDone:
			// The server closed; give the client side a moment to drain.
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				c.Close()
				up.Close()
				<-done
			}
			finish()
			return
		case <-done:
			select {
			case <-copyDone:
			case <-time.After(30 * time.Second):
				up.Close()
				c.Close()
				<-copyDone
			}
			finish()
			return
		}
	}
}

type countWriter struct{ n atomicInt64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), nil
}

// connectOK answers a CONNECT request.
func connectOK(w *bufio.Writer) error {
	w.WriteString("HTTP/1.1 200 Connection Established\r\nProxy-Agent: NLR Capture\r\n\r\n")
	return w.Flush()
}

// writeSimple writes a complete response with a text body.
func writeSimple(w *bufio.Writer, code int, h capture.Header, body []byte, closeConn bool) (int, error) {
	reason := capture.StatusText(code)
	if h.Get("Content-Type") == "" && len(body) > 0 {
		h = append(h, capture.Field{Name: "Content-Type", Value: "text/plain; charset=utf-8"})
	}
	h = setLength(h, len(body))
	if closeConn {
		h = h.Set("Connection", "close")
	}
	n := writeHead(w, fmt.Sprintf("HTTP/1.1 %d %s", code, reason), h)
	w.Write(body)
	return n, w.Flush()
}
