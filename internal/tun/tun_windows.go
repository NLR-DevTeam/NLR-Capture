package tun

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"nlrcapture/internal/netx"
)

// AdapterName is the name of the virtual adapter in Windows.
const AdapterName = "NLR Capture"

var (
	iphlpapi                        = windows.NewLazySystemDLL("iphlpapi.dll")
	procConvertInterfaceLuidToIndex = iphlpapi.NewProc("ConvertInterfaceLuidToIndex")
)

// Handler takes a TCP connection redirected by the adapter. dst is where
// the client was connecting to, src the client's own address.
type Handler func(c net.Conn, src, dst netip.AddrPort)

// Options configure the device.
type Options struct {
	DLLPath   string
	BlockQUIC bool
	Handler   Handler
	// Log reports noteworthy events.
	Log func(string)
}

// Device is a running virtual adapter.
type Device struct {
	opts     Options
	adapter  *adapter
	sess     *session
	index    uint32 // the adapter's interface index
	phys     uint32 // the physical interface outgoing traffic is bound to
	listener net.Listener
	nat      *nat

	mu     sync.Mutex
	udp    map[flowKey]*udpFlow
	closed chan struct{}
	wg     sync.WaitGroup
	routes []string
}

// PhysicalIndex returns the interface outgoing connections must be bound
// to while the device runs.
func (d *Device) PhysicalIndex() uint32 { return d.phys }

func netsh(args ...string) error {
	cmd := exec.Command("netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %v: %v %s", args, err, decodeOEM(out))
	}
	return nil
}

func decodeOEM(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
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

// Start creates the adapter, takes the default routes and begins
// redirecting traffic. It needs administrator rights.
func Start(opts Options) (*Device, error) {
	w, err := loadWintun(opts.DLLPath)
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
	d := &Device{opts: opts, adapter: ad, phys: phys, udp: map[flowKey]*udpFlow{}, closed: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			d.teardown()
		}
	}()
	luid := ad.luid()
	r, _, _ := procConvertInterfaceLuidToIndex.Call(uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&d.index)))
	if r != 0 {
		return nil, fmt.Errorf("无法取得虚拟网卡序号：%d", r)
	}
	if d.index == phys {
		return nil, errors.New("虚拟网卡与物理网卡冲突")
	}
	idx := strconv.Itoa(int(d.index))
	if err := netsh("interface", "ipv4", "set", "address", "name="+idx, "source=static", "address="+tunAddr.String(), "mask=255.255.255.252", "gateway=none", "store=active"); err != nil {
		return nil, err
	}
	netsh("interface", "ipv4", "set", "interface", idx, "metric=1", "store=active")
	netsh("interface", "ipv4", "set", "subinterface", idx, "mtu=1500", "store=active")

	sess, err := ad.start(0x400000)
	if err != nil {
		return nil, err
	}
	d.sess = sess

	// The address takes a moment to become usable.
	var l net.Listener
	for i := 0; i < 50; i++ {
		l, err = net.Listen("tcp4", net.JoinHostPort(tunAddr.String(), "0"))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("虚拟网卡地址不可用：%w", err)
	}
	d.listener = l
	d.nat = newNAT(uint16(l.Addr().(*net.TCPAddr).Port))

	d.wg.Add(3)
	go d.readLoop()
	go d.acceptLoop()
	go d.gcLoop()

	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := netsh("interface", "ipv4", "add", "route", "prefix="+prefix, "interface="+idx, "nexthop="+fakeAddr.String(), "metric=1", "store=active"); err != nil {
			return nil, err
		}
		d.routes = append(d.routes, prefix)
	}
	ok = true
	return d, nil
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
	idx := strconv.Itoa(int(d.index))
	for _, prefix := range d.routes {
		netsh("interface", "ipv4", "delete", "route", "prefix="+prefix, "interface="+idx, "nexthop="+fakeAddr.String(), "store=active")
	}
	d.routes = nil
	if d.listener != nil {
		d.listener.Close()
	}
	d.mu.Lock()
	for k, f := range d.udp {
		f.conn.Close()
		delete(d.udp, k)
	}
	d.mu.Unlock()
	if d.sess != nil {
		// Ending the session wakes the reader.
		d.sess.sendMu.Lock()
		d.sess.end()
		d.sess.sendMu.Unlock()
	}
	d.wg.Wait()
	if d.adapter != nil {
		d.adapter.close()
		d.adapter = nil
	}
}

func (d *Device) readLoop() {
	defer d.wg.Done()
	buf := make([]byte, 65536)
	sess := d.sess
	for {
		select {
		case <-d.closed:
			return
		default:
		}
		sess.sendMu.Lock()
		if sess.handle == 0 {
			sess.sendMu.Unlock()
			return
		}
		n, err := sess.receive(buf)
		sess.sendMu.Unlock()
		if err == errNoMore {
			windows.WaitForSingleObject(sess.event, 200)
			continue
		}
		if err != nil {
			if err == windows.ERROR_HANDLE_EOF {
				return
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
			d.sess.send(p)
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
		d.sess.send(buildUnreachable(p))
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
		d.sess.send(buildUDP(from, f.key.srcAddr(), buf[:n]))
	}
}
