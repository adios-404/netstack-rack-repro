package main

import (
	"context"
	"fmt"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const nicID = 1

// tailscaleTCPConfig describes the TCP settings wgengine/netstack applies.
// The defaults match Tailscale v1.104.2 on Linux (verified 2026-10-27 against
// wgengine/netstack/netstack.go and netstack_tcpbuf_default.go at tag v1.104.2):
//   - CUBIC congestion control, set explicitly.
//   - SACK and RACK left at gVisor's defaults, which are both on.
//   - Receive buffer {Min: 4 KiB, Default: 1 MiB, Max: 8 MiB}.
//   - Send buffer {Min: 4 KiB, Default: 1 MiB, Max: 6 MiB}.
//   - Host GSO with a 64 KiB maximum, segmented by tstun before WireGuard.
//   - Link MTU 1280 (tstun.DefaultTUNMTU) and RX checksum offload.
//   - No ClockResolution (it is only set on Windows).
type tailscaleTCPConfig struct {
	rack     bool
	cc       string
	hostGSO  bool
	clockRes time.Duration
}

// gsoLinkEndpoint is a channel endpoint that reports Tailscale's 64 KiB GSO
// limit instead of the channel package's 32 KiB.
type gsoLinkEndpoint struct {
	*channel.Endpoint
	gsoMaxSize uint32
}

func (e *gsoLinkEndpoint) GSOMaxSize() uint32 { return e.gsoMaxSize }

type gvisorHost struct {
	name  string
	stack *stack.Stack
	link  *gsoLinkEndpoint
	addr  tcpip.Address
}

// newTailscaleStack builds a stack configured like wgengine/netstack.
func newTailscaleStack(name string, addr tcpip.Address, mtu uint32, cfg tailscaleTCPConfig, probe tcp.TCPProbeFunc) (*gvisorHost, error) {
	opts := stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocolProbe(probe), udp.NewProtocol},
		ClockResolution:    cfg.clockRes,
	}
	s := stack.New(opts)
	ccOpt := tcpip.CongestionControlOption(cfg.cc)
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &ccOpt); err != nil {
		return nil, fmt.Errorf("set congestion control %q: %v", cfg.cc, err)
	}
	if !cfg.rack {
		// This is what Tailscale did before #21303 removed it.
		recovery := tcpip.TCPRecovery(0)
		if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &recovery); err != nil {
			return nil, fmt.Errorf("disable RACK: %v", err)
		}
	}
	rx := tcpip.TCPReceiveBufferSizeRangeOption{Min: tcp.MinBufferSize, Default: tcp.DefaultSendBufferSize, Max: 8 << 20}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &rx); err != nil {
		return nil, fmt.Errorf("set receive buffer range: %v", err)
	}
	tx := tcpip.TCPSendBufferSizeRangeOption{Min: tcp.MinBufferSize, Default: tcp.DefaultReceiveBufferSize, Max: 6 << 20}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &tx); err != nil {
		return nil, fmt.Errorf("set send buffer range: %v", err)
	}
	link := &gsoLinkEndpoint{Endpoint: channel.New(4096, mtu, ""), gsoMaxSize: 1<<16 - 1}
	link.LinkEPCapabilities = stack.CapabilityRXChecksumOffload
	if cfg.hostGSO {
		link.SupportedGSOKind = stack.HostGSOSupported
	}
	return attachNIC(name, s, link, addr)
}

// newPeerStack builds the other end of the connection with gVisor's defaults,
// standing in for the client's kernel TCP.
func newPeerStack(name string, addr tcpip.Address, mtu uint32) (*gvisorHost, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	// Why: gVisor's default 4 MiB receive buffer limit is smaller than the
	// 8 MiB Tailscale and current macOS/Linux kernels autotune up to; keep the
	// receive window out of the way so only the sender's recovery is measured.
	rx := tcpip.TCPReceiveBufferSizeRangeOption{Min: tcp.MinBufferSize, Default: tcp.DefaultReceiveBufferSize, Max: 8 << 20}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &rx); err != nil {
		return nil, fmt.Errorf("set receive buffer range: %v", err)
	}
	link := &gsoLinkEndpoint{Endpoint: channel.New(4096, mtu, ""), gsoMaxSize: 1 << 15}
	return attachNIC(name, s, link, addr)
}

func attachNIC(name string, s *stack.Stack, link *gsoLinkEndpoint, addr tcpip.Address) (*gvisorHost, error) {
	if err := s.CreateNIC(nicID, link); err != nil {
		return nil, fmt.Errorf("create NIC: %v", err)
	}
	pa := tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: addr.WithPrefix()}
	if err := s.AddProtocolAddress(nicID, pa, stack.AddressProperties{}); err != nil {
		return nil, fmt.Errorf("add address: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})
	return &gvisorHost{name: name, stack: s, link: link, addr: addr}, nil
}

// pumpOutbound moves packets the stack transmits onto the path, segmenting
// host-GSO packets first.
func (h *gvisorHost) pumpOutbound(ctx context.Context, send func([]byte)) {
	for {
		pkt := h.link.ReadContext(ctx)
		if pkt == nil {
			return
		}
		v := pkt.ToView()
		b := append([]byte(nil), v.AsSlice()...)
		v.Release()
		gso := pkt.GSOOptions
		pkt.DecRef()
		segs, err := gsoSplit(b, gso)
		if err != nil {
			panic(fmt.Sprintf("%s: %v", h.name, err))
		}
		for _, seg := range segs {
			send(seg)
		}
	}
}

// inject hands one IPv4 packet from the path to the stack.
func (h *gvisorHost) inject(b []byte) {
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(b)})
	h.link.InjectInbound(header.IPv4ProtocolNumber, pkt)
	pkt.DecRef()
}

func (h *gvisorHost) close() {
	h.link.Close()
	h.stack.Close()
	h.stack.Wait()
}
