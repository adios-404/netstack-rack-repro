//go:build !netstack2

package main

import (
	"net/netip"

	"github.com/fosrl/newt/proxy"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// newTunnel creates the main tunnel's netstack as newt 1.19.0 does
// (newt/connect.go): wireguard-go's tun/netstack, with gVisor's RACK on.
func newTunnel(addr netip.Addr, mtu int) (tun.Device, *proxy.ProxyManager, string, error) {
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, []netip.Addr{netip.MustParseAddr("9.9.9.9")}, mtu)
	if err != nil {
		return nil, nil, "", err
	}
	return tdev, proxy.NewProxyManager(tnet), "wireguard-go tun/netstack (newt 1.19.0)", nil
}
