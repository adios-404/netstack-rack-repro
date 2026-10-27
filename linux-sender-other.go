//go:build !linux

package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"time"
)

// Why: the kernel-sender comparison drives a TUN device in a private network
// namespace, which only Linux has. The gVisor senders build and run anywhere.
var errLinuxOnly = errors.New("-sender=linux needs Linux")

func requireOwnNetns() error                                { return errLinuxOnly }
func openTun(string, netip.Prefix, int) (*os.File, error)   { return nil, errLinuxOnly }
func linuxTCPSysctls() string                               { return "" }
func setCongestionControl(net.Conn, string) error           { return errLinuxOnly }
func congestionControlOf(net.Conn) string                   { return "" }
func timestampsOf(net.Conn) string                          { return "" }
func watchKernelRTOs(net.Conn, time.Time) *kernelRTOWatcher { return &kernelRTOWatcher{} }

func listenWithCongestionControl(context.Context, string, string) (net.Listener, error) {
	return nil, errLinuxOnly
}

type kernelRTOWatcher struct{}

func (*kernelRTOWatcher) stop() []time.Duration { return nil }
