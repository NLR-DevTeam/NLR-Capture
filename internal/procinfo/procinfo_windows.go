package procinfo

import (
	"encoding/binary"
	"net/netip"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const tcpTableOwnerPIDAll = 5

// Owner returns the process that owns the local socket at local (an
// address on this machine) connected to remote. It returns 0 when no such
// connection is found.
func Owner(local, remote netip.AddrPort) int {
	if local.Addr().Is4() || local.Addr().Is4In6() {
		return owner4(local.Addr().Unmap(), local.Port(), remote.Port())
	}
	return owner6(local.Addr(), local.Port(), remote.Port())
}

func table(family uint32) []byte {
	size := uint32(64 << 10)
	for range 4 {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		if r == 0 {
			return buf[:size]
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil
		}
		size += 16 << 10
	}
	return nil
}

func port(b []byte) uint16 { return binary.BigEndian.Uint16(b[:2]) }

func owner4(ip netip.Addr, lport, rport uint16) int {
	t := table(windows.AF_INET)
	if len(t) < 4 {
		return 0
	}
	n := int(binary.LittleEndian.Uint32(t))
	const row = 24
	want := ip.As4()
	for i := 0; i < n && 4+(i+1)*row <= len(t); i++ {
		r := t[4+i*row:]
		la := r[4:8]
		lp := port(r[8:12])
		rp := port(r[16:20])
		if lp == lport && rp == rport && (string(la) == string(want[:]) || ip.IsLoopback()) {
			return int(binary.LittleEndian.Uint32(r[20:24]))
		}
	}
	return 0
}

func owner6(ip netip.Addr, lport, rport uint16) int {
	t := table(windows.AF_INET6)
	if len(t) < 4 {
		return 0
	}
	n := int(binary.LittleEndian.Uint32(t))
	const row = 56
	for i := 0; i < n && 4+(i+1)*row <= len(t); i++ {
		r := t[4+i*row:]
		lp := port(r[20:24])
		rp := port(r[44:48])
		if lp == lport && rp == rport {
			return int(binary.LittleEndian.Uint32(r[52:56]))
		}
	}
	return 0
}

// processName returns the executable name of a process.
func processName(pid int) string {
	if pid == 4 {
		return "System"
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
