package main

import (
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// stripSYNTimestamps replaces the timestamps option of a TCP SYN without ACK
// with NOPs and fixes the TCP checksum. Other packets are left alone.
//
// Why: gVisor always offers timestamps, so this is how the client stands in
// for one that does not, such as Windows with its default settings. Without
// timestamps, RACK's TSecr test (rack.go, rackControl.update) does not apply,
// so a delivered loss probe can start RACK recovery.
func stripSYNTimestamps(b []byte) {
	ip := header.IPv4(b)
	if len(b) < header.IPv4MinimumSize || ip.TransportProtocol() != header.TCPProtocolNumber {
		return
	}
	ipHdrLen := int(ip.HeaderLength())
	if len(b) < ipHdrLen+header.TCPMinimumSize {
		return
	}
	tcp := header.TCP(b[ipHdrLen:])
	if tcp.Flags()&(header.TCPFlagSyn|header.TCPFlagAck) != header.TCPFlagSyn {
		return
	}
	opts := tcp.Options()
	changed := false
	for i := 0; i < len(opts); {
		switch kind := opts[i]; kind {
		case header.TCPOptionEOL:
			i = len(opts)
		case header.TCPOptionNOP:
			i++
		default:
			if i+1 >= len(opts) || opts[i+1] < 2 || i+int(opts[i+1]) > len(opts) {
				return
			}
			n := int(opts[i+1])
			if kind == header.TCPOptionTS {
				for j := i; j < i+n; j++ {
					opts[j] = header.TCPOptionNOP
				}
				changed = true
			}
			i += n
		}
	}
	if !changed {
		return
	}
	tcp.SetChecksum(0)
	tcpLen := uint16(len(b) - ipHdrLen)
	xsum := header.PseudoHeaderChecksum(header.TCPProtocolNumber, ip.SourceAddress(), ip.DestinationAddress(), tcpLen)
	xsum = checksum.Checksum(b[ipHdrLen:], xsum)
	tcp.SetChecksum(^xsum)
}
