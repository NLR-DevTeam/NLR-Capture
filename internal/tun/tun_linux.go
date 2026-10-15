package tun

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// AdapterName is the name of the virtual interface.
const AdapterName = "nlrcapture0"

// The device and its routes need CAP_NET_ADMIN. The program gets it either
// by running as root or from file capabilities (see osutil.Elevated); the
// ip commands it runs inherit it as an ambient capability.
const capNetAdmin = 12

// Ready reports why the virtual adapter cannot be started, or nil.
func Ready() error {
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return errors.New("系统没有 /dev/net/tun")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return errors.New("缺少 ip 命令，请安装 iproute2")
	}
	return nil
}

// ip runs an iproute2 command with the network capability.
func ip(args ...string) error {
	cmd := exec.Command("ip", args...)
	if os.Geteuid() != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{AmbientCaps: []uintptr{capNetAdmin}}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 300 {
			out = out[:300]
		}
		return fmt.Errorf("ip %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// defaultInterface returns the interface of the default route with the
// lowest metric, which carries traffic to the internet now.
func defaultInterface() (uint32, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	best, bestMetric := "", -1
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		fs := strings.Fields(sc.Text())
		if len(fs) < 8 || fs[1] != "00000000" || fs[7] != "00000000" || fs[0] == AdapterName {
			continue
		}
		m, _ := strconv.Atoi(fs[6])
		if bestMetric < 0 || m < bestMetric {
			best, bestMetric = fs[0], m
		}
	}
	if best == "" {
		return 0, errors.New("找不到默认路由")
	}
	ifc, err := net.InterfaceByName(best)
	if err != nil {
		return 0, fmt.Errorf("找不到网络接口 %s：%w", best, err)
	}
	return uint32(ifc.Index), nil
}

// fileLink is a TUN device opened from /dev/net/tun. The descriptor is
// non-blocking, so the runtime poller handles it and closing it wakes a
// pending read.
type fileLink struct{ f *os.File }

func (l fileLink) read(buf []byte) (int, error) {
	n, err := l.f.Read(buf)
	if errors.Is(err, os.ErrClosed) {
		return 0, errClosed
	}
	return n, err
}

func (l fileLink) write(p []byte) error {
	_, err := l.f.Write(p)
	return err
}

func (l fileLink) close() { l.f.Close() }

func openTUN(name string) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 /dev/net/tun 失败：%w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EPERM) {
			return nil, errors.New("没有创建虚拟网卡的权限")
		}
		return nil, fmt.Errorf("创建虚拟网卡失败：%w", err)
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun"), nil
}

// Start creates the device, takes the default routes and begins
// redirecting traffic. It needs CAP_NET_ADMIN and CAP_NET_RAW.
func Start(opts Options) (*Device, error) {
	if err := Ready(); err != nil {
		return nil, err
	}
	phys, err := defaultInterface()
	if err != nil {
		return nil, err
	}
	// A device left by a run that was killed holds the name.
	ip("link", "delete", AdapterName)
	f, err := openTUN(AdapterName)
	if err != nil {
		return nil, err
	}
	d := newDevice(opts, phys)
	d.link = fileLink{f}
	ok := false
	defer func() {
		if !ok {
			d.teardown()
		}
	}()
	if err := ip("addr", "add", tunAddr.String()+"/30", "dev", AdapterName); err != nil {
		return nil, err
	}
	if err := ip("link", "set", "dev", AdapterName, "mtu", "1500", "up"); err != nil {
		return nil, err
	}
	if err := d.run(); err != nil {
		return nil, err
	}
	var routes []string
	d.unroute = func() {
		for _, prefix := range routes {
			ip("route", "delete", prefix, "dev", AdapterName)
		}
	}
	// Two halves of the address space win over the default route without
	// touching it, so the system's own routes stay as they were.
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := ip("route", "add", prefix, "dev", AdapterName); err != nil {
			return nil, err
		}
		routes = append(routes, prefix)
	}
	ok = true
	return d, nil
}
