package main

import (
	"math/rand/v2"
	"sync"
	"time"
)

// pathDirection emulates one direction of the path between the two TCP
// endpoints: a drop-tail bottleneck queue with a fixed service rate, followed
// by a propagation delay with optional jitter. A scripted dropper can discard
// packets before they reach the queue.
type pathDirection struct {
	name     string
	delay    time.Duration
	jitter   time.Duration // Extra delay, uniform in [0, jitter), never reordering.
	rateBps  float64       // 0 means the bottleneck adds no serialization delay.
	queueCap int           // 0 means the queue never overflows.
	dropper  func(p *packetInfo, now time.Time) bool
	deliver  func(b []byte)
	trace    *wireTrace

	mu          sync.Mutex
	cond        *sync.Cond
	rng         *rand.Rand
	busyUntil   time.Time
	lastDeliver time.Time
	departs     []time.Time // Departure times of packets still in the queue.
	inFlight    []scheduledPacket
	closed      bool

	queueDrops  int
	scriptDrops int
}

type scheduledPacket struct {
	b         []byte
	deliverAt time.Time
}

func newPathDirection(name string, delay, jitter time.Duration, seed uint64, rateBps float64, queueCap int, deliver func([]byte), trace *wireTrace) *pathDirection {
	d := &pathDirection{
		name:     name,
		delay:    delay,
		jitter:   jitter,
		rng:      rand.New(rand.NewPCG(seed, seed)),
		rateBps:  rateBps,
		queueCap: queueCap,
		deliver:  deliver,
		trace:    trace,
	}
	d.cond = sync.NewCond(&d.mu)
	go d.deliverLoop()
	return d
}

// send offers one IP packet to the path. It never blocks.
func (d *pathDirection) send(b []byte) {
	now := time.Now()
	info := parsePacket(b)
	if d.trace != nil {
		d.trace.recordSent(now, &info)
	}
	if d.dropper != nil && d.dropper(&info, now) {
		d.mu.Lock()
		d.scriptDrops++
		d.mu.Unlock()
		if d.trace != nil {
			d.trace.recordScriptDrop(now, &info)
		}
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for len(d.departs) > 0 && !d.departs[0].After(now) {
		d.departs = d.departs[1:]
	}
	if d.queueCap > 0 && len(d.departs) >= d.queueCap {
		d.queueDrops++
		if d.trace != nil {
			d.trace.recordQueueDrop(now, &info)
		}
		return
	}
	start := now
	if d.busyUntil.After(now) {
		start = d.busyUntil
	}
	depart := start
	if d.rateBps > 0 {
		depart = start.Add(time.Duration(float64(len(b)*8) / d.rateBps * float64(time.Second)))
	}
	d.busyUntil = depart
	d.departs = append(d.departs, depart)
	deliverAt := depart.Add(d.delay)
	// Why: on a path with a constant delay, an RTO retransmission's RTT ties
	// RACK's minimum RTT, and RACK discards retransmission RTT samples below
	// it (rack.go, rackControl.update), so whether loss recovery starts would
	// depend on scheduling noise. Real Wi-Fi paths have far more jitter than
	// this; the seeded generator keeps runs comparable. Delivery order is kept
	// so the jitter never looks like reordering.
	if d.jitter > 0 {
		deliverAt = deliverAt.Add(time.Duration(d.rng.Int64N(int64(d.jitter))))
	}
	if deliverAt.Before(d.lastDeliver) {
		deliverAt = d.lastDeliver
	}
	d.lastDeliver = deliverAt
	d.inFlight = append(d.inFlight, scheduledPacket{b: b, deliverAt: deliverAt})
	d.cond.Signal()
}

// deliverLoop hands packets to the receiver in FIFO order once their delivery
// time has passed. FIFO order is also time order because the queue is FIFO and
// the propagation delay is constant.
func (d *pathDirection) deliverLoop() {
	var batch [][]byte
	for {
		d.mu.Lock()
		for len(d.inFlight) == 0 && !d.closed {
			d.cond.Wait()
		}
		if d.closed {
			d.mu.Unlock()
			return
		}
		wait := time.Until(d.inFlight[0].deliverAt)
		if wait > 0 {
			d.mu.Unlock()
			time.Sleep(wait)
			continue
		}
		now := time.Now()
		batch = batch[:0]
		n := 0
		for n < len(d.inFlight) && !d.inFlight[n].deliverAt.After(now) {
			batch = append(batch, d.inFlight[n].b)
			n++
		}
		d.inFlight = d.inFlight[n:]
		d.mu.Unlock()
		for _, b := range batch {
			if d.trace != nil {
				d.trace.recordDelivered(now, b)
			}
			d.deliver(b)
		}
	}
}

func (d *pathDirection) close() {
	d.mu.Lock()
	d.closed = true
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *pathDirection) drops() (queue, script int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queueDrops, d.scriptDrops
}

// burstDropper scripts one loss burst after the data direction has been
// idle: it delivers the first keepFirst data packets of the transfer, then
// drops data packets, new or retransmitted, until dropCount have been dropped,
// window has passed since the first drop, or (if untilGap is non-zero) the
// sender resumes after a pause of at least untilGap, whichever comes first.
// If keepEvery is non-zero, one in every keepEvery packets in the drop phase
// is delivered. Packets are counted, not timed, until the drop phase starts, so
// every run loses the same segments of the response; the window only bounds
// how long the path stays lossy once the sender stalls.
type burstDropper struct {
	idleGap   time.Duration
	keepFirst int
	dropCount int
	window    time.Duration
	untilGap  time.Duration
	keepEvery int
	armed     func() bool // Only transfers that start while armed() is true lose packets.

	mu        sync.Mutex
	lastData  time.Time
	phase     burstPhase
	seen      int
	dropped   int
	firstDrop time.Time
}

type burstPhase int

const (
	burstIdle burstPhase = iota
	burstKeeping
	burstDropping
)

func (b *burstDropper) drop(p *packetInfo, now time.Time) bool {
	if !p.isTCP || p.payloadLen == 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	gap := now.Sub(b.lastData)
	idle := b.lastData.IsZero() || gap >= b.idleGap
	b.lastData = now
	if b.phase == burstIdle {
		if !idle || (b.armed != nil && !b.armed()) {
			return false
		}
		b.phase = burstKeeping
		b.seen, b.dropped = 0, 0
	}
	b.seen++
	if b.phase == burstKeeping {
		if b.seen <= b.keepFirst {
			return false
		}
		b.phase = burstDropping
		b.firstDrop = now
	}
	// Why: a sender whose whole tail was lost goes quiet until its loss
	// probe or RTO fires, while an ACK-clocked sender emits a packet every
	// ~0.13 ms at 80 Mbit/s. A pause of a few milliseconds therefore marks
	// the first probe after a tail loss, independent of the cwnd.
	if b.dropped >= b.dropCount || (b.window > 0 && now.Sub(b.firstDrop) >= b.window) ||
		(b.untilGap > 0 && b.dropped > 0 && gap >= b.untilGap) {
		b.phase = burstIdle
		return false
	}
	if b.keepEvery > 0 && (b.seen-b.keepFirst)%b.keepEvery == 0 {
		return false
	}
	b.dropped++
	return true
}
