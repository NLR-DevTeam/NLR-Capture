// Package osutil holds the small operating system helpers the UI needs:
// elevation for the virtual adapter, file dialogs, and opening files and
// links with the desktop's default programs.
package osutil

import "errors"

// ErrCancelled is returned when the user closes a dialog without choosing.
var ErrCancelled = errors.New("cancelled")

// ErrNoDialog is returned when the desktop offers no file dialog the
// program can use; the caller asks for the path itself.
var ErrNoDialog = errors.New("no file dialog")

// Filter is one entry of a dialog's file type list, e.g. {"HAR 文件",
// "*.har"}. Pattern holds one or more patterns separated by semicolons.
type Filter struct {
	Name, Pattern string
}
