package ui

import (
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"gioui.org/font"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modShell32 = windows.NewLazySystemDLL("shell32.dll")
	modUser32  = windows.NewLazySystemDLL("user32.dll")
	modDwmapi  = windows.NewLazySystemDLL("dwmapi.dll")

	procDragAcceptFiles   = modShell32.NewProc("DragAcceptFiles")
	procDragQueryFileW    = modShell32.NewProc("DragQueryFileW")
	procDragFinish        = modShell32.NewProc("DragFinish")
	procSetWindowLongPtrW = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = modUser32.NewProc("CallWindowProcW")
	procDefWindowProcW    = modUser32.NewProc("DefWindowProcW")

	procDwmExtendFrame     = modDwmapi.NewProc("DwmExtendFrameIntoClientArea")
	procDwmSetWindowAttrib = modDwmapi.NewProc("DwmSetWindowAttribute")
)

// Fonts: families are tried in order, so CJK text falls back to a font
// that has the glyphs when the preferred one does not.
const (
	uiFont   font.Typeface = "Microsoft YaHei UI, Segoe UI, sans-serif"
	monoFont font.Typeface = "Cascadia Mono, Consolas, Go Mono, Microsoft YaHei UI, monospace"
)

const (
	wmDropFiles       = 0x0233
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmSize            = 0x0005
	wmDwmComposition  = 0x031E
	sizeRestored      = 0
	gwlpWndProc       = ^uintptr(3) // -4
)

var hook struct {
	sync.Mutex
	hooks map[uintptr]*winHook
	cb    uintptr
}

type winHook struct {
	old     atomic.Uintptr
	drop    func([]string)
	endSess func()
	// sized is the last WM_SIZE kind, to notice a restore.
	sized atomic.Uintptr
}

func hookWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	hook.Lock()
	h := hook.hooks[hwnd]
	hook.Unlock()
	var old uintptr
	if h != nil {
		old = h.old.Load()
	}
	if old == 0 {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	switch msg {
	case wmDropFiles:
		n, _, _ := procDragQueryFileW.Call(wParam, 0xffffffff, 0, 0)
		paths := make([]string, 0, n)
		for i := uintptr(0); i < n; i++ {
			l, _, _ := procDragQueryFileW.Call(wParam, i, 0, 0)
			buf := make([]uint16, l+1)
			procDragQueryFileW.Call(wParam, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
			paths = append(paths, windows.UTF16ToString(buf))
		}
		procDragFinish.Call(wParam)
		if len(paths) > 0 {
			h.drop(paths)
		}
		return 0
	case wmSize, wmDwmComposition:
		if msg == wmDwmComposition || wParam == sizeRestored && h.sized.Swap(wParam) != sizeRestored {
			frameShadow(hwnd)
		}
		if msg == wmSize {
			h.sized.Store(wParam)
		}
	case wmEndSession:
		// Windows is logging off or shutting down: the system proxy must
		// be put back before the process is killed.
		if wParam != 0 && h.endSess != nil {
			h.endSess()
		}
	}
	r, _, _ := procCallWindowProcW.Call(old, hwnd, msg, wParam, lParam)
	return r
}

// InstallWindowHook makes the window accept dropped files and restore the
// system settings when Windows ends the session. It must run on the
// window thread.
func InstallWindowHook(hwnd uintptr, drop func([]string), endSession func()) {
	if hwnd == 0 {
		return
	}
	hook.Lock()
	if hook.hooks == nil {
		hook.hooks = map[uintptr]*winHook{}
		hook.cb = syscall.NewCallback(hookWndProc)
	}
	if _, ok := hook.hooks[hwnd]; ok {
		hook.Unlock()
		return
	}
	h := &winHook{drop: drop, endSess: endSession}
	h.sized.Store(^uintptr(0))
	hook.hooks[hwnd] = h
	cb := hook.cb
	hook.Unlock()
	old, _, _ := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, cb)
	h.old.Store(old)
	procDragAcceptFiles.Call(hwnd, 1)
	frameShadow(hwnd)
}

// systemDark reports whether Windows is set to the dark app mode.
func systemDark() (dark, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false, false
	}
	return v == 0, true
}

// frameShadow keeps the system shadow and rounded corners on the window,
// which draws its own title bar. Gio only asks for them when it makes the
// window windowed itself, so a window that starts maximized and is then
// restored by the system would otherwise lose them.
func frameShadow(hwnd uintptr) {
	if procDwmExtendFrame.Find() != nil {
		return
	}
	m := [4]int32{-1, -1, -1, -1}
	procDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m)))
	// DWMWA_WINDOW_CORNER_PREFERENCE: round, on Windows 11.
	pref := int32(2)
	procDwmSetWindowAttrib.Call(hwnd, 33, uintptr(unsafe.Pointer(&pref)), 4)
}
