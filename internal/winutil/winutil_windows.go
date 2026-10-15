// Package winutil holds small Windows helpers: elevation, file dialogs and
// opening things with the shell.
package winutil

import (
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// IsAdmin reports whether the process runs elevated.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// RelaunchAdmin starts the program again elevated with the given extra
// arguments. Windows asks the user for consent; the call returns once the
// new process started (or the user declined, which is an error).
func RelaunchAdmin(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	var quoted []string
	for _, a := range args {
		quoted = append(quoted, syscall.EscapeArg(a))
	}
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_SHOWNORMAL)
}

// Open opens a file or URL with its default program.
func Open(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// Reveal opens Explorer with the file selected.
func Reveal(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	exe, _ := windows.UTF16PtrFromString("explorer.exe")
	args, _ := windows.UTF16PtrFromString(`/select,"` + path + `"`)
	windows.ShellExecute(0, verb, exe, args, nil, windows.SW_SHOWNORMAL)
}
