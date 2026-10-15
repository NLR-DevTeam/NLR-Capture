package tun

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func tcpPacket(src, dst netip.AddrPort, flags byte) []byte {
	p := make([]byte, 40)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], 40)
	p[8] = 64
	p[9] = protoTCP
	setAddrs(p, src.Addr().As4(), dst.Addr().As4())
	binary.BigEndian.PutUint16(p[20:22], src.Port())
	binary.BigEndian.PutUint16(p[22:24], dst.Port())
	p[32] = 5 << 4
	p[33] = flags
	ipChecksum(p[:20])
	l4Checksum(p, 20, protoTCP)
	return p
}

func valid(t *testing.T, p []byte) {
	t.Helper()
	if fold(checksum(0, p[:20])) != 0 {
		t.Fatal("bad IP checksum")
	}
	var pseudo [12]byte
	copy(pseudo[0:8], p[12:20])
	pseudo[9] = p[9]
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(p)-20))
	if fold(checksum(checksum(0, pseudo[:]), p[20:])) != 0 {
		t.Fatal("bad TCP checksum")
	}
}

func addrs(p []byte) (netip.AddrPort, netip.AddrPort) {
	s := netip.AddrPortFrom(netip.AddrFrom4([4]byte(p[12:16])), binary.BigEndian.Uint16(p[20:22]))
	d := netip.AddrPortFrom(netip.AddrFrom4([4]byte(p[16:20])), binary.BigEndian.Uint16(p[22:24]))
	return s, d
}

func TestNATRoundTrip(t *testing.T) {
	n := newNAT(4321)
	client := netip.MustParseAddrPort("172.19.0.1:50000")
	server := netip.MustParseAddrPort("93.184.216.34:443")
	now := time.Now()

	syn := tcpPacket(client, server, 0x02)
	if !n.rewriteTCP(syn, now) {
		t.Fatal("SYN dropped")
	}
	valid(t, syn)
	s, d := addrs(syn)
	if s.Addr() != fakeAddr || d != netip.AddrPortFrom(tunAddr, 4321) {
		t.Fatalf("SYN rewritten to %v -> %v", s, d)
	}
	k, ok := n.lookup(s.Port())
	if !ok || k.srcAddr() != client || k.dstAddr() != server {
		t.Fatalf("lookup %v %v", k, ok)
	}

	synack := tcpPacket(netip.AddrPortFrom(tunAddr, 4321), s, 0x12)
	if !n.rewriteTCP(synack, now) {
		t.Fatal("SYN-ACK dropped")
	}
	valid(t, synack)
	s2, d2 := addrs(synack)
	if s2 != server || d2 != client {
		t.Fatalf("reply rewritten to %v -> %v", s2, d2)
	}

	// A stray non-SYN packet without a flow is dropped.
	stray := tcpPacket(netip.MustParseAddrPort("172.19.0.1:50001"), server, 0x10)
	if n.rewriteTCP(stray, now) {
		t.Fatal("stray packet passed")
	}
}

func TestUDPBuild(t *testing.T) {
	p := buildUDP(netip.MustParseAddrPort("8.8.8.8:53"), netip.MustParseAddrPort("172.19.0.1:5353"), []byte("hello"))
	if fold(checksum(0, p[:20])) != 0 {
		t.Fatal("bad IP checksum")
	}
	var pseudo [12]byte
	copy(pseudo[0:8], p[12:20])
	pseudo[9] = protoUDP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(p)-20))
	if fold(checksum(checksum(0, pseudo[:]), p[20:])) != 0 {
		t.Fatal("bad UDP checksum")
	}
	u := buildUnreachable(p)
	if u[20] != 3 || u[21] != 3 || fold(checksum(0, u[20:])) != 0 {
		t.Fatal("bad ICMP")
	}
}
