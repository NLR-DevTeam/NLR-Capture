// Package tun implements the virtual network adapter capture mode: a
// Wintun adapter takes the default routes, and TCP connections arriving on
// it are reflected into a local listener (by rewriting addresses, so the
// system's own TCP stack does the work) and handed to the transparent
// proxy. UDP is forwarded as is, except QUIC, which is refused so that
// browsers fall back to TCP where their HTTPS can be intercepted.
package tun

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"time"
)

// Addresses inside the adapter's /30: the adapter's own address, where the
// listener sits, and the stand-in address redirected connections appear
// to come from.
var (
	tunAddr  = netip.MustParseAddr("172.19.0.1")
	fakeAddr = netip.MustParseAddr("172.19.0.2")
)

const (
	protoICMP = 1
	protoTCP  = 6
	protoUDP  = 17
)

type flowKey struct {
	src, dst     [4]byte
	sport, dport uint16
}

func (k flowKey) srcAddr() netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4(k.src), k.sport)
}

func (k flowKey) dstAddr() netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4(k.dst), k.dport)
}

type natEntry struct {
	key  flowKey
	port uint16
	last time.Time
	fin  bool
}

// nat maps redirected TCP flows to ports of the stand-in address.
type nat struct {
	mu     sync.Mutex
	lport  uint16 // the listener's port on tunAddr
	byKey  map[flowKey]*natEntry
	byPort map[uint16]*natEntry
	next   uint16
}

func newNAT(lport uint16) *nat {
	return &nat{lport: lport, byKey: map[flowKey]*natEntry{}, byPort: map[uint16]*natEntry{}, next: 10000}
}

// lookup returns the original flow of a connection the listener accepted
// from the stand-in address at port.
func (n *nat) lookup(port uint16) (flowKey, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.byPort[port]
	if !ok {
		return flowKey{}, false
	}
	return e.key, true
}

func (n *nat) alloc(k flowKey, now time.Time) *natEntry {
	if e, ok := n.byKey[k]; ok {
		return e
	}
	for range 65536 {
		p := n.next
		n.next++
		if n.next < 10000 {
			n.next = 10000
		}
		if old, ok := n.byPort[p]; ok {
			// Reuse a port only once its flow has been idle a while.
			if now.Sub(old.last) < 2*time.Minute && !(old.fin && now.Sub(old.last) > 10*time.Second) {
				continue
			}
			delete(n.byKey, old.key)
		}
		e := &natEntry{key: k, port: p, last: now}
		n.byKey[k] = e
		n.byPort[p] = e
		return e
	}
	return nil
}

// gc forgets idle flows.
func (n *nat) gc(now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for p, e := range n.byPort {
		if now.Sub(e.last) > 10*time.Minute || (e.fin && now.Sub(e.last) > 30*time.Second) {
			delete(n.byPort, p)
			delete(n.byKey, e.key)
		}
	}
}

// rewriteTCP rewrites a TCP/IPv4 packet read from the adapter so the
// system's TCP stack delivers it to the listener, or delivers the
// listener's answer back to the client. It reports whether the packet
// should be written back.
func (n *nat) rewriteTCP(p []byte, now time.Time) bool {
	ihl := int(p[0]&0x0f) * 4
	if len(p) < ihl+20 {
		return false
	}
	tcp := p[ihl:]
	var src, dst [4]byte
	copy(src[:], p[12:16])
	copy(dst[:], p[16:20])
	sport := binary.BigEndian.Uint16(tcp[0:2])
	dport := binary.BigEndian.Uint16(tcp[2:4])
	flags := tcp[13]
	fin := flags&0x05 != 0 // FIN or RST

	n.mu.Lock()
	defer n.mu.Unlock()
	if src == tunAddr.As4() && sport == n.lport {
		// The listener answering the stand-in: back to the client, as if
		// from the server.
		e, ok := n.byPort[dport]
		if !ok || netip.AddrFrom4(dst) != fakeAddr {
			return false
		}
		e.last = now
		e.fin = e.fin || fin
		setAddrs(p, e.key.dst, e.key.src)
		binary.BigEndian.PutUint16(tcp[0:2], e.key.dport)
		binary.BigEndian.PutUint16(tcp[2:4], e.key.sport)
	} else {
		k := flowKey{src: src, dst: dst, sport: sport, dport: dport}
		e, ok := n.byKey[k]
		if !ok {
			if flags&0x02 == 0 {
				// Not a SYN and no flow: a stray packet.
				return false
			}
			e = n.alloc(k, now)
			if e == nil {
				return false
			}
		}
		e.last = now
		e.fin = e.fin || fin
		setAddrs(p, fakeAddr.As4(), tunAddr.As4())
		binary.BigEndian.PutUint16(tcp[0:2], e.port)
		binary.BigEndian.PutUint16(tcp[2:4], n.lport)
	}
	ipChecksum(p[:ihl])
	l4Checksum(p, ihl, protoTCP)
	return true
}

func setAddrs(p []byte, src, dst [4]byte) {
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
}

func checksum(sum uint32, b []byte) uint32 {
	for len(b) >= 2 {
		sum += uint32(b[0])<<8 | uint32(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	return sum
}

func fold(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func ipChecksum(h []byte) {
	h[10], h[11] = 0, 0
	binary.BigEndian.PutUint16(h[10:12], fold(checksum(0, h)))
}

// l4Checksum recomputes the TCP or UDP checksum of an IPv4 packet.
func l4Checksum(p []byte, ihl int, proto byte) {
	total := int(binary.BigEndian.Uint16(p[2:4]))
	if total > len(p) || total < ihl {
		total = len(p)
	}
	seg := p[ihl:total]
	off := 16
	if proto == protoUDP {
		off = 6
	}
	if len(seg) < off+2 {
		return
	}
	seg[off], seg[off+1] = 0, 0
	var pseudo [12]byte
	copy(pseudo[0:8], p[12:20])
	pseudo[9] = proto
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(seg)))
	sum := checksum(checksum(0, pseudo[:]), seg)
	c := fold(sum)
	if proto == protoUDP && c == 0 {
		c = 0xffff
	}
	binary.BigEndian.PutUint16(seg[off:off+2], c)
}

// buildUDP makes an IPv4/UDP packet.
func buildUDP(src, dst netip.AddrPort, payload []byte) []byte {
	p := make([]byte, 28+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[8] = 64
	p[9] = protoUDP
	s, d := src.Addr().As4(), dst.Addr().As4()
	setAddrs(p, s, d)
	binary.BigEndian.PutUint16(p[20:22], src.Port())
	binary.BigEndian.PutUint16(p[22:24], dst.Port())
	binary.BigEndian.PutUint16(p[24:26], uint16(8+len(payload)))
	copy(p[28:], payload)
	ipChecksum(p[:20])
	l4Checksum(p, 20, protoUDP)
	return p
}

// buildUnreachable makes an ICMP port-unreachable answer to an IPv4
// packet, which makes browsers give up on QUIC at once.
func buildUnreachable(orig []byte) []byte {
	ihl := int(orig[0]&0x0f) * 4
	quote := orig[:min(len(orig), ihl+8)]
	p := make([]byte, 20+8+len(quote))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[8] = 64
	p[9] = protoICMP
	var src, dst [4]byte
	copy(src[:], orig[16:20])
	copy(dst[:], orig[12:16])
	setAddrs(p, src, dst)
	p[20] = 3 // destination unreachable
	p[21] = 3 // port unreachable
	copy(p[28:], quote)
	binary.BigEndian.PutUint16(p[22:24], fold(checksum(0, p[20:])))
	ipChecksum(p[:20])
	return p
}
