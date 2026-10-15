package ui

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/layout"

	"nlrcapture/internal/capture"
	"nlrcapture/internal/certs"
	"nlrcapture/internal/config"
	"nlrcapture/internal/procinfo"
	"nlrcapture/internal/proxy"
	"nlrcapture/internal/sysproxy"
	"nlrcapture/internal/tun"
	"nlrcapture/internal/winutil"
)

// service runs the capture machinery behind the UI: the proxy listener,
// the system proxy and the virtual adapter.
type service struct {
	a      *App
	engine *proxy.Engine
	ca     *certs.Authority
	caErr  error
	sys    *sysproxy.Manager

	listener net.Listener
	lerr     error
	cur      string // active mode; UI goroutine only
	pending  string // mode being switched to; UI goroutine only

	mu  sync.Mutex // guards dev and the switch itself
	dev *tun.Device

	dirty     atomic.Bool
	scheduled atomic.Bool
	stopped   atomic.Bool
	shutOnce  sync.Once
}

func newService(a *App) *service {
	s := &service{a: a, cur: config.ModeManual}
	s.ca, s.caErr = certs.Load(filepath.Join(a.dataDir, "certs"))
	s.engine = proxy.New(a.store, s.ca)
	s.engine.Version = Version
	s.engine.Lookup = procinfo.OfConn
	s.engine.OnPause = func(id int64) {
		a.Post(func() {
			a.list.selectID(id)
			a.details.showBreak()
			a.Toast(toastInfo, "请求已在断点处暂停，编辑后点击“继续”")
		})
	}
	s.sys = sysproxy.NewManager(a.dataDir)
	s.applyConfig()
	return s
}

// notify is called by the store on every change, from any goroutine; it
// asks for a frame at most every 50 ms.
func (s *service) notify() {
	if s.scheduled.CompareAndSwap(false, true) {
		time.AfterFunc(50*time.Millisecond, func() {
			s.scheduled.Store(false)
			s.a.host.Invalidate()
		})
	}
}

func (s *service) frame(gtx layout.Context) {}

func (s *service) start() {
	a := s.a
	if ok, err := s.sys.Recover(); ok {
		a.Toast(toastInfo, "已恢复系统代理")
	} else if err != nil {
		a.Toast(toastError, "恢复系统代理失败："+err.Error())
	}
	if s.caErr != nil {
		a.Toast(toastError, "无法创建根证书："+s.caErr.Error())
	}
	s.listen(true)
	mode := a.set.Mode
	if mode == config.ModeTUN && !winutil.IsAdmin() {
		// Elevation is asked for only when the user picks the mode.
		mode = config.ModeSystem
	}
	if mode != config.ModeManual {
		s.requestMode(mode)
	}
	if !a.set.CertAsked && s.ca != nil && a.set.Decrypt && !s.ca.Installed() {
		a.updateSettings(func(st *config.Settings) { st.CertAsked = true })
		a.Open(newCertDialog(a))
	}
}

// applyConfig hands the settings the proxy follows to it.
func (s *service) applyConfig() {
	set := s.a.set
	s.engine.SetConfig(proxy.Config{
		Decrypt:          set.Decrypt,
		NoDecrypt:        set.NoDecrypt,
		AutoBypass:       set.AutoBypass,
		IgnoreCertErrors: set.IgnoreCertErrors,
		UpstreamProxy:    set.UpstreamProxy,
		MaxBody:          int64(set.MaxBodyMB) << 20,

		Throttle:    proxy.ThrottlePresets[s.a.throttle],
		Rules:       set.Rules,
		Breakpoints: set.Breakpoints,
	})
	s.a.store.Limit = set.MaxSessions
}

func (s *service) listenAddr() string {
	host := "127.0.0.1"
	if s.a.set.ListenAll {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(s.a.set.Port))
}

// addr is the address clients should use.
func (s *service) addr() string {
	if s.a.set.ListenAll {
		return "0.0.0.0:" + strconv.Itoa(s.a.set.Port)
	}
	return "127.0.0.1:" + strconv.Itoa(s.a.set.Port)
}

func (s *service) listenError() error { return s.lerr }

// listen (re)starts the explicit proxy listener.
func (s *service) listen(retry bool) {
	if s.listener != nil {
		s.engine.CloseListener(s.listener)
		s.listener = nil
	}
	l, err := s.engine.Listen(s.listenAddr())
	// After an elevated restart the old instance may still hold the port
	// for a moment.
	for i := 0; retry && err != nil && i < 10; i++ {
		time.Sleep(300 * time.Millisecond)
		l, err = s.engine.Listen(s.listenAddr())
	}
	s.listener, s.lerr = l, err
	if err != nil {
		s.a.Toast(toastError, fmt.Sprintf("端口 %d 不可用", s.a.set.Port))
	}
}

func (s *service) mode() string        { return s.cur }
func (s *service) pendingMode() string { return s.pending }

// modeNames are the capture modes as the switch names them.
var modeNames = map[string]string{
	config.ModeManual: "仅代理端口",
	config.ModeSystem: "系统代理",
	config.ModeTUN:    "虚拟网卡",
}

// requestMode switches the capture mode in the background.
func (s *service) requestMode(m string) {
	a := s.a
	if s.pending != "" || m == s.cur {
		return
	}
	if m == config.ModeTUN {
		if !winutil.IsAdmin() {
			// Restart elevated straight away; Windows asks for consent.
			a.updateSettings(func(st *config.Settings) { st.Mode = m })
			a.Flush()
			if err := winutil.RelaunchAdmin("-mode", config.ModeTUN); err != nil {
				a.updateSettings(func(st *config.Settings) { st.Mode = s.cur })
				a.Toast(toastError, "未获得管理员权限")
				return
			}
			a.quit()
			return
		}
		if tun.FindDLL() == "" {
			a.Toast(toastError, "缺少 wintun.dll")
			return
		}
	}
	if m == config.ModeSystem && s.lerr != nil {
		a.Toast(toastError, "代理端口不可用")
		return
	}
	from := s.cur
	s.pending = m
	go func() {
		err := s.switchMode(from, m)
		a.Post(func() {
			s.pending = ""
			if err != nil {
				s.cur = config.ModeManual
				a.Toast(toastError, err.Error())
			} else {
				s.cur = m
				a.Toast(toastOK, "已切换到"+modeNames[m]+"模式")
			}
			a.updateSettings(func(st *config.Settings) { st.Mode = s.cur })
		})
	}()
}

func (s *service) switchMode(from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped.Load() {
		return errors.New("正在退出")
	}
	switch from {
	case config.ModeSystem:
		if err := s.sys.Disable(); err != nil {
			return fmt.Errorf("恢复系统代理失败：%w", err)
		}
	case config.ModeTUN:
		if s.dev != nil {
			s.dev.Close()
			s.dev = nil
		}
		s.engine.BindInterface(0)
	}
	switch to {
	case config.ModeSystem:
		if err := s.sys.Enable("127.0.0.1:"+strconv.Itoa(s.a.set.Port), s.a.set.Bypass); err != nil {
			return fmt.Errorf("设置系统代理失败：%w", err)
		}
	case config.ModeTUN:
		dev, err := tun.Start(tun.Options{
			DLLPath:   tun.FindDLL(),
			BlockQUIC: s.a.set.BlockQUIC,
			Handler:   s.transparent,
		})
		if err != nil {
			return fmt.Errorf("启动虚拟网卡失败：%w", err)
		}
		s.dev = dev
		s.engine.BindInterface(dev.PhysicalIndex())
	}
	return nil
}

// transparent hands a connection caught by the adapter to the proxy.
func (s *service) transparent(c net.Conn, src, dst netip.AddrPort) {
	pid := procinfo.Owner(src, dst)
	info := &proxy.ClientInfo{Addr: src.String(), PID: pid, Process: procinfo.Name(pid), Source: capture.SourceTUN}
	s.engine.ServeTransparent(c, dst.String(), info)
}

// reapply follows settings changes that need more than a new config:
// another port, or new system proxy values.
func (s *service) reapply(old config.Settings) {
	set := s.a.set
	s.applyConfig()
	if old.Port != set.Port || old.ListenAll != set.ListenAll {
		s.listen(false)
	}
	if s.cur == config.ModeSystem && (old.Port != set.Port || old.Bypass != set.Bypass) {
		go func() {
			s.mu.Lock()
			err := s.sys.Enable("127.0.0.1:"+strconv.Itoa(set.Port), set.Bypass)
			s.mu.Unlock()
			if err != nil {
				s.a.Post(func() { s.a.Toast(toastError, "更新系统代理失败："+err.Error()) })
			}
		}()
	}
}

// shutdown puts the system proxy and the routes back and stops the proxy.
func (s *service) shutdown() {
	s.shutOnce.Do(func() {
		s.stopped.Store(true)
		s.mu.Lock()
		s.sys.Disable()
		if s.dev != nil {
			s.dev.Close()
			s.dev = nil
		}
		s.mu.Unlock()
		s.engine.Close()
	})
}
