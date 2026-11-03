package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// countingTUN passes the packets newt's netstack sends into the tunnel
// through a packetCounter, which counts them and can drop some.
type countingTUN struct {
	tun.Device
	c *packetCounter
}

func (t *countingTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for {
		n, err := t.Device.Read(bufs, sizes, offset)
		if n == 0 || err != nil {
			return n, err
		}
		kept := 0
		for i := 0; i < n; i++ {
			if t.c.drop(bufs[i][offset : offset+sizes[i]]) {
				continue
			}
			if kept != i {
				copy(bufs[kept][offset:], bufs[i][offset:offset+sizes[i]])
				sizes[kept] = sizes[i]
			}
			kept++
		}
		if kept > 0 {
			return kept, nil
		}
	}
}

// packetCounter counts the TCP data packets the proxy sends, per transfer:
// a transfer starts with the first data packet after idleGap without one.
// With keepFirst > 0 it also drops, in each transfer, every data packet in
// the window after the first keepFirst, as the harness's burst loss does.
type packetCounter struct {
	src       netip.Addr
	srcPort   uint16
	keepFirst int
	window    time.Duration
	idleGap   time.Duration

	mu        sync.Mutex
	transfer  int
	start     time.Time
	last      time.Time
	packets   int
	retx      int
	dropped   int
	dropUntil time.Time
	highest   map[uint16]uint32 // per client port, highest sequence number sent
}

func newPacketCounter(src netip.Addr, srcPort uint16, keepFirst int, window, idleGap time.Duration) *packetCounter {
	return &packetCounter{src: src, srcPort: srcPort, keepFirst: keepFirst, window: window, idleGap: idleGap, highest: map[uint16]uint32{}}
}

func (c *packetCounter) describe() string {
	if c.keepFirst == 0 {
		return "none"
	}
	return fmt.Sprintf("keep the first %d data packets of each transfer, then drop all for %v", c.keepFirst, c.window)
}

func (c *packetCounter) drop(b []byte) bool {
	dstPort, end, ok := c.dataPacket(b)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.last.IsZero() || now.Sub(c.last) >= c.idleGap {
		c.report()
		c.transfer++
		c.start, c.packets, c.retx, c.dropped, c.dropUntil = now, 0, 0, 0, time.Time{}
	}
	c.last = now
	c.packets++
	if h, seen := c.highest[dstPort]; seen && int32(end-h) <= 0 {
		c.retx++
	} else {
		c.highest[dstPort] = end
	}
	if c.keepFirst == 0 || c.packets <= c.keepFirst {
		return false
	}
	if c.dropUntil.IsZero() {
		c.dropUntil = now.Add(c.window)
	}
	if now.Before(c.dropUntil) {
		c.dropped++
		return true
	}
	return false
}

// dataPacket reports whether b is an IPv4 TCP packet with data from the
// proxy, with its destination port and the sequence number after its data.
func (c *packetCounter) dataPacket(b []byte) (dstPort uint16, end uint32, ok bool) {
	if len(b) < 20 || b[0]>>4 != 4 || b[9] != 6 {
		return 0, 0, false
	}
	ihl := int(b[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(b[2:4]))
	if total > len(b) || total < ihl+20 || netip.AddrFrom4([4]byte(b[12:16])) != c.src {
		return 0, 0, false
	}
	t := b[ihl:total]
	if binary.BigEndian.Uint16(t[0:2]) != c.srcPort {
		return 0, 0, false
	}
	payload := len(t) - int(t[12]>>4)*4
	if payload <= 0 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(t[2:4]), binary.BigEndian.Uint32(t[4:8]) + uint32(payload), true
}

// report logs the transfer that just ended. c.mu must be held.
func (c *packetCounter) report() {
	if c.transfer == 0 {
		return
	}
	log.Printf("transfer %d: %.3f s from first to last data packet, %d data packets, %d retransmitted, %d dropped",
		c.transfer, c.last.Sub(c.start).Seconds(), c.packets, c.retx, c.dropped)
}

func (c *packetCounter) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report()
}
