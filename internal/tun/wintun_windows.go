package tun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Bindings to wintun.dll (https://www.wintun.net), the layer 3 TUN driver
// of WireGuard. The DLL carries the signed driver and installs it on first
// use; it needs administrator rights.

type wintunDLL struct {
	dll                     *windows.DLL
	createAdapter           *windows.Proc
	closeAdapter            *windows.Proc
	getAdapterLUID          *windows.Proc
	startSession            *windows.Proc
	endSession              *windows.Proc
	getReadWaitEvent        *windows.Proc
	receivePacket           *windows.Proc
	releaseReceivePacket    *windows.Proc
	allocateSendPacket      *windows.Proc
	sendPacket              *windows.Proc
	getRunningDriverVersion *windows.Proc
}

var (
	loadMu sync.Mutex
	loaded *wintunDLL
)

// DLLName is the file the driver library is loaded from.
const DLLName = "wintun.dll"

// FindDLL returns the path of wintun.dll, which ships next to the
// executable, or "" if it is missing.
func FindDLL() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(exe), DLLName)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func loadWintun(path string) (*wintunDLL, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if loaded != nil {
		return loaded, nil
	}
	if err := VerifyDLL(path); err != nil {
		return nil, err
	}
	d, err := windows.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("加载 %s 失败：%w", path, err)
	}
	{
		w := &wintunDLL{dll: d}
		for name, p := range map[string]**windows.Proc{
			"WintunCreateAdapter":           &w.createAdapter,
			"WintunCloseAdapter":            &w.closeAdapter,
			"WintunGetAdapterLUID":          &w.getAdapterLUID,
			"WintunStartSession":            &w.startSession,
			"WintunEndSession":              &w.endSession,
			"WintunGetReadWaitEvent":        &w.getReadWaitEvent,
			"WintunReceivePacket":           &w.receivePacket,
			"WintunReleaseReceivePacket":    &w.releaseReceivePacket,
			"WintunAllocateSendPacket":      &w.allocateSendPacket,
			"WintunSendPacket":              &w.sendPacket,
			"WintunGetRunningDriverVersion": &w.getRunningDriverVersion,
		} {
			proc, err := d.FindProc(name)
			if err != nil {
				return nil, fmt.Errorf("wintun.dll 缺少 %s", name)
			}
			*p = proc
		}
		loaded = w
	}
	return loaded, nil
}

type adapter struct {
	w      *wintunDLL
	handle uintptr
}

func (w *wintunDLL) create(name, tunnelType string, guid *windows.GUID) (*adapter, error) {
	n, _ := windows.UTF16PtrFromString(name)
	t, _ := windows.UTF16PtrFromString(tunnelType)
	r, _, err := w.createAdapter.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(guid)))
	if r == 0 {
		return nil, fmt.Errorf("创建虚拟网卡失败：%w", err)
	}
	return &adapter{w: w, handle: r}, nil
}

func (a *adapter) close() {
	if a.handle != 0 {
		a.w.closeAdapter.Call(a.handle)
		a.handle = 0
	}
}

func (a *adapter) luid() uint64 {
	var l uint64
	a.w.getAdapterLUID.Call(a.handle, uintptr(unsafe.Pointer(&l)))
	return l
}

type session struct {
	w      *wintunDLL
	handle uintptr
	event  windows.Handle
	sendMu sync.Mutex
}

func (a *adapter) start(capacity uint32) (*session, error) {
	r, _, err := a.w.startSession.Call(a.handle, uintptr(capacity))
	if r == 0 {
		return nil, fmt.Errorf("启动虚拟网卡会话失败：%w", err)
	}
	ev, _, _ := a.w.getReadWaitEvent.Call(r)
	return &session{w: a.w, handle: r, event: windows.Handle(ev)}, nil
}

func (s *session) end() {
	if s.handle != 0 {
		s.w.endSession.Call(s.handle)
		s.handle = 0
	}
}

var errNoMore = errors.New("no packet")

// receive copies the next packet into buf and returns its length.
func (s *session) receive(buf []byte) (int, error) {
	var size uint32
	r, _, err := s.w.receivePacket.Call(s.handle, uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		if err == windows.ERROR_NO_MORE_ITEMS {
			return 0, errNoMore
		}
		return 0, err
	}
	pkt := unsafe.Slice((*byte)(unsafe.Pointer(r)), size)
	n := copy(buf, pkt)
	s.w.releaseReceivePacket.Call(s.handle, r)
	return n, nil
}

// send queues one packet to the adapter.
func (s *session) send(p []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.handle == 0 {
		return errors.New("session closed")
	}
	r, _, err := s.w.allocateSendPacket.Call(s.handle, uintptr(len(p)))
	if r == 0 {
		return err
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(r)), len(p)), p)
	s.w.sendPacket.Call(s.handle, r)
	return nil
}
