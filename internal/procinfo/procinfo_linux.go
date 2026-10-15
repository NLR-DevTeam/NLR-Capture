package procinfo

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// On Linux the kernel lists TCP sockets with their inode in /proc/net/tcp
// and tcp6, and each process's open sockets show up in /proc/<pid>/fd as
// links to "socket:[inode]". Only the user's own processes can be read
// unless the program runs as root.

// Owner returns the process that owns the local socket at local (an
// address on this machine) connected to remote. It returns 0 when no such
// connection is found.
func Owner(local, remote netip.AddrPort) int {
	ino := socketInode(netip.AddrPortFrom(local.Addr().Unmap(), local.Port()), remote.Port())
	if ino == 0 {
		return 0
	}
	return inodeOwner(ino)
}

// socketInode finds the inode of the TCP socket bound to local and
// connected to remotePort.
func socketInode(local netip.AddrPort, remotePort uint16) uint64 {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if ino := scanTCP(f, local, remotePort); ino != 0 {
			return ino
		}
	}
	return 0
}

func scanTCP(path string, local netip.AddrPort, remotePort uint16) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		fs := strings.Fields(sc.Text())
		if len(fs) < 10 {
			continue
		}
		la, ok := parseHexAddr(fs[1])
		if !ok || la != local {
			continue
		}
		ra, ok := parseHexAddr(fs[2])
		if !ok || ra.Port() != remotePort {
			continue
		}
		ino, _ := strconv.ParseUint(fs[9], 10, 64)
		if ino != 0 {
			return ino
		}
	}
	return 0
}

// parseHexAddr reads an address as /proc/net/tcp prints it: the IP as
// 32-bit words in host (little-endian) order, then the port, in hex.
func parseHexAddr(s string) (netip.AddrPort, bool) {
	ip, port, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	b, err := hex.DecodeString(ip)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(b); i += 4 {
		binary.BigEndian.PutUint32(b[i:], binary.LittleEndian.Uint32(b[i:]))
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	a, _ := netip.AddrFromSlice(b)
	return netip.AddrPortFrom(a.Unmap(), uint16(p)), true
}

// inodes maps socket inodes to the processes holding them. Walking every
// process's descriptors is the slow part, so the map is rebuilt only when
// an inode is missing and the last walk is not too recent.
var inodes = struct {
	sync.Mutex
	m  map[uint64]int
	at time.Time
}{}

func inodeOwner(ino uint64) int {
	inodes.Lock()
	defer inodes.Unlock()
	if pid, ok := inodes.m[ino]; ok {
		return pid
	}
	if time.Since(inodes.at) < 50*time.Millisecond {
		return 0
	}
	inodes.m = walkSockets()
	inodes.at = time.Now()
	return inodes.m[ino]
}

func walkSockets() map[uint64]int {
	m := map[uint64]int{}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return m
	}
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			l, err := os.Readlink(filepath.Join(dir, fd.Name()))
			if err != nil || !strings.HasPrefix(l, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(l[len("socket:["):], "]"), 10, 64)
			if err == nil {
				m[ino] = pid
			}
		}
	}
	return m
}

// processName returns the executable name of a process.
func processName(pid int) string {
	if exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); err == nil {
		return filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
