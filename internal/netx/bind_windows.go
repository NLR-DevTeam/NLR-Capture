// Package netx has socket helpers shared by the proxy and the virtual
// adapter: binding outgoing sockets to a physical interface.
package netx

import (
	"encoding/binary"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ipUnicastIf   = 31
	ipv6UnicastIf = 31
)

// BindControl returns a net.Dialer / net.ListenConfig Control function that
// sends the socket's traffic out of the interface with the given index,
// whatever the routing table says. Zero returns nil.
func BindControl(index uint32) func(network, address string, c syscall.RawConn) error {
	if index == 0 {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			h := windows.Handle(fd)
			if strings.HasSuffix(network, "6") {
				serr = windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(index))
				return
			}
			// IP_UNICAST_IF takes the index in network byte order.
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], index)
			v := *(*uint32)(unsafe.Pointer(&b[0]))
			serr = windows.Setsockopt(h, windows.IPPROTO_IP, ipUnicastIf, (*byte)(unsafe.Pointer(&v)), 4)
			if serr == nil && network != "tcp4" && network != "udp4" {
				// Dual-stack sockets need both.
				windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(index))
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}
