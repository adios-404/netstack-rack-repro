//go:build netstack2

package main

import (
	"net/netip"

	"github.com/fosrl/newt/netstack2"
	"github.com/fosrl/newt/proxy"
	"golang.zx2c4.com/wireguard/tun"
)

// newTunnel creates the main tunnel's netstack as the proposed newt change
// does: newt's netstack2 without proxying, with gVisor's RACK off.
func newTunnel(addr netip.Addr, mtu int) (tun.Device, *proxy.ProxyManager, string, error) {
	tdev, tnet, err := netstack2.CreateNetTUNWithOptions([]netip.Addr{addr}, []netip.Addr{netip.MustParseAddr("9.9.9.9")}, mtu, netstack2.NetTunOptions{})
	if err != nil {
		return nil, nil, "", err
	}
	return tdev, proxy.NewProxyManager(tnet), "newt netstack2 (proposed change)", nil
}
