// Package procinfo finds the program on the other end of a local TCP
// connection, for the Process column.
package procinfo

import (
	"encoding/binary"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

var names = struct {
	sync.Mutex
	m map[int]nameEntry
}{m: map[int]nameEntry{}}

type nameEntry struct {
	name string
	at   time.Time
}

// Name returns the executable name of a process, without the directory.
func Name(pid int) string {
	if pid == 0 {
		return ""
	}
	if pid == 4 {
		return "System"
	}
	names.Lock()
	if e, ok := names.m[pid]; ok && time.Since(e.at) < 30*time.Second {
		names.Unlock()
		return e.name
	}
	names.Unlock()
	name := ""
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			name = filepath.Base(windows.UTF16ToString(buf[:n]))
		}
		windows.CloseHandle(h)
	}
	names.Lock()
	if len(names.m) > 4096 {
		names.m = map[int]nameEntry{}
	}
	names.m[pid] = nameEntry{name, time.Now()}
	names.Unlock()
	return name
}

// OfConn finds the process at the remote end of a connection accepted on
// this machine (the client of the proxy).
func OfConn(c net.Conn) (int, string) {
	ra, ok1 := c.RemoteAddr().(*net.TCPAddr)
	la, ok2 := c.LocalAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return 0, ""
	}
	rip, _ := netip.AddrFromSlice(ra.IP)
	if !isLocal(rip.Unmap()) {
		// A remote device using the proxy: name it by its address.
		return 0, ra.IP.String()
	}
	// The client's socket has the client port locally and our port remote.
	pid := Owner(netip.AddrPortFrom(rip.Unmap(), uint16(ra.Port)), netip.AddrPortFrom(netip.Addr{}, uint16(la.Port)))
	return pid, strings.TrimSpace(Name(pid))
}

var localAddrs = struct {
	sync.Mutex
	set map[netip.Addr]bool
	at  time.Time
}{}

func isLocal(ip netip.Addr) bool {
	if ip.IsLoopback() {
		return true
	}
	localAddrs.Lock()
	defer localAddrs.Unlock()
	if localAddrs.set == nil || time.Since(localAddrs.at) > 30*time.Second {
		localAddrs.set = map[netip.Addr]bool{}
		addrs, _ := net.InterfaceAddrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if x, ok := netip.AddrFromSlice(n.IP); ok {
					localAddrs.set[x.Unmap()] = true
				}
			}
		}
		localAddrs.at = time.Now()
	}
	return localAddrs.set[ip]
}
