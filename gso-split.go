package main

import (
	"encoding/binary"
	"fmt"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// gsoSplit segments a TCP/IPv4 packet that gVisor emitted with host GSO into
// MSS-sized packets, the way Tailscale's tstun does with tun.GSOSplit before
// handing packets to WireGuard. Packets without GSO are returned unchanged.
func gsoSplit(pkt []byte, gso stack.GSO) ([][]byte, error) {
	if gso.Type == stack.GSONone {
		return [][]byte{pkt}, nil
	}
	if gso.Type != stack.GSOTCPv4 {
		return nil, fmt.Errorf("unsupported GSO type %v", gso.Type)
	}
	ip := header.IPv4(pkt)
	ipHdrLen := int(ip.HeaderLength())
	tcp := header.TCP(pkt[ipHdrLen:])
	tcpHdrLen := int(tcp.DataOffset())
	hdrLen := ipHdrLen + tcpHdrLen
	payload := pkt[hdrLen:]
	mss := int(gso.MSS)
	if mss <= 0 {
		return nil, fmt.Errorf("GSO packet with MSS %d", mss)
	}

	seq := tcp.SequenceNumber()
	flags := tcp.Flags()
	id := ip.ID()
	src, dst := ip.SourceAddress(), ip.DestinationAddress()
	var out [][]byte
	for off := 0; off < len(payload) || off == 0; off += mss {
		end := min(off+mss, len(payload))
		seg := make([]byte, hdrLen+end-off)
		copy(seg, pkt[:hdrLen])
		copy(seg[hdrLen:], payload[off:end])

		sip := header.IPv4(seg)
		sip.SetTotalLength(uint16(len(seg)))
		sip.SetID(id + uint16(len(out)))
		sip.SetChecksum(0)
		sip.SetChecksum(^sip.CalculateChecksum())

		stcp := header.TCP(seg[ipHdrLen:])
		binary.BigEndian.PutUint32(stcp[header.TCPSeqNumOffset:], seq+uint32(off))
		f := flags
		// Why: like the kernel's and wireguard-go's segmentation, only the
		// last segment keeps FIN and PSH, and only the first keeps CWR.
		if end < len(payload) {
			f &^= header.TCPFlagFin | header.TCPFlagPsh
		}
		if off > 0 {
			f &^= header.TCPFlagCwr
		}
		stcp.SetFlags(uint8(f))
		// Why: gVisor leaves only the pseudo-header sum in a GSO packet's TCP
		// checksum field (CHECKSUM_PARTIAL), so every segment needs a full
		// checksum computed from scratch.
		stcp.SetChecksum(0)
		tcpLen := uint16(len(seg) - ipHdrLen)
		xsum := header.PseudoHeaderChecksum(header.TCPProtocolNumber, src, dst, tcpLen)
		xsum = checksum.Checksum(seg[ipHdrLen:], xsum)
		stcp.SetChecksum(^xsum)
		out = append(out, seg)
		if len(payload) == 0 {
			break
		}
	}
	return out, nil
}

// packetInfo is the part of a TCP/IPv4 packet the path emulator and the wire
// trace look at.
type packetInfo struct {
	isTCP      bool
	src, dst   tcpip.Address
	srcPort    uint16
	dstPort    uint16
	seq        uint32
	ack        uint32
	flags      header.TCPFlags
	payloadLen int
	hasSACK    bool
	sackBlocks []header.SACKBlock
	wnd        uint16
	size       int
}

func parsePacket(b []byte) packetInfo {
	info := packetInfo{size: len(b)}
	if len(b) < header.IPv4MinimumSize || header.IPVersion(b) != header.IPv4Version {
		return info
	}
	ip := header.IPv4(b)
	if ip.TransportProtocol() != header.TCPProtocolNumber {
		return info
	}
	ipHdrLen := int(ip.HeaderLength())
	if len(b) < ipHdrLen+header.TCPMinimumSize {
		return info
	}
	tcp := header.TCP(b[ipHdrLen:])
	info.isTCP = true
	info.src, info.dst = ip.SourceAddress(), ip.DestinationAddress()
	info.srcPort, info.dstPort = tcp.SourcePort(), tcp.DestinationPort()
	info.seq = tcp.SequenceNumber()
	info.ack = tcp.AckNumber()
	info.flags = tcp.Flags()
	info.wnd = tcp.WindowSize()
	info.payloadLen = int(ip.TotalLength()) - ipHdrLen - int(tcp.DataOffset())
	opts := header.ParseTCPOptions(tcp.Options())
	if len(opts.SACKBlocks) > 0 {
		info.hasSACK = true
		info.sackBlocks = opts.SACKBlocks
	}
	return info
}
