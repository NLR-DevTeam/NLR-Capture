package sysproxy

// On Windows the system proxy is the WinINet setting, which browsers and
// most programs follow.

import (
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

// proxied returns the settings that send traffic to addr (host:port),
// except for the hosts in bypass, e.g. "localhost;127.*;<local>".
func proxied(addr, bypass string) Settings {
	return Settings{Flags: flagDirect | flagProxy, Server: "http=" + addr + ";https=" + addr, Bypass: bypass}
}

// direct returns settings without a proxy.
func direct() Settings { return Settings{Flags: flagDirect} }
