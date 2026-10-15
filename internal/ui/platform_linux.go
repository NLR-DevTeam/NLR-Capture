package ui

import (
	"os"
	"os/exec"
	"strings"

	"gioui.org/font"
)

// Fonts: families are tried in order, so CJK text falls back to a font
// that has the glyphs when the preferred one does not. The shaper loads
// the system's fonts, so these are the common desktop families.
const (
	uiFont   font.Typeface = "Noto Sans CJK SC, Source Han Sans SC, WenQuanYi Micro Hei, Noto Sans, Cantarell, DejaVu Sans, sans-serif"
	monoFont font.Typeface = "JetBrains Mono, Cascadia Mono, Noto Sans Mono, DejaVu Sans Mono, Noto Sans Mono CJK SC, monospace"
)

// systemDark reports whether the desktop prefers dark applications: the
// freedesktop color scheme GNOME and others set, else a dark GTK or KDE
// theme name.
func systemDark() (dark, ok bool) {
	if gs, err := exec.LookPath("gsettings"); err == nil {
		if out, err := exec.Command(gs, "get", "org.gnome.desktop.interface", "color-scheme").Output(); err == nil {
			switch v := strings.Trim(strings.TrimSpace(string(out)), "'"); v {
			case "prefer-dark":
				return true, true
			case "prefer-light":
				return false, true
			}
		}
		if out, err := exec.Command(gs, "get", "org.gnome.desktop.interface", "gtk-theme").Output(); err == nil {
			return strings.Contains(strings.ToLower(string(out)), "dark"), true
		}
	}
	for _, r := range []string{"kreadconfig6", "kreadconfig5"} {
		if p, err := exec.LookPath(r); err == nil {
			out, err := exec.Command(p, "--group", "General", "--key", "ColorScheme").Output()
			if err == nil && len(strings.TrimSpace(string(out))) > 0 {
				return strings.Contains(strings.ToLower(string(out)), "dark"), true
			}
		}
	}
	if t := os.Getenv("GTK_THEME"); t != "" {
		return strings.Contains(strings.ToLower(t), "dark"), true
	}
	return false, false
}
