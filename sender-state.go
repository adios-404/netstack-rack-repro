package main

import (
	"fmt"
	"io"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// senderSample is the gVisor sender's congestion state after it processed one
// ACK, as reported by gVisor's TCP probe.
type senderSample struct {
	at          time.Duration
	cwnd        int
	ssthresh    int
	outstanding int // RFC 6675 pipe while in SACK recovery.
	sackedOut   int
	inRecovery  bool
	unacked     uint32 // SND.NXT - SND.UNA in bytes.
	rto         time.Duration
}

// senderRecorder keeps the probe samples for the server's endpoint.
type senderRecorder struct {
	localPort uint16

	mu      sync.Mutex
	t0      time.Time
	on      bool
	samples []senderSample
	sendTS  bool // Whether the connection uses TCP timestamps.
	probed  bool // Whether a probe has reported sendTS yet.
}

func (r *senderRecorder) probe(st *tcp.TCPEndpointState) {
	if st.ID.LocalPort != r.localPort {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sendTS, r.probed = st.SendTSOk, true
	if !r.on {
		return
	}
	snd := &st.Sender
	r.samples = append(r.samples, senderSample{
		at:          time.Since(r.t0),
		cwnd:        snd.SndCwnd,
		ssthresh:    snd.Ssthresh,
		outstanding: snd.Outstanding,
		sackedOut:   snd.SackedOut,
		inRecovery:  snd.FastRecovery.Active,
		unacked:     uint32(snd.SndNxt - snd.SndUna),
		rto:         snd.RTO,
	})
}

// timestamps reports whether the server's connection uses TCP timestamps, as
// of the last probe, and whether any probe has run yet.
func (r *senderRecorder) timestamps() (on, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sendTS, r.probed
}

func (r *senderRecorder) start(t0 time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t0 = t0
	r.on = true
	r.samples = r.samples[:0]
}

func (r *senderRecorder) stop() []senderSample {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.on = false
	return append([]senderSample(nil), r.samples...)
}

// printSenderTimeline prints the first sample in each 100 ms bucket. While a
// sender is stuck with outstanding >= cwnd, nothing is sent on ACK arrival.
func printSenderTimeline(out io.Writer, samples []senderSample, mss int, maxRows int) {
	var rows []senderSample
	last := -1
	for _, s := range samples {
		if b := int(s.at / bucketWidth); b != last {
			rows = append(rows, s)
			last = b
		}
	}
	row := func(s senderSample) {
		fmt.Fprintf(out, "  +%5.1fs cwnd %5d ssthresh %5d pipe %5d sacked %5d recovery %-5t unacked %6d segs  rto %v\n",
			s.at.Seconds(), s.cwnd, s.ssthresh, s.outstanding, s.sackedOut, s.inRecovery, int(s.unacked)/mss, s.rto.Round(time.Millisecond))
	}
	if len(rows) <= maxRows {
		for _, s := range rows {
			row(s)
		}
		return
	}
	head, tail := maxRows/2, maxRows-maxRows/2
	for _, s := range rows[:head] {
		row(s)
	}
	fmt.Fprintf(out, "  ... %d more rows ...\n", len(rows)-head-tail)
	for _, s := range rows[len(rows)-tail:] {
		row(s)
	}
}

// timeoutWatcher records when a gVisor stack's TCP retransmission timer
// fires, by polling the stack-wide Timeouts counter.
type timeoutWatcher struct {
	stack *stack.Stack

	mu    sync.Mutex
	t0    time.Time
	last  uint64
	times []time.Duration
	stopc chan struct{}
	done  chan struct{}
}

func watchTimeouts(s *stack.Stack, t0 time.Time) *timeoutWatcher {
	w := &timeoutWatcher{stack: s, t0: t0, last: s.Stats().TCP.Timeouts.Value(), stopc: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-w.stopc:
				return
			case now := <-tick.C:
				w.record(now)
			}
		}
	}()
	return w
}

// record adds now as the time of each RTO since the last call.
func (w *timeoutWatcher) record(now time.Time) {
	v := w.stack.Stats().TCP.Timeouts.Value()
	w.mu.Lock()
	defer w.mu.Unlock()
	for ; w.last < v; w.last++ {
		w.times = append(w.times, now.Sub(w.t0))
	}
}

func (w *timeoutWatcher) stop() []time.Duration {
	close(w.stopc)
	<-w.done
	// Why: in the 2026-10-27 runs, the 1 ms poll missed 3 of 1111 RTOs, all
	// in end-of-transfer fetches, which end a few milliseconds after their
	// RTO. Reading the counter once more when the fetch ends counts them.
	w.record(time.Now())
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.times...)
}

// statCounters is the subset of gVisor's TCP stats reported per fetch.
type statCounters struct {
	retransmits, timeouts, fastRetransmit, sackRecovery, tlpRecovery, spuriousRTO uint64
}

func readStats(s *stack.Stack) statCounters {
	t := s.Stats().TCP
	return statCounters{
		retransmits:    t.Retransmits.Value(),
		timeouts:       t.Timeouts.Value(),
		fastRetransmit: t.FastRetransmit.Value(),
		sackRecovery:   t.SACKRecovery.Value(),
		tlpRecovery:    t.TLPRecovery.Value(),
		spuriousRTO:    t.SpuriousRTORecovery.Value(),
	}
}

func (a statCounters) sub(b statCounters) statCounters {
	return statCounters{
		retransmits:    a.retransmits - b.retransmits,
		timeouts:       a.timeouts - b.timeouts,
		fastRetransmit: a.fastRetransmit - b.fastRetransmit,
		sackRecovery:   a.sackRecovery - b.sackRecovery,
		tlpRecovery:    a.tlpRecovery - b.tlpRecovery,
		spuriousRTO:    a.spuriousRTO - b.spuriousRTO,
	}
}
