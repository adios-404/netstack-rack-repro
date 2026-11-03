// Command e2e-newt runs the data path of a Pangolin public resource through
// newt's own code: a wireguard-go tunnel over newt's main-tunnel netstack,
// with newt's TCP proxy (proxy.ProxyManager) in front of a local HTTP
// server. The other end is a Linux kernel WireGuard interface and Linux TCP,
// as on a Pangolin server (Gerbil and Traefik). run.sh builds the network
// namespaces and the path between them.
//
//	e2e-newt keys                print a private key and its public key
//	e2e-newt wg-server [flags]   configure a kernel WireGuard interface
//	e2e-newt site [flags]        run the newt side until killed
//
// build.sh compiles it inside a copy of a newt checkout, as cmd/e2e-newt, so
// that it can start newt's telemetry, which newt's proxy needs. Which
// netstack the site uses depends on that newt; see tunnel_*.go.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fosrl/newt/internal/telemetry"
	"github.com/fosrl/newt/logger"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if len(os.Args) < 2 {
		log.Fatal("usage: e2e-newt keys | wg-server [flags] | site [flags]")
	}
	switch os.Args[1] {
	case "keys":
		k, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(k.String(), k.PublicKey().String())
	case "wg-server":
		wgServer(os.Args[2:])
	case "site":
		site(os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

// wgServer configures an existing kernel WireGuard interface with one peer.
func wgServer(args []string) {
	fs := flag.NewFlagSet("wg-server", flag.ExitOnError)
	iface := fs.String("iface", "wg0", "kernel WireGuard interface")
	key := fs.String("key", "", "private key (base64)")
	port := fs.Int("port", 51820, "listen port")
	peer := fs.String("peer", "", "peer public key (base64)")
	allowed := fs.String("allowed", "", "peer allowed IP prefix")
	fs.Parse(args)
	priv := mustKey(*key)
	pub := mustKey(*peer)
	prefix := netip.MustParsePrefix(*allowed)
	c, err := wgctrl.New()
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	err = c.ConfigureDevice(*iface, wgtypes.Config{
		PrivateKey:   &priv,
		ListenPort:   port,
		ReplacePeers: true,
		Peers: []wgtypes.PeerConfig{{
			PublicKey:  pub,
			AllowedIPs: []net.IPNet{{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())}},
		}},
	})
	if err != nil {
		log.Fatal(err)
	}
}

func site(args []string) {
	fs := flag.NewFlagSet("site", flag.ExitOnError)
	key := fs.String("key", "", "private key (base64)")
	peer := fs.String("peer", "", "server public key (base64)")
	endpoint := fs.String("endpoint", "", "server WireGuard endpoint, host:port")
	tunnelIP := fs.String("tunnel-ip", "10.77.0.2", "newt's address in the tunnel")
	serverIP := fs.String("server-ip", "10.77.0.1", "the server's address in the tunnel")
	mtu := fs.Int("mtu", 1280, "tunnel MTU (newt's default)")
	port := fs.Int("port", 8080, "port the proxy listens on in the tunnel")
	targetPort := fs.Int("target-port", 8000, "port of the local HTTP server the proxy forwards to")
	keepFirst := fs.Int("burst-keep-first", 0, "scripted loss: deliver the first N data packets of each transfer (0: no scripted loss)")
	window := fs.Duration("burst-window", 150*time.Millisecond, "scripted loss: then drop every data packet for this long")
	// Why: longer than any wait for a retransmission timer, so a timeout in
	// the middle of a fetch does not start a new transfer and re-arm the
	// scripted loss; run.sh waits 3 s between fetches.
	idleGap := fs.Duration("idle-gap", 2*time.Second, "a data packet after this much idle time starts a transfer (and re-arms the scripted loss)")
	fs.Parse(args)
	logger.GetLogger().SetLevel(logger.WARN)
	// As newt's main does; ProxyManager.Start registers its metrics.
	if _, err := telemetry.Init(context.Background(), telemetry.Config{ServiceName: "e2e-newt"}); err != nil {
		log.Fatal(err)
	}

	go serveTarget(*targetPort)

	addr := netip.MustParseAddr(*tunnelIP)
	tdev, pm, which, err := newTunnel(addr, *mtu)
	if err != nil {
		log.Fatal(err)
	}
	counter := newPacketCounter(addr, uint16(*port), *keepFirst, *window, *idleGap)
	dev := device.NewDevice(&countingTUN{Device: tdev, c: counter}, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "wireguard: "))
	cfg := fmt.Sprintf("private_key=%s\npublic_key=%s\nallowed_ip=%s/32\nendpoint=%s\npersistent_keepalive_interval=5",
		hexKey(*key), hexKey(*peer), *serverIP, *endpoint)
	if err := dev.IpcSet(cfg); err != nil {
		log.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		log.Fatal(err)
	}
	if err := pm.AddTarget("tcp", *tunnelIP, *port, "127.0.0.1:"+strconv.Itoa(*targetPort)); err != nil {
		log.Fatal(err)
	}
	if err := pm.Start(); err != nil {
		log.Fatal(err)
	}
	log.Printf("site up: %s, gVisor %s, proxy on %s:%d, scripted loss: %s", which, gvisorVersion(), *tunnelIP, *port, counter.describe())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	counter.flush()
	pm.Stop()
	dev.Close()
}

// serveTarget serves /bytes/N, N zero bytes, like a file on a home server.
func serveTarget(port int) {
	chunk := make([]byte, 64<<10)
	http.HandleFunc("/bytes/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/bytes/"))
		if err != nil || n < 0 {
			http.Error(w, "bad size", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		for n > 0 {
			k := min(n, len(chunk))
			if _, err := w.Write(chunk[:k]); err != nil {
				return
			}
			n -= k
		}
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+strconv.Itoa(port), nil))
}

// gvisorVersion returns the gVisor module the binary was built with.
func gvisorVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, d := range bi.Deps {
		if d.Path == "gvisor.dev/gvisor" {
			if d.Replace != nil {
				return d.Version + " (patched)"
			}
			return d.Version
		}
	}
	return "unknown"
}

func mustKey(s string) wgtypes.Key {
	k, err := wgtypes.ParseKey(s)
	if err != nil {
		log.Fatalf("key %q: %v", s, err)
	}
	return k
}

// hexKey converts a base64 key to the hex form IpcSet takes.
func hexKey(s string) string {
	k := mustKey(s)
	return hex.EncodeToString(k[:])
}
