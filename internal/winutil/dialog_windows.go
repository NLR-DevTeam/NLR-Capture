package winutil

import (
	"errors"
	"runtime"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comdlg32                 = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW     = comdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileNameW     = comdlg32.NewProc("GetSaveFileNameW")
	procCommDlgExtendedError = comdlg32.NewProc("CommDlgExtendedError")
)

type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

const (
	ofnOverwritePrompt = 0x00000002
	ofnHideReadOnly    = 0x00000004
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnFileMustExist   = 0x00001000
	ofnExplorer        = 0x00080000
)

// ErrCancelled is returned when the user closes a dialog without choosing.
var ErrCancelled = errors.New("cancelled")

// Filter is one entry of a dialog's file type list, e.g. {"HAR 文件",
// "*.har"}.
type Filter struct {
	Name, Pattern string
}

func filterString(fs []Filter) *uint16 {
	if len(fs) == 0 {
		return nil
	}
	// The list is NUL-separated and ends with two NULs, which
	// windows.UTF16FromString refuses; encode it directly.
	var u []uint16
	for _, f := range fs {
		u = append(u, utf16.Encode([]rune(f.Name))...)
		u = append(u, 0)
		u = append(u, utf16.Encode([]rune(f.Pattern))...)
		u = append(u, 0)
	}
	u = append(u, 0)
	return &u[0]
}

func fileDialog(save bool, owner uintptr, title, name, defExt string, filters []Filter) (path string, err error) {
	// A failure here runs on a helper goroutine; it must not take the
	// program down.
	defer func() {
		if r := recover(); r != nil {
			path, err = "", errors.New("文件对话框出错")
		}
	}()
	// The dialog runs a message loop and uses COM on this thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	defer windows.CoUninitialize()

	buf := make([]uint16, 32768)
	if name != "" {
		n, _ := windows.UTF16FromString(name)
		copy(buf, n)
	}
	t, _ := windows.UTF16PtrFromString(title)
	ofn := openFileName{
		owner:   owner,
		filter:  filterString(filters),
		file:    &buf[0],
		maxFile: uint32(len(buf)),
		title:   t,
		flags:   ofnExplorer | ofnHideReadOnly | ofnNoChangeDir | ofnPathMustExist,
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	if defExt != "" {
		ofn.defExt, _ = windows.UTF16PtrFromString(defExt)
	}
	proc := procGetOpenFileNameW
	if save {
		proc = procGetSaveFileNameW
		ofn.flags |= ofnOverwritePrompt
	} else {
		ofn.flags |= ofnFileMustExist
	}
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		code, _, _ := procCommDlgExtendedError.Call()
		if code == 0 {
			return "", ErrCancelled
		}
		return "", errors.New("文件对话框出错")
	}
	return windows.UTF16ToString(buf), nil
}

// OpenFile asks for a file to open. It blocks until the user answers and
// must not be called on the window's thread.
func OpenFile(owner uintptr, title string, filters []Filter) (string, error) {
	return fileDialog(false, owner, title, "", "", filters)
}

// SaveFile asks where to save a file, proposing name.
func SaveFile(owner uintptr, title, name, defExt string, filters []Filter) (string, error) {
	return fileDialog(true, owner, title, name, defExt, filters)
}
