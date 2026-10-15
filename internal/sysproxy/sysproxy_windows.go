// Package sysproxy sets and restores the Windows (WinINet) system proxy,
// which browsers and most programs follow.
package sysproxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wininet         = windows.NewLazySystemDLL("wininet.dll")
	procSetOption   = wininet.NewProc("InternetSetOptionW")
	procQueryOption = wininet.NewProc("InternetQueryOptionW")
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalFree  = kernel32.NewProc("GlobalFree")
)

const (
	optPerConnection    = 75
	optSettingsChanged  = 39
	optRefresh          = 37
	optProxySettingsChg = 95

	perConnFlags        = 1
	perConnProxyServer  = 2
	perConnProxyBypass  = 3
	perConnAutoConfig   = 4
	perConnAutoDiscover = 5

	flagDirect     = 1
	flagProxy      = 2
	flagAutoURL    = 4
	flagAutoDetect = 8
)

// perConnOption mirrors INTERNET_PER_CONN_OPTIONW: a DWORD option and an
// 8-byte union (DWORD, string pointer or FILETIME).
type perConnOption struct {
	option uint32
	_      uint32
	value  uintptr
}

// perConnList mirrors INTERNET_PER_CONN_OPTION_LISTW.
type perConnList struct {
	size        uint32
	_           uint32
	connection  *uint16
	optionCount uint32
	optionError uint32
	options     *perConnOption
}

// Settings are the proxy settings of the default connection.
type Settings struct {
	Flags   uint32 `json:"flags"`
	Server  string `json:"server"`
	Bypass  string `json:"bypass"`
	AutoURL string `json:"autoUrl"`
}

// Current reads the system proxy settings.
func Current() (Settings, error) {
	opts := []perConnOption{{option: perConnFlags}, {option: perConnProxyServer}, {option: perConnProxyBypass}, {option: perConnAutoConfig}}
	list := perConnList{optionCount: uint32(len(opts)), options: &opts[0]}
	list.size = uint32(unsafe.Sizeof(list))
	size := list.size
	r, _, err := procQueryOption.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return Settings{}, err
	}
	str := func(p uintptr) string {
		if p == 0 {
			return ""
		}
		s := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p)))
		procGlobalFree.Call(p)
		return s
	}
	return Settings{
		Flags:   uint32(opts[0].value),
		Server:  str(opts[1].value),
		Bypass:  str(opts[2].value),
		AutoURL: str(opts[3].value),
	}, nil
}

func apply(s Settings) error {
	server, _ := windows.UTF16PtrFromString(s.Server)
	bypass, _ := windows.UTF16PtrFromString(s.Bypass)
	auto, _ := windows.UTF16PtrFromString(s.AutoURL)
	opts := []perConnOption{
		{option: perConnFlags, value: uintptr(s.Flags)},
		{option: perConnProxyServer, value: uintptr(unsafe.Pointer(server))},
		{option: perConnProxyBypass, value: uintptr(unsafe.Pointer(bypass))},
		{option: perConnAutoConfig, value: uintptr(unsafe.Pointer(auto))},
	}
	list := perConnList{optionCount: uint32(len(opts)), options: &opts[0]}
	list.size = uint32(unsafe.Sizeof(list))
	r, _, err := procSetOption.Call(0, optPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	if r == 0 {
		return err
	}
	procSetOption.Call(0, optProxySettingsChg, 0, 0)
	procSetOption.Call(0, optSettingsChanged, 0, 0)
	procSetOption.Call(0, optRefresh, 0, 0)
	return nil
}

// Manager points the system proxy at the capture proxy and puts the
// previous settings back. The previous settings are saved to a file first,
// so a crash can be repaired on the next start (see Recover).
type Manager struct {
	backup string
	saved  *Settings
	active bool
}

// NewManager keeps its backup file in dir.
func NewManager(dir string) *Manager {
	return &Manager{backup: filepath.Join(dir, "proxy-backup.json")}
}

// Active reports whether the system proxy is currently set by us.
func (m *Manager) Active() bool { return m.active }

// Enable sets the system proxy to addr (host:port) with the given bypass
// list, e.g. "localhost;127.*;<local>".
func (m *Manager) Enable(addr, bypass string) error {
	if !m.active {
		cur, err := Current()
		if err != nil {
			return err
		}
		// A previous run that crashed left its backup: keep the original.
		if b, err := os.ReadFile(m.backup); err == nil {
			var old Settings
			if json.Unmarshal(b, &old) == nil {
				cur = old
			}
		}
		b, _ := json.Marshal(cur)
		if err := os.WriteFile(m.backup, b, 0o600); err != nil {
			return err
		}
		m.saved = &cur
	}
	s := Settings{Flags: flagDirect | flagProxy, Server: "http=" + addr + ";https=" + addr, Bypass: bypass}
	if err := apply(s); err != nil {
		return err
	}
	m.active = true
	return nil
}

// Disable restores the settings saved by Enable.
func (m *Manager) Disable() error {
	if !m.active {
		return nil
	}
	s := Settings{Flags: flagDirect}
	if m.saved != nil {
		s = *m.saved
	}
	if err := apply(s); err != nil {
		return err
	}
	m.active = false
	os.Remove(m.backup)
	return nil
}

// Recover restores the settings left in the backup file by a run that did
// not exit cleanly. It reports whether anything was restored.
func (m *Manager) Recover() (bool, error) {
	b, err := os.ReadFile(m.backup)
	if err != nil {
		return false, nil
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		os.Remove(m.backup)
		return false, errors.New("代理备份文件已损坏")
	}
	if err := apply(s); err != nil {
		return false, err
	}
	os.Remove(m.backup)
	return true, nil
}
