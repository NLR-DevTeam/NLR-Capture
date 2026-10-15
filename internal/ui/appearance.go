package ui

import (
	"image/color"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"nlrcapture/internal/config"
)

// accentColor maps an accent name to its color.
func accentColor(name string) color.NRGBA {
	switch name {
	case "blue":
		return rgb(0x0192ff)
	case "orange":
		return rgb(0xffa500)
	case "purple":
		return rgb(0x8b5cf6)
	}
	return rgb(0x00b935)
}

// applyTheme sets the palette from the appearance and accent settings.
func (a *App) applyTheme() {
	light := false
	switch a.set.Appearance {
	case config.AppearanceLight:
		light = true
	case config.AppearanceSystem:
		if a.sysDark.Load() == 0 {
			if dark, ok := systemDark(); ok {
				a.sysDark.Store(map[bool]int32{true: 1, false: 2}[dark])
			}
		}
		light = a.sysDark.Load() == 2
	}
	a.th.Apply(light, accentColor(a.set.Accent))
	a.host.Invalidate()
}

// watchSystemTheme follows the OS light/dark preference.
func (a *App) watchSystemTheme() {
	for range time.Tick(3 * time.Second) {
		dark, ok := systemDark()
		if !ok {
			continue
		}
		v := map[bool]int32{true: 1, false: 2}[dark]
		if a.sysDark.Swap(v) != v {
			a.Post(func() {
				if a.set.Appearance == config.AppearanceSystem {
					a.applyTheme()
				}
			})
		}
	}
}

// editMenu opens the cut/copy/paste menu for an input.
func (a *App) editMenu(e *widget.Editor) {
	start, end := e.Selection()
	hasSel := start != end
	copySel := func(gtx layout.Context) {
		if s := e.SelectedText(); s != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
		}
	}
	items := []MenuItem{
		{Label: "剪切", Icon: icCut, Hint: "Ctrl+X", Disabled: !hasSel || e.ReadOnly, Do: func() {
			a.later(func(gtx layout.Context) {
				copySel(gtx)
				e.Delete(1)
				gtx.Execute(key.FocusCmd{Tag: e})
			})
		}},
		{Label: "复制", Icon: icCopy, Hint: "Ctrl+C", Disabled: !hasSel, Do: func() {
			a.later(func(gtx layout.Context) {
				copySel(gtx)
				gtx.Execute(key.FocusCmd{Tag: e})
			})
		}},
		{Label: "粘贴", Icon: icPaste, Hint: "Ctrl+V", Disabled: e.ReadOnly, Do: func() {
			a.later(func(gtx layout.Context) {
				gtx.Execute(key.FocusCmd{Tag: e})
				gtx.Execute(clipboard.ReadCmd{Tag: e})
			})
		}},
		{Sep: true},
		{Label: "全选", Icon: icSelectAll, Hint: "Ctrl+A", Disabled: e.Len() == 0, Do: func() {
			e.SetCaret(e.Len(), 0)
			a.later(func(gtx layout.Context) { gtx.Execute(key.FocusCmd{Tag: e}) })
		}},
	}
	a.Menu(items...)
	if a.menu != nil {
		a.menu.keepFocus = true
	}
}
