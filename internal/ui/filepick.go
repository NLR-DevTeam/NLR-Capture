package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"nlrcapture/internal/osutil"
)

// choosePath asks the user for a file, with the system's file dialog, or
// with a path field where the desktop has none. done runs in the
// background with the chosen path; nothing runs if the user cancels.
func (a *App) choosePath(save bool, title, name, defExt string, filters []osutil.Filter, done func(path string)) {
	hwnd := a.host.HWND()
	go func() {
		var p string
		var err error
		if save {
			p, err = osutil.SaveFile(hwnd, title, name, defExt, filters)
		} else {
			p, err = osutil.OpenFile(hwnd, title, filters)
		}
		switch {
		case err == nil:
			done(p)
		case errors.Is(err, osutil.ErrNoDialog):
			a.Post(func() { a.askPath(save, title, name, defExt, done) })
		}
	}()
}

// askPath asks for a path in a dialog of our own.
func (a *App) askPath(save bool, title, name, defExt string, done func(path string)) {
	dir, err := os.UserHomeDir()
	if err != nil {
		dir = "."
	}
	start := dir + string(filepath.Separator)
	if save {
		start = filepath.Join(dir, name)
	}
	a.Open(newInputDialog(title, "文件路径", start, func(v string) {
		p := strings.TrimSpace(v)
		if p == "" {
			return
		}
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(dir, p[2:])
		}
		if save && defExt != "" && filepath.Ext(p) == "" {
			p += "." + defExt
		}
		go done(p)
	}))
}
