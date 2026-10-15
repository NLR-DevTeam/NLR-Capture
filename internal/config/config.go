// Package config keeps the settings of NLR Capture as JSON in the data
// directory.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"nlrcapture/internal/proxy"
)

// Capture modes.
const (
	ModeManual = "manual" // only listen; clients are pointed at the proxy by hand
	ModeSystem = "system" // set the Windows system proxy
	ModeTUN    = "tun"    // redirect everything through the virtual adapter
)

// Appearances.
const (
	AppearanceDark   = "dark"
	AppearanceLight  = "light"
	AppearanceSystem = "system"
)

// Column is the remembered state of one request list column.
type Column struct {
	ID      string `json:"id"`
	Visible bool   `json:"visible"`
	Width   int    `json:"width"`
}

// Settings are the user's preferences and remembered layout.
type Settings struct {
	Port      int    `json:"port"`
	ListenAll bool   `json:"listenAll"`
	Mode      string `json:"mode"`
	Bypass    string `json:"bypass"`

	Decrypt          bool     `json:"decrypt"`
	NoDecrypt        []string `json:"noDecrypt"`
	AutoBypass       bool     `json:"autoBypass"`
	IgnoreCertErrors bool     `json:"ignoreCertErrors"`
	UpstreamProxy    string   `json:"upstreamProxy"`
	MaxBodyMB        int      `json:"maxBodyMB"`
	MaxSessions      int      `json:"maxSessions"`
	BlockQUIC        bool     `json:"blockQUIC"`
	CertAsked        bool     `json:"certAsked"`

	Appearance string `json:"appearance"`
	Accent     string `json:"accent"`
	Animations bool   `json:"animations"`

	WindowWidth  int  `json:"windowWidth"`
	WindowHeight int  `json:"windowHeight"`
	Maximized    bool `json:"maximized"`

	ListColumns   []Column `json:"listColumns"`
	DetailColumns []Column `json:"detailColumns"`
	SortCol       string   `json:"sortCol"`
	SortDesc      bool     `json:"sortDesc"`
	NameWidth     int      `json:"nameWidth"`
	SearchWidth   int      `json:"searchWidth"`
	ShowFilter    bool     `json:"showFilter"`
	HideTunnels   bool     `json:"hideTunnels"`
	AutoScroll    bool     `json:"autoScroll"`
	WrapText      bool     `json:"wrapText"`

	Rules       []proxy.Rule      `json:"rules"`
	Breakpoints proxy.Breakpoints `json:"breakpoints"`
}

// Defaults returns the settings of a first start.
func Defaults() Settings {
	return Settings{
		Port:         8888,
		Mode:         ModeSystem,
		Bypass:       "localhost;127.*;10.*;192.168.*;172.16.*;<local>",
		Decrypt:      true,
		AutoBypass:   true,
		MaxBodyMB:    32,
		MaxSessions:  20000,
		BlockQUIC:    true,
		Appearance:   AppearanceSystem,
		Accent:       "green",
		Animations:   true,
		WindowWidth:  1280,
		WindowHeight: 800,
		NameWidth:    300,
		SearchWidth:  320,
		ShowFilter:   true,
		AutoScroll:   true,
		WrapText:     true,
		NoDecrypt:    []string{},
	}
}

// Store reads and writes the settings file.
type Store struct {
	dir string
	mu  sync.Mutex
	set Settings
}

// Dir returns the data directory: "data" next to the executable when it
// can be written (a portable install), else %APPDATA%\NLR Capture.
func Dir() string {
	if d := os.Getenv("NLRCAPTURE_DATA"); d != "" {
		os.MkdirAll(d, 0o700)
		return d
	}
	if exe, err := os.Executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		dir := filepath.Join(filepath.Dir(exe), "data")
		if err := os.MkdirAll(dir, 0o700); err == nil && writable(dir) {
			return dir
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "NLR Capture")
	os.MkdirAll(dir, 0o700)
	return dir
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Open loads the settings in dir, falling back to the defaults.
func Open(dir string) *Store {
	st := &Store{dir: dir, set: Defaults()}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		s := Defaults()
		if json.Unmarshal(b, &s) == nil {
			st.set = s
		}
	}
	s := &st.set
	if s.Port <= 0 || s.Port > 65535 {
		s.Port = 8888
	}
	if s.MaxBodyMB <= 0 {
		s.MaxBodyMB = 32
	}
	if s.NameWidth < 120 {
		s.NameWidth = 300
	}
	if s.SearchWidth < 200 {
		s.SearchWidth = 320
	}
	// Animations are always on; the setting is no longer offered.
	s.Animations = true
	return st
}

// Settings returns a copy of the settings.
func (st *Store) Settings() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.set
}

// Save replaces the settings and writes them to disk.
func (st *Store) Save(s Settings) error {
	st.mu.Lock()
	st.set = s
	st.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(st.dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(st.dir, "config.json"))
}
