// Command tsbridge joins two network namespaces with an emulated path, so
// that two real tailscaled nodes talk over it: it creates a TUN device in
// each namespace and carries IP packets between them with the delay, jitter,
// bottleneck rate and scripted burst loss of netstack-rack-repro's emulated
// path (link-emulator.go). Between two tailscaled nodes every packet is
// WireGuard over UDP, so the burst loss counts large UDP packets, not TCP
// segments: each WireGuard transport message carries one IP packet of the
// tunnel. Any transport message bigger than a bare TCP ACK counts as data, as
// every TCP segment with a payload does in the parent harness's dropper.
//
// SIGUSR1 arms the burst loss for the next transfer toward -dev-b, with the
// parameters in -arm-file; SIGUSR2 appends a report of that transfer, as one
// line of JSON, to -report-file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	nsA        = flag.String("ns-a", "/var/run/netns/srv", "network namespace of the sending side (the server)")
	nsB        = flag.String("ns-b", "/var/run/netns/cli", "network namespace of the receiving side (the client)")
	devA       = flag.String("dev-a", "tsa", "TUN device to create in -ns-a")
	devB       = flag.String("dev-b", "tsb", "TUN device to create in -ns-b")
	delay      = flag.Duration("delay", 1500*time.Microsecond, "one-way propagation delay")
	jitter     = flag.Duration("jitter", 200*time.Microsecond, "extra one-way delay, uniform in [0, jitter), never reordering")
	seed       = flag.Uint64("seed", 1, "jitter seed")
	rateBps    = flag.Float64("rate", 80e6, "bottleneck rate toward -dev-b, in bit/s (0: none)")
	dataMin    = flag.Int("data-min", 100, "UDP payload bytes from which a WireGuard transport message counts as data (a bare ACK of the tunnel's IPv4 TCP is 84)")
	idleGap    = flag.Duration("idle-gap", time.Second, "a transfer starts after this much silence toward -dev-b")
	armFile    = flag.String("arm-file", "arm.json", "burst parameters read at each SIGUSR1")
	reportFile = flag.String("report-file", "reports.jsonl", "where SIGUSR2 appends the last armed transfer's report")
)

// burstParams has the meaning of netstack-rack-repro's -burst-* flags.
type burstParams struct {
	KeepFirst int           `json:"keep_first"`
	DropCount int           `json:"drop_count"`
	Window    time.Duration `json:"window_ns"`
	UntilGap  time.Duration `json:"until_gap_ns"`
}

type report struct {
	Armed         bool    `json:"armed"`
	Started       bool    `json:"started"`
	Kept          int     `json:"kept_before_drop"`
	Dropped       int     `json:"dropped"`
	DropMs        float64 `json:"drop_phase_ms"`
	After         int     `json:"data_pkts_after_drop"`
	Silences100ms int     `json:"silences_over_100ms_after_drop"`
	LongestGapMs  float64 `json:"longest_gap_after_drop_ms"`
	LastDataS     float64 `json:"last_data_s"`
	TotalData     int     `json:"data_pkts_total"`
}

// burst scripts one loss burst per arming, as burstDropper in
// netstack-rack-repro does, and records what happened to the transfer.
type burst struct {
	mu        sync.Mutex
	p         burstParams
	armed     bool
	phase     int // 0 idle, 1 keeping, 2 dropping, 3 done
	lastData  time.Time
	start     time.Time
	firstDrop time.Time
	dropEnd   time.Time
	seen      int
	dropped   int
	after     []time.Time
}

func (b *burst) arm(p burstParams) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.p, b.armed, b.phase = p, true, 0
	b.seen, b.dropped, b.after = 0, 0, b.after[:0]
	b.start, b.firstDrop, b.dropEnd = time.Time{}, time.Time{}, time.Time{}
}

func (b *burst) drop(pkt []byte, now time.Time) bool {
	if !isWireGuardData(pkt) {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	gap := now.Sub(b.lastData)
	idle := b.lastData.IsZero() || gap >= *idleGap
	b.lastData = now
	switch b.phase {
	case 0:
		if !b.armed || !idle {
			return false
		}
		b.phase, b.start = 1, now
		fallthrough
	case 1:
		b.seen++
		if b.seen <= b.p.KeepFirst {
			return false
		}
		b.phase, b.firstDrop = 2, now
		fallthrough
	case 2:
		if b.phase == 2 && b.dropped > 0 {
			b.seen++
		}
		// Why: the same end rules as burstDropper: a count, a window since the
		// first drop, or the sender resuming after a pause (its loss probe).
		if b.dropped >= b.p.DropCount || (b.p.Window > 0 && now.Sub(b.firstDrop) >= b.p.Window) ||
			(b.p.UntilGap > 0 && b.dropped > 0 && gap >= b.p.UntilGap) {
			b.phase, b.dropEnd, b.armed = 3, now, false
			b.after = append(b.after, now)
			return false
		}
		b.dropped++
		return true
	default:
		b.seen++
		b.after = append(b.after, now)
		return false
	}
}

func (b *burst) report() report {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := report{Armed: b.armed || b.phase > 0, Started: !b.start.IsZero(), Dropped: b.dropped, After: len(b.after), TotalData: b.seen}
	r.Kept = min(b.seen, b.p.KeepFirst)
	if !b.dropEnd.IsZero() {
		r.DropMs = float64(b.dropEnd.Sub(b.firstDrop).Microseconds()) / 1000
	}
	prev := b.dropEnd
	for _, t := range b.after {
		g := t.Sub(prev)
		if g >= 100*time.Millisecond {
			r.Silences100ms++
		}
		if ms := float64(g.Microseconds()) / 1000; ms > r.LongestGapMs {
			r.LongestGapMs = ms
		}
		prev = t
	}
	if n := len(b.after); n > 0 {
		r.LastDataS = b.after[n-1].Sub(b.start).Seconds()
	}
	return r
}

// isWireGuardData reports whether an IPv4 or IPv6 packet carries a
// WireGuard transport message (type 4) of at least -data-min bytes. Handshake,
// cookie and disco messages, keepalives and bare ACKs are not data.
func isWireGuardData(p []byte) bool {
	off := -1
	switch {
	case len(p) >= 20 && p[0]>>4 == 4 && p[9] == unix.IPPROTO_UDP:
		off = int(p[0]&0x0f)*4 + 8
	case len(p) >= 40 && p[0]>>4 == 6 && p[6] == unix.IPPROTO_UDP:
		off = 48
	}
	return off >= 0 && len(p) > off && p[off] == 4 && len(p)-off >= *dataMin
}

// direction is one direction of the path: a FIFO bottleneck with a service
// rate, then a propagation delay with jitter that never reorders.
type direction struct {
	rate    float64
	rng     *rand.Rand
	drop    func([]byte, time.Time) bool
	out     *os.File
	mu      sync.Mutex
	cond    *sync.Cond
	busy    time.Time
	last    time.Time
	pending []pending
}

type pending struct {
	b  []byte
	at time.Time
}

func newDirection(rate float64, s uint64, out *os.File, drop func([]byte, time.Time) bool) *direction {
	d := &direction{rate: rate, rng: rand.New(rand.NewPCG(s, s)), out: out, drop: drop}
	d.cond = sync.NewCond(&d.mu)
	go d.deliverLoop()
	return d
}

func (d *direction) send(b []byte) {
	now := time.Now()
	if d.drop != nil && d.drop(b, now) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	start := now
	if d.busy.After(now) {
		start = d.busy
	}
	depart := start
	if d.rate > 0 {
		depart = start.Add(time.Duration(float64(len(b)*8) / d.rate * float64(time.Second)))
	}
	d.busy = depart
	at := depart.Add(*delay)
	if *jitter > 0 {
		at = at.Add(time.Duration(d.rng.Int64N(int64(*jitter))))
	}
	if at.Before(d.last) {
		at = d.last
	}
	d.last = at
	d.pending = append(d.pending, pending{b, at})
	d.cond.Signal()
}

func (d *direction) deliverLoop() {
	for {
		d.mu.Lock()
		for len(d.pending) == 0 {
			d.cond.Wait()
		}
		wait := time.Until(d.pending[0].at)
		if wait > 0 {
			d.mu.Unlock()
			// Why: the Go netpoller sleeps in whole milliseconds, so short
			// sleeps overshoot; sleep to 2 ms before, then yield-spin, as
			// link-emulator.go does.
			if wait > 2*time.Millisecond {
				time.Sleep(wait - 2*time.Millisecond)
			} else {
				runtime.Gosched()
			}
			continue
		}
		now := time.Now()
		n := 0
		for n < len(d.pending) && !d.pending[n].at.After(now) {
			n++
		}
		batch := append([]pending(nil), d.pending[:n]...)
		d.pending = d.pending[n:]
		d.mu.Unlock()
		for _, p := range batch {
			if _, err := d.out.Write(p.b); err != nil {
				log.Printf("write: %v", err)
			}
		}
	}
}

// openTunIn creates a TUN device in the network namespace at nsPath. The
// thread that enters the namespace is never returned to the Go scheduler.
func openTunIn(nsPath, name string) (*os.File, error) {
	type result struct {
		f   *os.File
		err error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread() // Never unlocked: this thread ends in another namespace.
		ns, err := unix.Open(nsPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			ch <- result{nil, err}
			return
		}
		defer unix.Close(ns)
		if err := unix.Setns(ns, unix.CLONE_NEWNET); err != nil {
			ch <- result{nil, fmt.Errorf("setns %s: %w", nsPath, err)}
			return
		}
		fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			ch <- result{nil, err}
			return
		}
		ifr, err := unix.NewIfreq(name)
		if err != nil {
			ch <- result{nil, err}
			return
		}
		ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
		if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
			ch <- result{nil, fmt.Errorf("TUNSETIFF %s: %w", name, err)}
			return
		}
		if err := unix.SetNonblock(fd, true); err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{os.NewFile(uintptr(fd), name), nil}
	}()
	r := <-ch
	return r.f, r.err
}

func pump(in *os.File, d *direction) {
	buf := make([]byte, 65536)
	for {
		n, err := in.Read(buf)
		if err != nil {
			log.Fatalf("read %s: %v", in.Name(), err)
		}
		d.send(append([]byte(nil), buf[:n]...))
	}
}

func main() {
	flag.Parse()
	fa, err := openTunIn(*nsA, *devA)
	if err != nil {
		log.Fatal(err)
	}
	fb, err := openTunIn(*nsB, *devB)
	if err != nil {
		log.Fatal(err)
	}
	var b burst
	toB := newDirection(*rateBps, *seed, fb, b.drop)
	toA := newDirection(0, *seed+1, fa, nil)
	go pump(fa, toB)
	go pump(fb, toA)

	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTERM, syscall.SIGINT)
	log.Printf("tsbridge: %s/%s <-> %s/%s, delay %v, jitter %v, rate %.0f bit/s toward %s", *nsA, *devA, *nsB, *devB, *delay, *jitter, *rateBps, *devB)
	for s := range sig {
		switch s {
		case syscall.SIGUSR1:
			var p burstParams
			raw, err := os.ReadFile(*armFile)
			if err == nil {
				err = json.Unmarshal(raw, &p)
			}
			if err != nil {
				log.Printf("arm: %v", err)
				continue
			}
			b.arm(p)
		case syscall.SIGUSR2:
			line, _ := json.Marshal(b.report())
			f, err := os.OpenFile(*reportFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				log.Printf("report: %v", err)
				continue
			}
			fmt.Fprintf(f, "%s\n", line)
			f.Close()
		default:
			return
		}
	}
}
