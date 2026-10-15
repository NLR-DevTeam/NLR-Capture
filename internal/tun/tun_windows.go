package tun

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AdapterName is the name of the virtual adapter in Windows.
const AdapterName = "NLR Capture"

var (
	iphlpapi                        = windows.NewLazySystemDLL("iphlpapi.dll")
	procConvertInterfaceLuidToIndex = iphlpapi.NewProc("ConvertInterfaceLuidToIndex")
)

// Ready reports why the virtual adapter cannot be started, or nil: the
// Wintun driver library must ship next to the program.
func Ready() error {
	if FindDLL() == "" {
		return errors.New("缺少 wintun.dll")
	}
	return nil
}

func netsh(args ...string) error {
	cmd := exec.Command("netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 300 {
			out = out[:300]
		}
		return fmt.Errorf("netsh %v: %v %s", args, err, out)
	}
	return nil
}

// bestInterface returns the interface the system would use to reach the
// internet now.
func bestInterface() (uint32, error) {
	var idx uint32
	sa := &windows.SockaddrInet4{Addr: [4]byte{1, 1, 1, 1}}
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0, fmt.Errorf("找不到可用的网络接口：%w", err)
	}
	return idx, nil
}

// wintunLink is a Wintun adapter session.
type wintunLink struct{ sess *session }

func (l wintunLink) read(buf []byte) (int, error) {
	s := l.sess
	for {
		s.sendMu.Lock()
		if s.handle == 0 {
			s.sendMu.Unlock()
			return 0, errClosed
		}
		n, err := s.receive(buf)
		s.sendMu.Unlock()
		switch {
		case err == errNoMore:
			windows.WaitForSingleObject(s.event, 200)
			continue
		case err == windows.ERROR_HANDLE_EOF:
			return 0, errClosed
		}
		return n, err
	}
}

func (l wintunLink) write(p []byte) error { return l.sess.send(p) }

func (l wintunLink) close() {
	// Ending the session wakes the reader.
	l.sess.sendMu.Lock()
	l.sess.end()
	l.sess.sendMu.Unlock()
}

// Start creates the adapter, takes the default routes and begins
// redirecting traffic. It needs administrator rights.
func Start(opts Options) (*Device, error) {
	w, err := loadWintun(FindDLL())
	if err != nil {
		return nil, err
	}
	phys, err := bestInterface()
	if err != nil {
		return nil, err
	}
	guid := windows.GUID{Data1: 0x4e4c5243, Data2: 0x6170, Data3: 0x7475, Data4: [8]byte{0x72, 0x65, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}}
	ad, err := w.create(AdapterName, "NLR Capture", &guid)
	if err != nil {
		return nil, err
	}
	d := newDevice(opts, phys)
	d.release = ad.close
	ok := false
	defer func() {
		if !ok {
			d.teardown()
		}
	}()
	var index uint32
	luid := ad.luid()
	r, _, _ := procConvertInterfaceLuidToIndex.Call(uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&index)))
	if r != 0 {
		return nil, fmt.Errorf("无法取得虚拟网卡序号：%d", r)
	}
	if index == phys {
		return nil, errors.New("虚拟网卡与物理网卡冲突")
	}
	idx := strconv.Itoa(int(index))
	if err := netsh("interface", "ipv4", "set", "address", "name="+idx, "source=static", "address="+tunAddr.String(), "mask=255.255.255.252", "gateway=none", "store=active"); err != nil {
		return nil, err
	}
	netsh("interface", "ipv4", "set", "interface", idx, "metric=1", "store=active")
	netsh("interface", "ipv4", "set", "subinterface", idx, "mtu=1500", "store=active")

	sess, err := ad.start(0x400000)
	if err != nil {
		return nil, err
	}
	d.link = wintunLink{sess}
	if err := d.run(); err != nil {
		return nil, err
	}

	var routes []string
	d.unroute = func() {
		for _, prefix := range routes {
			netsh("interface", "ipv4", "delete", "route", "prefix="+prefix, "interface="+idx, "nexthop="+fakeAddr.String(), "store=active")
		}
	}
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := netsh("interface", "ipv4", "add", "route", "prefix="+prefix, "interface="+idx, "nexthop="+fakeAddr.String(), "metric=1", "store=active"); err != nil {
			return nil, err
		}
		routes = append(routes, prefix)
	}
	ok = true
	return d, nil
}
