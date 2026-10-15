// Package proxy is the capturing HTTP(S) proxy: an explicit proxy for the
// system proxy mode, and a transparent one for connections the virtual
// network adapter hands over. It intercepts TLS with certificates from
// package certs and records every exchange in a capture.Store.
package proxy

import (
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/certs"
)

// Config is the part of the settings the proxy follows. It can be replaced
// at any time with SetConfig.
type Config struct {
	Decrypt bool
	// NoDecrypt lists host patterns (wildcards allowed) whose TLS is passed
	// through untouched.
	NoDecrypt []string
	// AutoBypass passes a host through for a while after a client rejected
	// the intercepting certificate, as pinned apps do.
	AutoBypass       bool
	IgnoreCertErrors bool
	// UpstreamProxy is an HTTP proxy ("host:port" or "http://host:port")
	// that outgoing connections go through.
	UpstreamProxy string
	MaxBody       int64
	DisableCache  bool
	Throttle      Throttle
	Rules         []Rule
	Breakpoints   Breakpoints
}

type runtimeConfig struct {
	Config
	rules []compiledRule
	// breaks are the breakpoint patterns; none means every request.
	breaks []compiledRule
}

// ClientInfo describes where a connection comes from.
type ClientInfo struct {
	Addr    string
	PID     int
	Process string
	Source  string
}

// Engine is the proxy.
type Engine struct {
	Store *capture.Store
	CA    *certs.Authority
	// Lookup finds the process on the other end of an accepted connection;
	// it may be nil.
	Lookup func(c net.Conn) (pid int, name string)
	// OnPause is called when a breakpoint holds a session.
	OnPause func(id int64)
	// Version is reported in the HAR creator and the local page.
	Version string

	cfg       atomic.Pointer[runtimeConfig]
	recording atomic.Bool
	// bindIndex, when set, binds outgoing sockets to that interface so
	// they bypass the virtual adapter.
	bindIndex atomic.Uint32

	mu        sync.Mutex
	listeners []net.Listener
	paused    map[int64]chan Resume
	bypass    map[string]time.Time
	// selfAddrs are the addresses the explicit proxy listens on; requests
	// to them are answered locally instead of looping.
	selfPorts map[int]bool
	conns     map[net.Conn]struct{}
}

// New creates an engine recording into st.
func New(st *capture.Store, ca *certs.Authority) *Engine {
	e := &Engine{
		Store:     st,
		CA:        ca,
		paused:    map[int64]chan Resume{},
		bypass:    map[string]time.Time{},
		selfPorts: map[int]bool{},
		conns:     map[net.Conn]struct{}{},
	}
	e.recording.Store(true)
	e.SetConfig(Config{Decrypt: true, MaxBody: 32 << 20, AutoBypass: true})
	return e
}

// SetConfig replaces the configuration.
func (e *Engine) SetConfig(c Config) {
	if c.MaxBody <= 0 {
		c.MaxBody = 32 << 20
	}
	e.cfg.Store(&runtimeConfig{Config: c, rules: compileRules(c.Rules), breaks: compilePatterns(c.Breakpoints.Match)})
}

func (e *Engine) config() *runtimeConfig { return e.cfg.Load() }

// Config returns the current configuration.
func (e *Engine) Config() Config { return e.config().Config }

// SetRecording switches recording on or off. Traffic still flows while it
// is off; it is just not kept.
func (e *Engine) SetRecording(on bool) { e.recording.Store(on) }

// Recording reports whether new traffic is recorded.
func (e *Engine) Recording() bool { return e.recording.Load() }

// BindInterface makes outgoing connections use the interface with the
// given index (0 to stop); the virtual adapter mode needs it so that the
// proxy's own traffic does not loop back into the adapter.
func (e *Engine) BindInterface(index uint32) { e.bindIndex.Store(index) }

// Listen starts the explicit proxy on addr and serves it in the
// background.
func (e *Engine) Listen(addr string) (net.Listener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.listeners = append(e.listeners, l)
	e.selfPorts[l.Addr().(*net.TCPAddr).Port] = true
	e.mu.Unlock()
	go e.serve(l)
	return l, nil
}

// CloseListener stops one listener started by Listen.
func (e *Engine) CloseListener(l net.Listener) {
	if l == nil {
		return
	}
	l.Close()
	e.mu.Lock()
	for i, x := range e.listeners {
		if x == l {
			e.listeners = append(e.listeners[:i], e.listeners[i+1:]...)
			break
		}
	}
	delete(e.selfPorts, l.Addr().(*net.TCPAddr).Port)
	e.mu.Unlock()
}

// Close stops all listeners, open connections and paused sessions.
func (e *Engine) Close() {
	e.mu.Lock()
	ls := e.listeners
	e.listeners = nil
	conns := e.conns
	e.conns = map[net.Conn]struct{}{}
	paused := e.paused
	e.paused = map[int64]chan Resume{}
	e.mu.Unlock()
	for _, l := range ls {
		l.Close()
	}
	for c := range conns {
		c.Close()
	}
	for _, ch := range paused {
		ch <- Resume{Abort: true}
	}
}

func (e *Engine) track(c net.Conn) {
	e.mu.Lock()
	e.conns[c] = struct{}{}
	e.mu.Unlock()
}

func (e *Engine) untrack(c net.Conn) {
	e.mu.Lock()
	delete(e.conns, c)
	e.mu.Unlock()
}

func (e *Engine) serve(l net.Listener) {
	var delay time.Duration
	for {
		c, err := l.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				delay = min(max(delay*2, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			return
		}
		delay = 0
		go e.handleProxyConn(c)
	}
}

func (e *Engine) isSelf(host string, port int) bool {
	e.mu.Lock()
	self := e.selfPorts[port]
	e.mu.Unlock()
	if !self {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// ---- Recording --------------------------------------------------------

// rec is a session being recorded; id 0 means recording was off when the
// exchange began, and updates go nowhere.
type rec struct {
	e  *Engine
	id int64
}

func (e *Engine) begin(s *capture.Session) *rec {
	if !e.recording.Load() {
		return &rec{e: e}
	}
	e.Store.Add(s)
	return &rec{e: e, id: s.ID}
}

func (r *rec) update(f func(s *capture.Session)) {
	if r.id != 0 {
		r.e.Store.Update(r.id, f)
	}
}

func (r *rec) fail(err error) {
	msg := errText(err)
	r.update(func(s *capture.Session) {
		s.State = capture.StateFailed
		if s.Error == "" {
			s.Error = msg
		}
		if s.Ended.IsZero() {
			s.Ended = time.Now()
		}
	})
}

// ---- Breakpoints ------------------------------------------------------

// Resume releases a session held at a breakpoint. With Raw set, the
// message is replaced by the edited text (as produced by BreakText).
type Resume struct {
	Raw   string
	Edit  bool
	Abort bool
}

// Paused returns the IDs of sessions held at breakpoints.
func (e *Engine) Paused() []int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []int64
	for id := range e.paused {
		ids = append(ids, id)
	}
	return ids
}

// Continue releases a held session.
func (e *Engine) Continue(id int64, r Resume) bool {
	e.mu.Lock()
	ch, ok := e.paused[id]
	delete(e.paused, id)
	e.mu.Unlock()
	if ok {
		ch <- r
	}
	return ok
}

// ContinueAll releases every held session unchanged.
func (e *Engine) ContinueAll() {
	for _, id := range e.Paused() {
		e.Continue(id, Resume{})
	}
}

// hold parks the exchange until the UI resumes it. done closes when the
// client goes away, which aborts the hold.
func (e *Engine) hold(r *rec, what string, done <-chan struct{}) Resume {
	if r.id == 0 {
		return Resume{}
	}
	ch := make(chan Resume, 1)
	e.mu.Lock()
	e.paused[r.id] = ch
	e.mu.Unlock()
	r.update(func(s *capture.Session) {
		s.State = capture.StatePaused
		s.Paused = what
	})
	if e.OnPause != nil {
		e.OnPause(r.id)
	}
	var res Resume
	select {
	case res = <-ch:
	case <-done:
		e.mu.Lock()
		delete(e.paused, r.id)
		e.mu.Unlock()
		res = Resume{Abort: true}
	}
	r.update(func(s *capture.Session) {
		s.Paused = ""
		if s.State == capture.StatePaused {
			s.State = capture.StatePending
			if what == "response" {
				s.State = capture.StateReceiving
			}
		}
	})
	return res
}

func (c *runtimeConfig) breakOn(request bool, u string) bool {
	bp := c.Breakpoints
	if request && !bp.Request || !request && !bp.Response {
		return false
	}
	if len(c.breaks) == 0 {
		return true
	}
	for _, r := range c.breaks {
		if r.re.MatchString(u) {
			return true
		}
	}
	return false
}

// BreakText returns the editable text of a held message: the request or
// response with its body decoded (and Content-Encoding dropped to match).
func BreakText(s *capture.Session) string {
	if s.Paused == "response" {
		c := *s
		if body, err := capture.ResponseBody(s); err == nil && c.RespHeader.Get("Content-Encoding") != "" && !s.BodyDecoded {
			c.RespBody = body
			c.RespHeader = c.RespHeader.Del("Content-Encoding")
			c.RespHeader = setLength(c.RespHeader, len(body))
		}
		return string(capture.RawResponse(&c, false))
	}
	c := *s
	if body, err := capture.RequestBody(s); err == nil && c.ReqHeader.Get("Content-Encoding") != "" {
		c.ReqBody = body
		c.ReqHeader = c.ReqHeader.Del("Content-Encoding")
		c.ReqHeader = setLength(c.ReqHeader, len(body))
	}
	return string(capture.RawRequest(&c, false))
}

// ---- Bypass -----------------------------------------------------------

// shouldDecrypt decides whether TLS to host is intercepted.
func (e *Engine) shouldDecrypt(host string) bool {
	c := e.config()
	if !c.Decrypt || e.CA == nil {
		return false
	}
	h := strings.ToLower(host)
	for _, p := range c.NoDecrypt {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if matchHost(p, h) {
			return false
		}
	}
	if c.AutoBypass {
		e.mu.Lock()
		t, ok := e.bypass[h]
		if ok && time.Since(t) > 10*time.Minute {
			delete(e.bypass, h)
			ok = false
		}
		e.mu.Unlock()
		if ok {
			return false
		}
	}
	return true
}

func (e *Engine) addBypass(host string) {
	e.mu.Lock()
	e.bypass[strings.ToLower(host)] = time.Now()
	e.mu.Unlock()
}

// ClearBypass forgets hosts passed through after failed interception.
func (e *Engine) ClearBypass() {
	e.mu.Lock()
	e.bypass = map[string]time.Time{}
	e.mu.Unlock()
}

func matchHost(pattern, host string) bool {
	if !strings.Contains(pattern, "*") {
		return host == pattern || strings.HasSuffix(host, "."+pattern)
	}
	return MatchURL("regex:^(?i)"+strings.ReplaceAll(regexpQuote(pattern), `\*`, ".*")+"$", host)
}
