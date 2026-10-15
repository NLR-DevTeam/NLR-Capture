package sysproxy

import (
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// On Linux there is no single system proxy: each desktop keeps its own,
// and browsers and most programs follow the desktop's. GNOME (and the
// desktops built on its settings, such as Cinnamon, Budgie and MATE's
// GNOME schema) keeps it in gsettings, KDE in kioslaverc. Both are set
// when both are present. The values are saved as the tools print them, so
// restoring them is exact.

// Settings are the proxy settings of the desktops found.
type Settings struct {
	// Gnome maps "schema key" to the value gsettings prints.
	Gnome map[string]string `json:"gnome,omitempty"`
	// KDE maps a kioslaverc key of the "Proxy Settings" group to its value.
	KDE map[string]string `json:"kde,omitempty"`
}

var gnomeKeys = [][2]string{
	{"org.gnome.system.proxy", "mode"},
	{"org.gnome.system.proxy", "ignore-hosts"},
	{"org.gnome.system.proxy.http", "host"},
	{"org.gnome.system.proxy.http", "port"},
	{"org.gnome.system.proxy.https", "host"},
	{"org.gnome.system.proxy.https", "port"},
}

var kdeKeys = []string{"ProxyType", "httpProxy", "httpsProxy", "NoProxyFor"}

// gsettings returns the gsettings tool, if the GNOME proxy schema exists.
func gsettings() string {
	p, err := exec.LookPath("gsettings")
	if err != nil {
		return ""
	}
	if exec.Command(p, "get", "org.gnome.system.proxy", "mode").Run() != nil {
		return ""
	}
	return p
}

// kconfig returns the KDE config reader and writer of the newest Plasma
// installed.
func kconfig() (read, write string) {
	for _, v := range []string{"6", "5"} {
		r, err1 := exec.LookPath("kreadconfig" + v)
		w, err2 := exec.LookPath("kwriteconfig" + v)
		if err1 == nil && err2 == nil {
			return r, w
		}
	}
	return "", ""
}

var errNoDesktop = errors.New("未找到可设置的系统代理，支持 GNOME 和 KDE")

// Current reads the proxy settings of the desktops found.
func Current() (Settings, error) {
	var s Settings
	if gs := gsettings(); gs != "" {
		s.Gnome = map[string]string{}
		for _, k := range gnomeKeys {
			out, err := exec.Command(gs, "get", k[0], k[1]).Output()
			if err != nil {
				return s, err
			}
			s.Gnome[k[0]+" "+k[1]] = strings.TrimSpace(string(out))
		}
	}
	if r, _ := kconfig(); r != "" {
		s.KDE = map[string]string{}
		for _, k := range kdeKeys {
			out, _ := exec.Command(r, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", k).Output()
			s.KDE[k] = strings.TrimSpace(string(out))
		}
	}
	if s.Gnome == nil && s.KDE == nil {
		return s, errNoDesktop
	}
	return s, nil
}

func apply(s Settings) error {
	if gs := gsettings(); gs != "" && s.Gnome != nil {
		// The mode goes last, so programs never see it on with a half
		// written proxy.
		for _, k := range append(gnomeKeys[1:], gnomeKeys[0]) {
			v, ok := s.Gnome[k[0]+" "+k[1]]
			if !ok {
				continue
			}
			if out, err := exec.Command(gs, "set", k[0], k[1], v).CombinedOutput(); err != nil {
				return errors.New("gsettings: " + strings.TrimSpace(string(out)))
			}
		}
	}
	if _, w := kconfig(); w != "" && s.KDE != nil {
		for _, k := range kdeKeys {
			v, ok := s.KDE[k]
			if !ok {
				continue
			}
			if out, err := exec.Command(w, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", k, v).CombinedOutput(); err != nil {
				return errors.New("kwriteconfig: " + strings.TrimSpace(string(out)))
			}
		}
		// Running KDE programs reread the settings on this signal.
		if p, err := exec.LookPath("dbus-send"); err == nil {
			exec.Command(p, "--type=signal", "/KIO/Scheduler", "org.kde.KIO.Scheduler.reparseSlaveConfiguration", "string:").Run()
		}
	}
	return nil
}

// proxied returns the settings that send traffic to addr (host:port),
// except for the hosts in bypass, e.g. "localhost;127.*;<local>".
func proxied(addr, bypass string) Settings {
	host, port, _ := net.SplitHostPort(addr)
	hosts := bypassHosts(bypass)
	var s Settings
	if gsettings() != "" {
		quoted := make([]string, len(hosts))
		for i, h := range hosts {
			quoted[i] = "'" + strings.ReplaceAll(h, "'", "") + "'"
		}
		s.Gnome = map[string]string{
			"org.gnome.system.proxy mode":         "'manual'",
			"org.gnome.system.proxy ignore-hosts": "[" + strings.Join(quoted, ", ") + "]",
			"org.gnome.system.proxy.http host":    "'" + host + "'",
			"org.gnome.system.proxy.http port":    port,
			"org.gnome.system.proxy.https host":   "'" + host + "'",
			"org.gnome.system.proxy.https port":   port,
		}
	}
	if r, _ := kconfig(); r != "" {
		s.KDE = map[string]string{
			"ProxyType":  "1",
			"httpProxy":  "http://" + host + " " + port,
			"httpsProxy": "http://" + host + " " + port,
			"NoProxyFor": strings.Join(hosts, ","),
		}
	}
	return s
}

// direct returns settings without a proxy.
func direct() Settings {
	var s Settings
	if gsettings() != "" {
		s.Gnome = map[string]string{"org.gnome.system.proxy mode": "'none'"}
	}
	if r, _ := kconfig(); r != "" {
		s.KDE = map[string]string{"ProxyType": "0"}
	}
	return s
}

// bypassHosts turns the Windows-style bypass list the settings keep into
// host patterns: "<local>" stands for the local addresses.
func bypassHosts(bypass string) []string {
	var out []string
	for _, h := range strings.FieldsFunc(bypass, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
		if h == "<local>" {
			out = append(out, "localhost", "127.0.0.0/8", "::1")
			continue
		}
		out = append(out, wildcardCIDR(h))
	}
	return out
}

// wildcardCIDR turns an address wildcard such as "192.168.*", which the
// desktops do not understand, into the range "192.168.0.0/16". Anything
// else is returned as it is.
func wildcardCIDR(h string) string {
	parts := strings.Split(h, ".")
	n := len(parts) - 1
	if n < 1 || n > 3 || parts[n] != "*" {
		return h
	}
	oct := make([]string, 4)
	for i := range oct {
		oct[i] = "0"
	}
	for i := 0; i < n; i++ {
		v, err := strconv.Atoi(parts[i])
		if err != nil || v < 0 || v > 255 {
			return h
		}
		oct[i] = parts[i]
	}
	return strings.Join(oct, ".") + "/" + strconv.Itoa(n*8)
}
