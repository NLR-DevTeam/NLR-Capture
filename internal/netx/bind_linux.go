// Package netx has socket helpers shared by the proxy and the virtual
// adapter: binding outgoing sockets to a physical interface.
package netx

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// BindControl returns a net.Dialer / net.ListenConfig Control function that
// sends the socket's traffic out of the interface with the given index,
// whatever the routing table says. Zero returns nil. Binding to a device
// needs CAP_NET_RAW.
func BindControl(index uint32) func(network, address string, c syscall.RawConn) error {
	if index == 0 {
		return nil
	}
	ifc, err := net.InterfaceByIndex(int(index))
	if err != nil {
		return func(string, string, syscall.RawConn) error { return err }
	}
	name := ifc.Name
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, name)
		})
		if err != nil {
			return err
		}
		return serr
	}
}
