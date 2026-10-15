package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/netx"
)

// upConn is a connection to a server (or to the upstream proxy).
type upConn struct {
	net.Conn
	br *bufio.Reader
	bw *bufio.Writer
	// host is the host:port it was opened for; addr the remote address.
	host string
	addr string
	// viaProxy marks a plain connection to the upstream proxy: requests
	// on it use the absolute form.
	viaProxy bool
	alpn     string
	tls      *capture.TLSInfo
	// timings of opening the connection; consumed by the first exchange.
	timings capture.Timings
	// opened is when the connection to the server was made.
	opened time.Time
	fresh  bool
	closed bool
}

func (u *upConn) Close() error {
	u.closed = true
	return u.Conn.Close()
}

func newUpConn(c net.Conn, host, addr string) *upConn {
	return &upConn{Conn: c, br: bufio.NewReaderSize(c, 32<<10), bw: bufio.NewWriterSize(c, 32<<10), host: host, addr: addr, opened: time.Now(), fresh: true}
}

// take returns the opening timings for the first exchange on the
// connection, started at the given time, and marks them used.
func (u *upConn) take(started time.Time) capture.Timings {
	fresh := u.fresh
	u.fresh = false
	return u.openTimings(fresh, started)
}

// openTimings returns the timings of opening the connection for an
// exchange started at the given time. A connection made before the
// exchange began, such as one opened while the client was still
// negotiating TLS, is not part of it: the exchange finds it ready, as it
// would a reused one.
func (u *upConn) openTimings(fresh bool, started time.Time) capture.Timings {
	if !fresh || u.opened.Before(started) {
		return capture.Timings{Blocked: 0, DNS: -1, Connect: -1, TLS: -1}
	}
	return u.timings
}

const dialTimeout = 15 * time.Second

func (e *Engine) dialer() *net.Dialer {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	if idx := e.bindIndex.Load(); idx != 0 {
		d.Control = netx.BindControl(idx)
	}
	return d
}

func splitHostPort(hostport, defPort string) (string, string) {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.Trim(hostport, "[]"), defPort
	}
	return h, p
}

// dialTCP opens a TCP connection to hostport, or to fixed (ip:port) when
// set, and reports the DNS and connect times.
func (e *Engine) dialTCP(ctx context.Context, hostport, fixed string) (net.Conn, time.Duration, time.Duration, error) {
	d := e.dialer()
	if fixed != "" {
		t := time.Now()
		c, err := d.DialContext(ctx, "tcp", fixed)
		return c, -1, time.Since(t), err
	}
	host, port := splitHostPort(hostport, "80")
	var ips []net.IP
	dns := time.Duration(-1)
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		t := time.Now()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		dns = time.Since(t)
		if err != nil {
			return nil, dns, -1, err
		}
		// IPv4 first: the IPv6 route is the one more often broken.
		for _, a := range addrs {
			if a.IP.To4() != nil {
				ips = append(ips, a.IP)
			}
		}
		for _, a := range addrs {
			if a.IP.To4() == nil {
				ips = append(ips, a.IP)
			}
		}
	}
	t := time.Now()
	var lastErr error
	for i, ip := range ips {
		cctx := ctx
		var cancel context.CancelFunc
		if len(ips) > 1 && i < len(ips)-1 {
			cctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		}
		c, err := d.DialContext(cctx, "tcp", net.JoinHostPort(ip.String(), port))
		if cancel != nil {
			cancel()
		}
		if err == nil {
			return c, dns, time.Since(t), nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s 没有地址", host)
	}
	return nil, dns, time.Since(t), lastErr
}

// upstreamProxy returns the configured upstream proxy address, if any.
func (e *Engine) upstreamProxy() string {
	p := strings.TrimSpace(e.config().UpstreamProxy)
	p = strings.TrimPrefix(strings.TrimPrefix(p, "http://"), "HTTP://")
	return strings.TrimSuffix(p, "/")
}

// dialRaw opens a byte stream to hostport, through the upstream proxy's
// CONNECT when one is set.
func (e *Engine) dialRaw(ctx context.Context, hostport, fixed string) (*upConn, error) {
	start := time.Now()
	if up := e.upstreamProxy(); up != "" {
		c, dns, conn, err := e.dialTCP(ctx, up, "")
		if err != nil {
			return nil, fmt.Errorf("连接上游代理 %s 失败：%w", up, err)
		}
		u := newUpConn(c, hostport, c.RemoteAddr().String())
		target := hostport
		if fixed != "" {
			target = fixed
		}
		fmt.Fprintf(u.bw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		if err := u.bw.Flush(); err != nil {
			c.Close()
			return nil, err
		}
		h, err := readHead(u.br)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("上游代理：%w", err)
		}
		if _, code, reason, err := statusLine(h.line); err != nil || code/100 != 2 {
			c.Close()
			if err == nil {
				err = fmt.Errorf("上游代理拒绝 CONNECT：%d %s", code, reason)
			}
			return nil, err
		}
		u.timings = capture.Timings{Blocked: 0, DNS: dns, Connect: time.Since(start) - max(dns, 0), TLS: -1}
		_ = conn
		return u, nil
	}
	c, dns, conn, err := e.dialTCP(ctx, hostport, fixed)
	if err != nil {
		return nil, err
	}
	u := newUpConn(c, hostport, c.RemoteAddr().String())
	u.timings = capture.Timings{Blocked: 0, DNS: dns, Connect: conn, TLS: -1}
	return u, nil
}

// dialHTTP opens a connection for plain HTTP requests to hostport.
func (e *Engine) dialHTTP(ctx context.Context, hostport, fixed string) (*upConn, error) {
	if up := e.upstreamProxy(); up != "" && fixed == "" {
		c, dns, conn, err := e.dialTCP(ctx, up, "")
		if err != nil {
			return nil, fmt.Errorf("连接上游代理 %s 失败：%w", up, err)
		}
		u := newUpConn(c, hostport, c.RemoteAddr().String())
		u.viaProxy = true
		u.timings = capture.Timings{Blocked: 0, DNS: dns, Connect: conn, TLS: -1}
		return u, nil
	}
	return e.dialRaw(ctx, hostport, fixed)
}

// dialTLS opens a TLS connection to hostport offering the given protocols.
func (e *Engine) dialTLS(ctx context.Context, hostport, fixed, sni string, alpn []string) (*upConn, error) {
	u, err := e.dialRaw(ctx, hostport, fixed)
	if err != nil {
		return nil, err
	}
	if sni == "" {
		sni, _ = splitHostPort(hostport, "443")
	}
	conf := &tls.Config{
		ServerName:         sni,
		NextProtos:         alpn,
		InsecureSkipVerify: e.config().IgnoreCertErrors,
		MinVersion:         tls.VersionTLS10,
	}
	if net.ParseIP(sni) != nil {
		// No SNI for addresses; verify against the IP.
		conf.ServerName = sni
	}
	t := time.Now()
	tc := tls.Client(u.Conn, conf)
	hctx, cancel := context.WithTimeout(ctx, dialTimeout)
	err = tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		u.Conn.Close()
		return nil, tlsError(err, sni)
	}
	st := tc.ConnectionState()
	u.Conn = tc
	u.br = bufio.NewReaderSize(tc, 32<<10)
	u.bw = bufio.NewWriterSize(tc, 32<<10)
	u.alpn = st.NegotiatedProtocol
	u.timings.TLS = time.Since(t)
	u.timings.Connect = max(u.timings.Connect, 0) + u.timings.TLS
	u.tls = tlsInfo(st, sni)
	return u, nil
}

func tlsInfo(st tls.ConnectionState, sni string) *capture.TLSInfo {
	ti := &capture.TLSInfo{
		Version:    tls.VersionName(st.Version),
		Cipher:     tls.CipherSuiteName(st.CipherSuite),
		ServerName: sni,
		ALPN:       st.NegotiatedProtocol,
	}
	if len(st.PeerCertificates) > 0 {
		c := st.PeerCertificates[0]
		ti.Subject = c.Subject.String()
		ti.Issuer = c.Issuer.String()
		ti.NotBefore, ti.NotAfter = c.NotBefore, c.NotAfter
		ti.SANs = append(ti.SANs, c.DNSNames...)
		for _, ip := range c.IPAddresses {
			ti.SANs = append(ti.SANs, ip.String())
		}
	}
	return ti
}

func tlsError(err error, host string) error {
	var ue x509.UnknownAuthorityError
	var he x509.HostnameError
	var ce x509.CertificateInvalidError
	switch {
	case errors.As(err, &ue):
		return fmt.Errorf("服务器证书不受信任：%v", err)
	case errors.As(err, &he):
		return fmt.Errorf("服务器证书与主机名不符：%v", err)
	case errors.As(err, &ce):
		return fmt.Errorf("服务器证书无效：%v", err)
	}
	return fmt.Errorf("与服务器的 TLS 握手失败：%w", err)
}

// errText turns an error into a short message.
func errText(err error) string {
	if err == nil {
		return ""
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "已取消"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "连接意外关闭"
	case errors.Is(err, syscall.ECONNRESET) || strings.Contains(err.Error(), "forcibly closed") || strings.Contains(err.Error(), "wsarecv: An existing connection"):
		return "连接被重置"
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "actively refused"):
		return "连接被拒绝：" + err.Error()
	case errors.As(err, &ne) && ne.Timeout():
		return "超时：" + err.Error()
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return "域名解析失败：" + de.Name
	}
	return err.Error()
}

func setLength(h capture.Header, n int) capture.Header {
	h = h.Del("Transfer-Encoding")
	return h.Set("Content-Length", strconv.Itoa(n))
}

func regexpQuote(s string) string { return regexp.QuoteMeta(s) }
