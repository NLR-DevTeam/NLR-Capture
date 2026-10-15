// Package sysproxy points the system proxy at the capture proxy and puts
// the previous settings back.
package sysproxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

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
	if err := apply(proxied(addr, bypass)); err != nil {
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
	s := direct()
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
