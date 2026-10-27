//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// requireOwnNetns refuses to touch interfaces unless this process runs in a
// network namespace of its own, so the Linux comparison can never reconfigure
// the host's network. run-linux-sender.sh passes the namespace it started in;
// without it, any interface with an address other than loopback is taken to
// mean this is not a fresh namespace.
func requireOwnNetns() error {
	self, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return err
	}
	if host := os.Getenv("REPRO_HOST_NETNS"); host != "" {
		if self == host {
			return errors.New("-sender=linux must run in its own network namespace; use run-linux-sender.sh")
		}
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		if ifc.Flags&net.FlagLoopback == 0 && len(addrs) > 0 {
			return fmt.Errorf("interface %s has addresses, so this is not a fresh network namespace; use run-linux-sender.sh", ifc.Name)
		}
	}
	return nil
}

// openTun creates a TUN interface with the given address and MTU and brings it
// and loopback up.
func openTun(name string, prefix netip.Prefix, mtu int) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return nil, err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		return nil, fmt.Errorf("TUNSETIFF: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}

	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(sock)
	set := func(req uint, fill func(*unix.Ifreq) error) error {
		r, err := unix.NewIfreq(name)
		if err != nil {
			return err
		}
		if err := fill(r); err != nil {
			return err
		}
		return unix.IoctlIfreq(sock, req, r)
	}
	if err := set(unix.SIOCSIFADDR, func(r *unix.Ifreq) error { return r.SetInet4Addr(prefix.Addr().AsSlice()) }); err != nil {
		return nil, fmt.Errorf("SIOCSIFADDR: %w", err)
	}
	mask := net.CIDRMask(prefix.Bits(), 32)
	if err := set(unix.SIOCSIFNETMASK, func(r *unix.Ifreq) error { return r.SetInet4Addr(mask) }); err != nil {
		return nil, fmt.Errorf("SIOCSIFNETMASK: %w", err)
	}
	if err := set(unix.SIOCSIFMTU, func(r *unix.Ifreq) error { r.SetUint32(uint32(mtu)); return nil }); err != nil {
		return nil, fmt.Errorf("SIOCSIFMTU: %w", err)
	}
	for _, ifname := range []string{"lo", name} {
		r, err := unix.NewIfreq(ifname)
		if err != nil {
			return nil, err
		}
		if err := unix.IoctlIfreq(sock, unix.SIOCGIFFLAGS, r); err != nil {
			return nil, fmt.Errorf("SIOCGIFFLAGS %s: %w", ifname, err)
		}
		r.SetUint16(r.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
		if err := unix.IoctlIfreq(sock, unix.SIOCSIFFLAGS, r); err != nil {
			return nil, fmt.Errorf("SIOCSIFFLAGS %s: %w", ifname, err)
		}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// listenWithCongestionControl listens on addr with the given congestion
// control set on the listening socket, so accepted connections start with it.
//
// Why: connections accepted from a listener that has the namespace default
// (BBR on this test machine) run bbr_init during the handshake, which turns
// on TCP internal pacing (sk_pacing_status), and switching the accepted
// socket to CUBIC afterwards does not turn pacing off. Stock CUBIC does not
// pace. A congestion control set on the listener is inherited by children.
func listenWithCongestionControl(ctx context.Context, addr, cc string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		if err := rc.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, cc)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(ctx, "tcp4", addr)
}

// setCongestionControl selects the congestion control of one kernel TCP
// connection.
//
// Why: a new network namespace may only switch its default to an algorithm in
// the host's tcp_allowed_congestion_control, which can be just the host's own
// default (BBR on this test machine). TCP_CONGESTION on the socket works with
// CAP_NET_ADMIN, which the namespace's root has.
func setCongestionControl(c net.Conn, name string) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("not a kernel TCP connection: %T", c)
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, name)
	}); err != nil {
		return err
	}
	return serr
}

// congestionControlOf returns the congestion control of a kernel TCP
// connection.
func congestionControlOf(c net.Conn) string {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return "?"
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return "?"
	}
	name := "?"
	rc.Control(func(fd uintptr) {
		if v, err := unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION); err == nil {
			name = v
		}
	})
	return name
}

// linuxTCPSysctls returns the sysctls that shape the kernel sender's loss
// recovery, read from this network namespace.
func linuxTCPSysctls() string {
	var parts []string
	for _, k := range []string{"tcp_congestion_control", "tcp_recovery", "tcp_sack", "tcp_slow_start_after_idle", "tcp_early_retrans"} {
		b, err := os.ReadFile("/proc/sys/net/ipv4/" + k)
		v := strings.TrimSpace(string(b))
		if err != nil {
			v = "?"
		}
		parts = append(parts, k+"="+v)
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		parts = append([]string{"kernel " + unix.ByteSliceToString(u.Release[:])}, parts...)
	}
	return strings.Join(parts, " ")
}

// kernelInfo reads TCP_INFO from a kernel TCP connection.
func kernelInfo(c net.Conn) (*unix.TCPInfo, error) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return nil, fmt.Errorf("not a kernel TCP connection: %T", c)
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var info *unix.TCPInfo
	var serr error
	err = rc.Control(func(fd uintptr) {
		info, serr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	})
	if err != nil {
		return nil, err
	}
	return info, serr
}

// kernelRTOWatcher polls TCP_INFO and records when tcpi_total_rto increases.
type kernelRTOWatcher struct {
	mu    sync.Mutex
	times []time.Duration
	stopc chan struct{}
	done  chan struct{}
}

func watchKernelRTOs(c net.Conn, t0 time.Time) *kernelRTOWatcher {
	w := &kernelRTOWatcher{stopc: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		var last uint16
		if info, err := kernelInfo(c); err == nil {
			last = info.Total_rto
		}
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-w.stopc:
				return
			case now := <-tick.C:
				info, err := kernelInfo(c)
				if err != nil {
					continue
				}
				w.mu.Lock()
				for ; last < info.Total_rto; last++ {
					w.times = append(w.times, now.Sub(t0))
				}
				w.mu.Unlock()
			}
		}
	}()
	return w
}

func (w *kernelRTOWatcher) stop() []time.Duration {
	close(w.stopc)
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.times...)
}
