package osutil

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The file dialogs come from zenity (GNOME and most others) or kdialog
// (KDE), whichever the desktop has; without either the caller asks for a
// path itself (ErrNoDialog).

// OpenFile asks for a file to open. It blocks until the user answers and
// must not be called on the window's thread.
func OpenFile(owner uintptr, title string, filters []Filter) (string, error) {
	return fileDialog(false, title, "", filters)
}

// SaveFile asks where to save a file, proposing name.
func SaveFile(owner uintptr, title, name, defExt string, filters []Filter) (string, error) {
	p, err := fileDialog(true, title, name, filters)
	if err == nil && defExt != "" && filepath.Ext(p) == "" {
		p += "." + defExt
	}
	return p, err
}

func fileDialog(save bool, title, name string, filters []Filter) (string, error) {
	kde := strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE")
	tools := []string{"zenity", "kdialog"}
	if kde {
		tools = []string{"kdialog", "zenity"}
	}
	for _, t := range tools {
		p, err := exec.LookPath(t)
		if err != nil {
			continue
		}
		var args []string
		if t == "zenity" {
			args = zenityArgs(save, title, name, filters)
		} else {
			args = kdialogArgs(save, title, name, filters)
		}
		var out bytes.Buffer
		cmd := exec.Command(p, args...)
		cmd.Stdout = &out
		err = cmd.Run()
		path := strings.TrimRight(out.String(), "\r\n")
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee) && ee.ExitCode() == 1, err == nil && path == "":
			return "", ErrCancelled
		case err != nil:
			continue
		}
		return path, nil
	}
	return "", ErrNoDialog
}

func zenityArgs(save bool, title, name string, filters []Filter) []string {
	args := []string{"--file-selection", "--title=" + title}
	if save {
		args = append(args, "--save", "--confirm-overwrite")
		if name != "" {
			args = append(args, "--filename="+name)
		}
	}
	for _, f := range filters {
		args = append(args, "--file-filter="+f.Name+" | "+strings.Join(patterns(f), " "))
	}
	return args
}

func kdialogArgs(save bool, title, name string, filters []Filter) []string {
	var fs []string
	for _, f := range filters {
		fs = append(fs, f.Name+" ("+strings.Join(patterns(f), " ")+")")
	}
	start := name
	if start == "" {
		start = "."
	}
	op := "--getopenfilename"
	if save {
		op = "--getsavefilename"
	}
	return []string{"--title", title, op, start, strings.Join(fs, "\n")}
}

// patterns splits a filter's semicolon-separated patterns; "*.*" means
// every file, which on Linux is "*".
func patterns(f Filter) []string {
	var out []string
	for _, p := range strings.Split(f.Pattern, ";") {
		if p = strings.TrimSpace(p); p == "*.*" {
			p = "*"
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
