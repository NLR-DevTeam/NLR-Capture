package ui

import (
	"strings"

	"nlrcapture/internal/config"
	"nlrcapture/internal/proxy"
)

// The helpers in this file exist for the off-screen renderer (cmd/shot);
// the application itself does not use them.

// DialogCount returns the number of open dialogs.
func (a *App) DialogCount() int { return len(a.dialogs) }

// MenuOpen reports whether a popup menu is showing.
func (a *App) MenuOpen() bool { return a.menu != nil }

// CloseDialogs closes every dialog and menu.
func (a *App) CloseDialogs() {
	a.closeMenu()
	for len(a.dialogs) > 0 {
		a.Close(a.dialogs[len(a.dialogs)-1])
	}
	a.dialogsOut = nil
	a.menuOut = nil
}

// SelectURL selects the first listed session whose URL contains s.
func (a *App) SelectURL(s string) bool {
	a.list.refresh()
	for _, x := range a.list.view {
		if strings.Contains(x.URL, s) {
			a.list.selectID(x.ID)
			return true
		}
	}
	return false
}

// ClearSelection closes the details.
func (a *App) ClearSelection() { a.list.clearSelection() }

// SetTab switches the detail tab.
func (a *App) SetTab(t string) { a.details.tab = t }

// OpenDialog opens a dialog by name.
func (a *App) OpenDialog(name string) {
	switch name {
	case "settings":
		a.Open(newSettingsDialog(a))
	case "cert":
		a.Open(newCertDialog(a))
	case "rules":
		d := newRulesDialog(a)
		if len(d.rows) == 0 {
			d.startNew(rulesExample[0])
			d.startNew(rulesExample[1])
		}
		a.Open(d)
	case "breakpoints":
		a.Open(newBreakDialog(a))
	case "compose":
		a.Open(newComposeDialog(a, a.list.selected()))
	case "about":
		a.Open(&aboutDialog{})
	}
}

// SetFilter sets the filter box text.
func (a *App) SetFilter(text string) {
	a.filterField.SetText(text)
	a.filterText = text
	a.applyFilter()
}

// SetTypes selects resource type chips.
func (a *App) SetTypes(types ...string) {
	a.types = map[string]bool{}
	for _, t := range types {
		a.types[t] = true
	}
	a.applyFilter()
}

// Search opens the search pane and searches for q.
func (a *App) Search(q string) {
	a.search.open = true
	a.search.field.SetText(q)
	a.search.start()
}

// SearchDone reports whether the search finished.
func (a *App) SearchDone() bool { return !a.search.running }

// SetLight switches between the light and the dark palette.
func (a *App) SetLight(light bool) {
	if light {
		a.set.Appearance = config.AppearanceLight
	} else {
		a.set.Appearance = config.AppearanceDark
	}
	a.applyTheme()
}

// RowMenu opens the context menu of the selected rows.
func (a *App) RowMenu() { a.list.rowMenu() }

// ListLen returns the number of listed sessions.
func (a *App) ListLen() int {
	a.list.refresh()
	return len(a.list.view)
}

// Port returns the port the proxy listens on.
func (a *App) Port() int { return a.set.Port }

var rulesExample = []proxy.Rule{
	{Enabled: true, Match: "/api/ads", Action: "*block", Comment: "屏蔽广告接口"},
	{Enabled: true, Match: `regex:^https://example\.com/config\.json$`, Action: `*body:{"debug":true}`},
}
