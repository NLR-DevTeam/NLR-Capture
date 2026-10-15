package tun

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"nlrcapture/internal/netx"
)

// Handler takes a TCP connection redirected by the adapter. dst is where
// the client was connecting to, src the client's own address.
type Handler func(c net.Conn, src, dst netip.AddrPort)

// Options configure the device.
type Options struct {
	BlockQUIC bool
	Handler   Handler
	// Log reports noteworthy events.
	Log func(string)
}

// link is the platform's virtual adapter: it reads the packets the system
// routes into it and writes the packets the system receives from it.
type link interface {
	// read blocks for the next packet; it returns errClosed once the link
	// is closed.
	read(buf []byte) (int, error)
	write(p []byte) error
	// close stops the link and wakes a blocked read.
	close()
}

var errClosed = errors.New("tun: closed")

// Device is a running virtual adapter.
type Device struct {
	opts     Options
	link     link
	phys     uint32 // the physical interface outgoing traffic is bound to
	listener net.Listener
	nat      *nat

	// unroute takes the routes back; release frees the adapter once the
	// loops stopped. Both are set by the platform's Start.
	unroute func()
	release func()

	mu     sync.Mutex
	udp    map[flowKey]*udpFlow
	closed chan struct{}
	wg     sync.WaitGroup
}

func newDevice(opts Options, phys uint32) *Device {
	return &Device{opts: opts, phys: phys, udp: map[flowKey]*udpFlow{}, closed: make(chan struct{})}
}

// PhysicalIndex returns the interface outgoing connections must be bound
// to while the device runs.
func (d *Device) PhysicalIndex() uint32 { return d.phys }

// run listens for the redirected connections on the adapter's address and
// starts the loops.
func (d *Device) run() error {
	// The address takes a moment to become usable.
	var l net.Listener
	var err error
	for i := 0; i < 50; i++ {
		l, err = net.Listen("tcp4", net.JoinHostPort(tunAddr.String(), "0"))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("虚拟网卡地址不可用：%w", err)
	}
	d.listener = l
	d.nat = newNAT(uint16(l.Addr().(*net.TCPAddr).Port))
	d.wg.Add(3)
	go d.readLoop()
	go d.acceptLoop()
	go d.gcLoop()
	return nil
}

func (d *Device) log(format string, args ...any) {
	if d.opts.Log != nil {
		d.opts.Log(fmt.Sprintf(format, args...))
	}
}

// Close removes the routes and the adapter.
func (d *Device) Close() {
	select {
	case <-d.closed:
		return
	default:
	}
	d.teardown()
}

func (d *Device) teardown() {
	select {
	case <-d.closed:
	default:
		close(d.closed)
	}
	if d.unroute != nil {
		d.unroute()
		d.unroute = nil
	}
	if d.listener != nil {
		d.listener.Close()
	}
	d.mu.Lock()
	for k, f := range d.udp {
		f.conn.Close()
		delete(d.udp, k)
	}
	d.mu.Unlock()
	if d.link != nil {
		d.link.close()
	}
	d.wg.Wait()
	if d.release != nil {
		d.release()
		d.release = nil
	}
}

func (d *Device) readLoop() {
	defer d.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, err := d.link.read(buf)
		if err == errClosed {
			return
		}
		if err != nil {
			select {
			case <-d.closed:
				return
			default:
			}
			d.log("虚拟网卡读取失败：%v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		d.packet(buf[:n])
	}
}

func (d *Device) packet(p []byte) {
	if len(p) < 20 || p[0]>>4 != 4 {
		// IPv6 is not redirected; its routes are left alone.
		return
	}
	ihl := int(p[0]&0x0f) * 4
	if ihl < 20 || len(p) < ihl+8 {
		return
	}
	// Fragments other than the first carry no ports.
	if binary.BigEndian.Uint16(p[6:8])&0x1fff != 0 {
		return
	}
	dst := netip.AddrFrom4([4]byte(p[16:20]))
	if dst.IsMulticast() || dst == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return
	}
	switch p[9] {
	case protoTCP:
		if d.nat.rewriteTCP(p, time.Now()) {
			d.link.write(p)
		}
	case protoUDP:
		d.udpPacket(p, ihl)
	}
}

func (d *Device) acceptLoop() {
	defer d.wg.Done()
	for {
		c, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.closed:
				return
			default:
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		ra := c.RemoteAddr().(*net.TCPAddr)
		k, ok := d.nat.lookup(uint16(ra.Port))
		if !ok {
			c.Close()
			continue
		}
		go d.opts.Handler(c, k.srcAddr(), k.dstAddr())
	}
}

func (d *Device) gcLoop() {
	defer d.wg.Done()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.closed:
			return
		case now := <-t.C:
			d.nat.gc(now)
			d.mu.Lock()
			for k, f := range d.udp {
				if now.Sub(f.lastSeen()) > 90*time.Second {
					f.conn.Close()
					delete(d.udp, k)
				}
			}
			d.mu.Unlock()
		}
	}
}

// ---- UDP --------------------------------------------------------------

type udpFlow struct {
	key  flowKey
	conn *net.UDPConn
	mu   sync.Mutex
	last time.Time
}

func (f *udpFlow) touch() {
	f.mu.Lock()
	f.last = time.Now()
	f.mu.Unlock()
}

func (f *udpFlow) lastSeen() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (d *Device) udpPacket(p []byte, ihl int) {
	u := p[ihl:]
	k := flowKey{sport: binary.BigEndian.Uint16(u[0:2]), dport: binary.BigEndian.Uint16(u[2:4])}
	copy(k.src[:], p[12:16])
	copy(k.dst[:], p[16:20])
	ulen := int(binary.BigEndian.Uint16(u[4:6]))
	if ulen < 8 || ulen > len(u) {
		return
	}
	payload := u[8:ulen]
	if d.opts.BlockQUIC && k.dport == 443 {
		d.link.write(buildUnreachable(p))
		return
	}
	d.mu.Lock()
	f, ok := d.udp[k]
	if !ok {
		lc := net.ListenConfig{Control: netx.BindControl(d.phys)}
		pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
		if err != nil {
			d.mu.Unlock()
			return
		}
		f = &udpFlow{key: k, conn: pc.(*net.UDPConn), last: time.Now()}
		d.udp[k] = f
		go d.udpReply(f)
	}
	d.mu.Unlock()
	f.touch()
	f.conn.WriteToUDPAddrPort(payload, k.dstAddr())
}

func (d *Device) udpReply(f *udpFlow) {
	buf := make([]byte, 65536)
	for {
		f.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		n, from, err := f.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			d.mu.Lock()
			if d.udp[f.key] == f {
				delete(d.udp, f.key)
			}
			d.mu.Unlock()
			f.conn.Close()
			return
		}
		f.touch()
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if !from.Addr().Is4() {
			continue
		}
		d.link.write(buildUDP(from, f.key.srcAddr(), buf[:n]))
	}
}
