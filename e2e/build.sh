#!/usr/bin/env bash
# Builds what run-e2e.sh runs, from source, into ./out and ./bin:
#
#   out/shipped/        tailscaled and tailscale v1.104.2 + the
#                       TS_NETSTACK_TCP_RACK knob, with the gVisor that
#                       v1.104.2 pins
#   out/pr15432-16081/  the same + google/gvisor#15432 and #16081
#                       (../patches/pin-1-*, pin-2-*)
#   out/pr15432-16081-tsecr-tlpnew/
#                       the same + the TSecr fix and the probe fix
#                       (../patches/pin-4-*, pin-5-*)
#   bin/derper, bin/headscale (v0.29.6), bin/tsbridge
#
# Needs git, patch and Go (it fetches go1.27.3 itself).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
patches="$here/../patches"
# Why: the script deletes this directory, so it never comes from the environment.
work="$here/build"
export GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0
gvisor_pin=v0.0.0-20260915211658-a6f909f08a72

# Why: an interrupted earlier run can leave half-patched trees, and building
# from them would silently drop a fix, so every run starts clean.
rm -rf "$work" "$here/out" "$here/bin"
mkdir -p "$work" "$here/out" "$here/bin"
git clone --quiet --branch v1.104.2 --depth 1 https://github.com/tailscale/tailscale "$work/tailscale"
git -C "$work/tailscale" apply "$here/tailscale-v1.104.2-netstack-tcp-rack-knob.patch"

# Why: the gVisor source comes out of the module cache, so it is byte for byte
# the module v1.104.2's go.sum pins.
(cd "$work/tailscale" && go mod download gvisor.dev/gvisor@"$gvisor_pin")
pin="$(cd "$work/tailscale" && go env GOMODCACHE)/gvisor.dev/gvisor@$gvisor_pin"
gvisor() { # gvisor NAME PATCH... : a copy of the pinned gVisor with the patches applied
  cp -r "$pin" "$work/gvisor-$1"
  # Why: files copied out of the module cache are read-only.
  chmod -R u+w "$work/gvisor-$1"
  local name=$1; shift
  for p in "$@"; do patch -s -d "$work/gvisor-$name" -p1 <"$patches/$p"; done
}
gvisor pr15432-16081 pin-1-pr-15432-code.patch pin-2-pr-16081-code.patch
gvisor pr15432-16081-tsecr-tlpnew pin-1-pr-15432-code.patch pin-2-pr-16081-code.patch pin-4-tsecr-fix-code.patch pin-5-tlp-new-data-code.patch

build() { # build FLAVOR [GVISOR-DIR]
  local flavor=$1 modargs=()
  mkdir -p "$here/out/$flavor"
  if [[ $# -gt 1 ]]; then
    cp "$work/tailscale/go.mod" "$work/go.$flavor.mod"
    cp "$work/tailscale/go.sum" "$work/go.$flavor.sum"
    go mod edit -replace=gvisor.dev/gvisor="$2" "$work/go.$flavor.mod"
    modargs=(-modfile="$work/go.$flavor.mod")
  fi
  (cd "$work/tailscale" && go build "${modargs[@]}" -o "$here/out/$flavor/" ./cmd/tailscaled ./cmd/tailscale)
}
build shipped
build pr15432-16081 "$work/gvisor-pr15432-16081"
build pr15432-16081-tsecr-tlpnew "$work/gvisor-pr15432-16081-tsecr-tlpnew"
(cd "$work/tailscale" && go build -o "$here/bin/" ./cmd/derper)
GOBIN="$here/bin" go install github.com/juanfont/headscale/cmd/headscale@v0.29.6
(cd "$here/tsbridge" && go build -o "$here/bin/" .)

# Check that each flavor carries the knob and its gVisor.
for f in shipped pr15432-16081 pr15432-16081-tsecr-tlpnew; do
  grep -q TS_NETSTACK_TCP_RACK "$here/out/$f/tailscaled" || { echo "knob missing from out/$f" >&2; exit 1; }
done
go version -m "$here/out/shipped/tailscaled" | grep -q "gvisor.dev/gvisor.*$gvisor_pin.*h1:EytKYr5W" ||
  { echo "out/shipped does not use the pinned gVisor" >&2; exit 1; }
for f in pr15432-16081 pr15432-16081-tsecr-tlpnew; do
  # Why: go version -m separates "=>" from the replacement path with a tab.
  go version -m "$here/out/$f/tailscaled" | awk -F'\t' -v p="$work/gvisor-$f" '$2 == "=>" && $3 == p { ok = 1 } END { exit !ok }' ||
    { echo "out/$f does not use its patched gVisor" >&2; exit 1; }
done
echo "built into $here/out and $here/bin"
