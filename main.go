// Command netstack-rack-repro reproduces tailscale/tailscale#21946: after a
// burst loss, a gVisor (netstack) TCP sender with RACK enabled recovers at a
// few segments per retransmission timeout.
//
// A client stack fetches a response from a server over an emulated path (3 ms
// RTT, 80 Mbit/s by default) after an idle period. The server is either gVisor
// configured the way Tailscale's wgengine/netstack configures it, or the Linux
// kernel's TCP over a TUN device. A scripted dropper cuts the response burst
// the same way every run.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"runtime/debug"
	"slices"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

var (
	senderFlag   = flag.String("sender", "gvisor", "TCP sender: gvisor (configured like Tailscale's netstack) or linux (kernel TCP over TUN; use run-linux-sender.sh)")
	rackFlag     = flag.Bool("rack", true, "gVisor sender: RACK loss detection (on since Tailscale 1.104.0)")
	ccFlag       = flag.String("cc", "cubic", "gVisor sender: congestion control (Tailscale sets cubic)")
	gsoFlag      = flag.Bool("gso", true, "gVisor sender: host GSO up to 64 KiB, as Tailscale uses on Linux")
	clockResFlag = flag.Duration("clock-res", 0, "gVisor sender: stack.Options.ClockResolution (Tailscale sets 500us on Windows only)")
	mtuFlag      = flag.Int("mtu", 1280, "link MTU (Tailscale's default TUN MTU)")
	rttFlag      = flag.Duration("rtt", 3*time.Millisecond, "base round-trip time")
	jitterFlag   = flag.Duration("jitter", 200*time.Microsecond, "extra one-way delay, uniform in [0, jitter), that never reorders packets")
	seedFlag     = flag.Uint64("seed", 1, "seed for the delay jitter")
	rateFlag     = flag.Float64("rate-mbps", 80, "bottleneck rate toward the client in Mbit/s, 0 for none")
	queueFlag    = flag.Int("queue", 0, "bottleneck drop-tail queue in packets, 0 for unlimited")
	lossFlag     = flag.String("loss", "burst", "loss model: burst (scripted, same packets every run), none")
	burstKeep    = flag.Int("burst-keep-first", 1000, "burst loss: deliver the first N data packets of a transfer that starts after an idle period")
	burstDrop    = flag.Int("burst-drop", 1400, "burst loss: then drop up to N data packets, new or retransmitted")
	burstWindow  = flag.Duration("burst-window", 150*time.Millisecond, "burst loss: stop dropping this long after the first drop (0: no limit)")
	burstEvery   = flag.Int("burst-keep-every", 0, "burst loss: deliver one in every M packets while dropping (0: none)")
	burstGap     = flag.Duration("burst-until-gap", 0, "burst loss: stop dropping at the first packet after the sender paused this long (0: off)")
	idleGapFlag  = flag.Duration("idle-gap", 100*time.Millisecond, "burst loss: data-direction idle time that starts a burst")
	sizeFlag     = flag.Int("size", 2_800_000, "response size in bytes")
	warmupFlag   = flag.Int("warmup", 2, "fetches per trial before the measured ones, without scripted loss")
	fetchesFlag  = flag.Int("fetches", 1, "measured fetches per trial, each with scripted loss")
	trialsFlag   = flag.Int("trials", 5, "trials, each on a new connection so every trial starts from the same sender state")
	intervalFlag = flag.Duration("interval", 3*time.Second, "idle time before each fetch (curl --rate 20/m waits 3 s)")
	timeoutFlag  = flag.Duration("timeout", 120*time.Second, "per-fetch timeout")
	linuxCCFlag  = flag.String("linux-cc", "cubic", "Linux sender: congestion control (Ubuntu's default is cubic)")
	traceFlag    = flag.Bool("trace", false, "print the per-100 ms wire trace (and gVisor sender state) for each measured fetch")
)

const serverPort = 8080

var (
	serverAddr = tcpip.AddrFrom4([4]byte{10, 9, 0, 1})
	clientAddr = tcpip.AddrFrom4([4]byte{10, 9, 0, 2})
)

type fetchResult struct {
	trial     int
	n         int
	measured  bool
	elapsed   time.Duration
	err       error
	dataPkts  int
	retxPkts  int
	retxTimes []time.Duration
	rtoTimes  []time.Duration
	stats     statCounters // gVisor sender only.
	samples   []senderSample
}

func main() {
	log.SetFlags(0)
	flag.Parse()
	if *lossFlag != "burst" && *lossFlag != "none" {
		log.Fatalf("unknown -loss %q", *lossFlag)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trace := newWireTrace(serverAddr)
	client, err := newPeerStack("client", clientAddr, uint32(*mtuFlag))
	if err != nil {
		log.Fatal(err)
	}
	dropper := &burstDropper{idleGap: *idleGapFlag, keepFirst: *burstKeep, dropCount: *burstDrop, window: *burstWindow, keepEvery: *burstEvery, untilGap: *burstGap}
	var armed atomic.Bool
	dropper.armed = func() bool { return armed.CompareAndSwap(true, false) }
	s2c := newPathDirection("server->client", *rttFlag/2, *jitterFlag, *seedFlag, *rateFlag*1e6, *queueFlag, client.inject, trace)
	if *lossFlag == "burst" {
		s2c.dropper = dropper.drop
	}

	var (
		c2s        *pathDirection
		rec        *senderRecorder
		serverHost *gvisorHost
		listener   net.Listener
		describe   string
		kernelConn = make(chan net.Conn, 1)
		senderMSS  = *mtuFlag - 40 - 12
	)
	switch *senderFlag {
	case "gvisor":
		rec = &senderRecorder{localPort: serverPort}
		cfg := tailscaleTCPConfig{rack: *rackFlag, cc: *ccFlag, hostGSO: *gsoFlag, clockRes: *clockResFlag}
		serverHost, err = newTailscaleStack("server", serverAddr, uint32(*mtuFlag), cfg, rec.probe)
		if err != nil {
			log.Fatal(err)
		}
		c2s = newPathDirection("client->server", *rttFlag/2, *jitterFlag, *seedFlag+1, 0, 0, serverHost.inject, trace)
		go serverHost.pumpOutbound(ctx, s2c.send)
		listener, err = gonet.ListenTCP(serverHost.stack, tcpip.FullAddress{NIC: nicID, Addr: serverAddr, Port: serverPort}, ipv4.ProtocolNumber)
		if err != nil {
			log.Fatal(err)
		}
		describe = fmt.Sprintf("gVisor %s (RACK %s, %s, host GSO %t, clock resolution %v)", gvisorVersion(), onOff(*rackFlag), *ccFlag, *gsoFlag, *clockResFlag)
	case "linux":
		if err := requireOwnNetns(); err != nil {
			log.Fatal(err)
		}
		tun, err := openTun("tun0", netip.MustParsePrefix("10.9.0.1/24"), *mtuFlag)
		if err != nil {
			log.Fatal(err)
		}
		c2s = newPathDirection("client->server", *rttFlag/2, *jitterFlag, *seedFlag+1, 0, 0, func(b []byte) { tun.Write(b) }, trace)
		go func() {
			buf := make([]byte, 1<<16)
			for {
				n, err := tun.Read(buf)
				if err != nil {
					return
				}
				// Why: the kernel also sends IPv6 router solicitations and MLD
				// reports on a new interface; only the IPv4 test traffic
				// belongs on the path.
				if n == 0 || buf[0]>>4 != 4 {
					continue
				}
				s2c.send(append([]byte(nil), buf[:n]...))
			}
		}()
		listener, err = listenWithCongestionControl(ctx, fmt.Sprintf("%s:%d", serverAddr, serverPort), *linuxCCFlag)
		if err != nil {
			log.Fatal(err)
		}
		describe = "Linux kernel TCP (" + linuxTCPSysctls() + ")"
	default:
		log.Fatalf("unknown -sender %q", *senderFlag)
	}
	go client.pumpOutbound(ctx, c2s.send)

	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i)
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			if *senderFlag == "linux" {
				if err := setCongestionControl(c, *linuxCCFlag); err != nil {
					log.Fatalf("set congestion control %q: %v", *linuxCCFlag, err)
				}
				kernelConn <- c
			}
			go serve(c, payload)
		}
	}()

	fmt.Printf("sender:   %s\n", describe)
	fmt.Printf("path:     RTT %v + up to 2x%v jitter (seed %d), %g Mbit/s toward the client, queue %s, MTU %d\n",
		*rttFlag, *jitterFlag, *seedFlag, *rateFlag, queueDesc(*queueFlag), *mtuFlag)
	if *lossFlag == "burst" {
		fmt.Printf("loss:     in each measured fetch, deliver the first %d data packets, then drop up to %d%s for at most %v%s\n",
			*burstKeep, *burstDrop, everyDesc(*burstEvery), *burstWindow, gapDesc(*burstGap))
	} else {
		fmt.Printf("loss:     none scripted\n")
	}
	fmt.Printf("workload: %d trial(s), each a new connection with %d warm-up + %d measured fetches of %d bytes, %v idle before each\n\n",
		*trialsFlag, *warmupFlag, *fetchesFlag, *sizeFlag, *intervalFlag)

	buf := make([]byte, *sizeFlag)
	var results []fetchResult
	for trial := 1; trial <= *trialsFlag; trial++ {
		conn, err := gonet.DialContextTCP(ctx, client.stack, tcpip.FullAddress{NIC: nicID, Addr: serverAddr, Port: serverPort}, ipv4.ProtocolNumber)
		if err != nil {
			log.Fatalf("dial: %v", err)
		}
		var kconn net.Conn
		if *senderFlag == "linux" {
			kconn = <-kernelConn
			if trial == 1 {
				fmt.Printf("          (kernel connection congestion control: %s)\n", congestionControlOf(kconn))
			}
		}
		for n := 1; n <= *warmupFlag+*fetchesFlag; n++ {
			time.Sleep(*intervalFlag)
			measured := n > *warmupFlag
			armed.Store(measured && *lossFlag == "burst")
			r := fetchResult{trial: trial, n: n, measured: measured}
			t0 := time.Now()
			trace.reset(t0)
			var tw *timeoutWatcher
			var kw *kernelRTOWatcher
			var before statCounters
			if serverHost != nil {
				rec.start(t0)
				tw = watchTimeouts(serverHost.stack, t0)
				before = readStats(serverHost.stack)
			} else {
				kw = watchKernelRTOs(kconn, t0)
			}

			var req [8]byte
			binary.BigEndian.PutUint64(req[:], uint64(*sizeFlag))
			conn.SetDeadline(t0.Add(*timeoutFlag))
			if _, err := conn.Write(req[:]); err != nil {
				r.err = err
			} else if _, err := io.ReadFull(conn, buf); err != nil {
				r.err = err
			}
			r.elapsed = time.Since(t0)
			armed.Store(false)

			if serverHost != nil {
				r.stats = readStats(serverHost.stack).sub(before)
				r.rtoTimes = tw.stop()
				r.samples = rec.stop()
			} else {
				r.rtoTimes = kw.stop()
			}
			r.dataPkts, r.retxPkts = trace.counts()
			r.retxTimes = trace.retransmitTimes()
			printFetch(r)
			if *traceFlag && measured {
				fmt.Printf("  wire trace at the server (data sent before loss; ACKs received):\n")
				trace.print(os.Stdout, 40)
				if len(r.samples) > 0 {
					fmt.Printf("  gVisor sender state after each ACK (first per 100 ms):\n")
					printSenderTimeline(os.Stdout, r.samples, senderMSS, 30)
				}
				fmt.Println()
			}
			results = append(results, r)
			if r.err != nil {
				break
			}
		}
		conn.Close()
	}
	printSummary(results)
	q, s := s2c.drops()
	fmt.Printf("path drops toward the client: %d by the queue, %d scripted\n", q, s)
}

// serve answers each 8-byte request with that many bytes, written in 32 KiB
// chunks like io.Copy does in tailscale serve's proxy.
func serve(c net.Conn, payload []byte) {
	defer c.Close()
	var req [8]byte
	for {
		if _, err := io.ReadFull(c, req[:]); err != nil {
			return
		}
		left := int(binary.BigEndian.Uint64(req[:]))
		for left > 0 {
			n := min(left, 32<<10)
			if _, err := c.Write(payload[:n]); err != nil {
				return
			}
			left -= n
		}
	}
}

func printFetch(r fetchResult) {
	kind := "warm-up "
	if r.measured {
		kind = "measured"
	}
	status := ""
	if r.err != nil {
		status = "  ERROR: " + r.err.Error()
		if errors.Is(r.err, os.ErrDeadlineExceeded) {
			status = "  TIMED OUT"
		}
	}
	fmt.Printf("trial %d fetch %d %s %7.3f s | %5d data pkts, %5d retransmitted | RTOs %3d%s%s\n",
		r.trial, r.n, kind, r.elapsed.Seconds(), r.dataPkts, r.retxPkts, len(r.rtoTimes), perRTO(r), status)
	if r.stats != (statCounters{}) {
		fmt.Printf("         gVisor stats: retransmits %d, timeouts %d, fast retransmits %d, SACK recoveries %d, TLP recoveries %d, spurious RTO recoveries %d\n",
			r.stats.retransmits, r.stats.timeouts, r.stats.fastRetransmit, r.stats.sackRecovery, r.stats.tlpRecovery, r.stats.spuriousRTO)
	}
}

// perRTO describes how many packets were retransmitted between successive
// retransmission timeouts. A sender that only makes progress when its RTO
// fires retransmits a constant handful per timeout.
func perRTO(r fetchResult) string {
	if len(r.rtoTimes) == 0 {
		return ""
	}
	end := r.elapsed
	var counts []int
	for i, start := range r.rtoTimes {
		stop := end
		if i+1 < len(r.rtoTimes) {
			stop = r.rtoTimes[i+1]
		}
		c := 0
		for _, t := range r.retxTimes {
			if t >= start && t < stop {
				c++
			}
		}
		counts = append(counts, c)
	}
	sorted := slices.Clone(counts)
	slices.Sort(sorted)
	return fmt.Sprintf(" (first at +%.2fs) | retransmissions per RTO interval: median %d, max %d",
		r.rtoTimes[0].Seconds(), sorted[len(sorted)/2], sorted[len(sorted)-1])
}

func printSummary(results []fetchResult) {
	var times []time.Duration
	slow := 0
	for _, r := range results {
		if !r.measured {
			continue
		}
		times = append(times, r.elapsed)
		if r.elapsed > time.Second || r.err != nil {
			slow++
		}
	}
	if len(times) == 0 {
		return
	}
	slices.Sort(times)
	fmt.Printf("\nmeasured fetches: %d, min %.3f s, median %.3f s, max %.3f s, over 1 s: %d\n",
		len(times), times[0].Seconds(), times[len(times)/2].Seconds(), times[len(times)-1].Seconds(), slow)
}

func gvisorVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown version)"
	}
	for _, d := range bi.Deps {
		if d.Path == "gvisor.dev/gvisor" {
			if d.Replace != nil {
				return d.Version + " => " + d.Replace.Path
			}
			return d.Version
		}
	}
	return "(unknown version)"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func queueDesc(n int) string {
	if n == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d packets (drop-tail)", n)
}

func gapDesc(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return fmt.Sprintf(", or until the sender resumes after a %v pause", d)
}

func everyDesc(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (delivering one in every %d)", n)
}
