package osutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The virtual adapter needs CAP_NET_ADMIN to create the device and set its
// routes, and CAP_NET_RAW to bind outgoing sockets to the physical
// interface.
const (
	capNetAdmin = 12
	capNetRaw   = 13
)

// Elevated reports whether the process may run the virtual adapter: it is
// root, or the program file was given the network capabilities.
func Elevated() bool {
	if os.Geteuid() == 0 {
		return true
	}
	caps := effectiveCaps()
	return caps&(1<<capNetAdmin) != 0 && caps&(1<<capNetRaw) != 0
}

// effectiveCaps reads the effective capability set of this process.
func effectiveCaps() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "CapEff:"); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return n
		}
	}
	return 0
}

// RelaunchElevated gives the program file the network capabilities, which
// the desktop asks the user to authorize through polkit, and starts the
// program again with the given extra arguments. The program keeps running
// as the user, with the user's settings and display; only the capabilities
// are added.
func RelaunchElevated(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return fmt.Errorf("需要 pkexec，或手动执行 sudo setcap cap_net_admin,cap_net_raw+ep %q", exe)
	}
	setcap := "/usr/sbin/setcap"
	if p, err := exec.LookPath("setcap"); err == nil {
		setcap = p
	} else if _, err := os.Stat(setcap); err != nil {
		setcap = "/sbin/setcap"
	}
	cmd := exec.Command(pkexec, setcap, "cap_net_admin,cap_net_raw+ep", exe)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			return ErrCancelled
		}
		return fmt.Errorf("setcap: %v %s", err, strings.TrimSpace(string(out)))
	}
	return start(exe, args...)
}

// start runs the program detached from this process.
func start(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Open opens a file or URL with its default program.
func Open(target string) error {
	for _, c := range [][]string{{"xdg-open", target}, {"gio", "open", target}} {
		if p, err := exec.LookPath(c[0]); err == nil {
			return start(p, c[1:]...)
		}
	}
	return errors.New("找不到 xdg-open")
}

// Reveal shows the file in the file manager, selected where the file
// manager supports it.
func Reveal(path string) {
	if p, err := exec.LookPath("gdbus"); err == nil {
		uri := "file://" + path
		cmd := exec.Command(p, "call", "--session", "--dest", "org.freedesktop.FileManager1",
			"--object-path", "/org/freedesktop/FileManager1", "--method", "org.freedesktop.FileManager1.ShowItems",
			"['"+strings.ReplaceAll(uri, "'", "%27")+"']", "")
		if cmd.Run() == nil {
			return
		}
	}
	Open(filepath.Dir(path))
}
