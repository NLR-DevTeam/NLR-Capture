// Package procinfo finds the program on the other end of a local TCP
// connection, for the Process column.
package procinfo

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

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
	names.Lock()
	if e, ok := names.m[pid]; ok && time.Since(e.at) < 30*time.Second {
		names.Unlock()
		return e.name
	}
	names.Unlock()
	name := processName(pid)
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
