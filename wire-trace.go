package main

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
)

const bucketWidth = 100 * time.Millisecond

// traceBucket mirrors the per-100 ms rows in tailscale/tailscale#21946: data
// the server put on the wire (before any loss), how much of it was
// retransmitted, and the ACKs that reached the server.
type traceBucket struct {
	dataPkts, dataBytes int
	retxPkts, retxBytes int
	acks, sackAcks      int
	lostPkts            int
}

// wireTrace records the server's side of one connection, as
// `tailscale debug capture` on the server would see it.
type wireTrace struct {
	server tcpip.Address

	mu         sync.Mutex
	t0         time.Time
	buckets    map[int]*traceBucket
	highestEnd map[flowKey]uint32 // Highest sequence number sent, per connection.
	retxTimes  []time.Duration    // Offsets from t0 of retransmitted packets.
	dataPkts   int
	retxPkts   int
}

type flowKey struct{ srcPort, dstPort uint16 }

func newWireTrace(server tcpip.Address) *wireTrace {
	return &wireTrace{server: server, buckets: map[int]*traceBucket{}, highestEnd: map[flowKey]uint32{}}
}

// reset starts a new measurement window at t0 and forgets earlier buckets. It
// keeps the highest sequence number sent so far, so retransmissions are still
// recognized across windows.
func (w *wireTrace) reset(t0 time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t0 = t0
	w.buckets = map[int]*traceBucket{}
	w.retxTimes = nil
	w.dataPkts, w.retxPkts = 0, 0
}

func (w *wireTrace) bucketLocked(now time.Time) *traceBucket {
	i := int(now.Sub(w.t0) / bucketWidth)
	b := w.buckets[i]
	if b == nil {
		b = &traceBucket{}
		w.buckets[i] = b
	}
	return b
}

// seqLess reports whether a is before b in sequence space (RFC 1982).
func seqLess(a, b uint32) bool { return int32(a-b) < 0 }

func (w *wireTrace) recordSent(now time.Time, p *packetInfo) {
	if !p.isTCP || p.src != w.server || p.payloadLen == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bucketLocked(now)
	b.dataPkts++
	b.dataBytes += p.payloadLen
	w.dataPkts++
	end := p.seq + uint32(p.payloadLen)
	key := flowKey{p.srcPort, p.dstPort}
	high, ok := w.highestEnd[key]
	if ok && seqLess(p.seq, high) {
		retx := p.payloadLen
		if seqLess(high, end) {
			retx = int(high - p.seq)
		}
		b.retxPkts++
		b.retxBytes += retx
		w.retxPkts++
		w.retxTimes = append(w.retxTimes, now.Sub(w.t0))
	}
	if !ok || seqLess(high, end) {
		w.highestEnd[key] = end
	}
}

func (w *wireTrace) recordQueueDrop(now time.Time, p *packetInfo) {
	if !p.isTCP || p.src != w.server || p.payloadLen == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bucketLocked(now).lostPkts++
}

func (w *wireTrace) recordScriptDrop(now time.Time, p *packetInfo) {
	w.recordQueueDrop(now, p)
}

func (w *wireTrace) recordDelivered(now time.Time, raw []byte) {
	p := parsePacket(raw)
	if !p.isTCP || p.dst != w.server || p.payloadLen != 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bucketLocked(now)
	b.acks++
	if p.hasSACK {
		b.sackAcks++
	}
}

// retransmitTimes returns when each retransmitted packet was sent, as offsets
// from the start of the measurement window.
func (w *wireTrace) retransmitTimes() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.retxTimes...)
}

func (w *wireTrace) counts() (data, retx int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dataPkts, w.retxPkts
}

// print writes the non-empty buckets in the format of the excerpt in
// tailscale/tailscale#21946. Long runs are elided in the middle.
func (w *wireTrace) print(out io.Writer, maxRows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var idx []int
	for i := range w.buckets {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	row := func(i int) {
		b := w.buckets[i]
		fmt.Fprintf(out, "  +%5.1fs %5d pkts %8d B  retx %8d B  lost %5d | acks %4d (%d with SACK)\n",
			float64(i)*bucketWidth.Seconds(), b.dataPkts, b.dataBytes, b.retxBytes, b.lostPkts, b.acks, b.sackAcks)
	}
	if len(idx) <= maxRows {
		for _, i := range idx {
			row(i)
		}
		return
	}
	head, tail := maxRows/2, maxRows-maxRows/2
	for _, i := range idx[:head] {
		row(i)
	}
	fmt.Fprintf(out, "  ... %d more 100 ms rows ...\n", len(idx)-head-tail)
	for _, i := range idx[len(idx)-tail:] {
		row(i)
	}
}
