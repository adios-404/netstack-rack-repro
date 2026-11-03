#!/usr/bin/env bash
# Builds two Linux site binaries from a newt clone. Each is compiled inside an
# exported copy of that newt revision, as cmd/e2e-newt, because it starts
# newt's internal telemetry, as newt's main does. -mod=mod lets the copy's
# go.sum gain wgctrl's Linux dependencies, which newt itself never imports.
#   bin/e2e-newt-before   <before rev>: the main tunnel on wireguard-go's
#                         tun/netstack, gVisor's RACK on (newt 1.19.0 and main)
#   bin/e2e-newt-after    <after rev>: the proposed change, the main tunnel on
#                         netstack2 with RACK off (built with -tags netstack2)
#   bin/e2e-newt-gvfix    <before rev> with RACK on, but its gVisor patched with
#                         google/gvisor#15432, #16081, #16659 and #16660
#                         (../patches/wgpin-*), as newt would run once gVisor has
#                         them
#   bin/e2e-newt-wgpatch  <before rev> with its wireguard-go patched with
#                         ../patches/wireguard-go-disable-rack.patch (RACK off
#                         in wireguard-go's tun/netstack, which the main tunnel
#                         runs on)
# Usage: ./build.sh <newt clone> <before rev> <after rev> [GOARCH, default arm64]
set -euo pipefail
cd "$(dirname "$0")"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.3}"
repo="$(cd "${1:?newt clone}" && pwd)"
before="${2:?before revision}"
after="${3:?after revision}"
arch="${4:-arm64}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p bin
build() {
  local name="$1" rev="$2" tags="$3" replace="${4:-}"
  mkdir -p "$work/$name/cmd/e2e-newt"
  git -C "$repo" archive "$rev" | tar -x -C "$work/$name"
  cp ./*.go "$work/$name/cmd/e2e-newt/"
  if [[ -n "$replace" ]]; then
    (cd "$work/$name" && go mod edit -replace "$replace")
  fi
  (cd "$work/$name" && GOOS=linux GOARCH="$arch" go build -mod=mod -tags "$tags" -o "$OLDPWD/bin/e2e-newt-$name" ./cmd/e2e-newt)
  echo "$name: newt $(git -C "$repo" rev-parse --short "$rev^{commit}")"
}
build before "$before" ""
build after "$after" netstack2
pin=v0.0.0-20250503011706-39ed1f5ac29c
go mod download gvisor.dev/gvisor@"$pin"
cp -r "$(go env GOMODCACHE)/gvisor.dev/gvisor@$pin" "$work/gvisor-fixed"
chmod -R u+w "$work/gvisor-fixed"
for p in ../patches/wgpin-1-* ../patches/wgpin-2-* ../patches/wgpin-3-* ../patches/wgpin-4-*; do
  patch -s -f -d "$work/gvisor-fixed" -p1 < "$p"
done
build gvfix "$before" "" "gvisor.dev/gvisor=$work/gvisor-fixed"
# Why: the version newt pins, so that only the patch differs.
wg=v0.0.0-20250521234502-f333402bd9cb
go mod download golang.zx2c4.com/wireguard@"$wg"
cp -r "$(go env GOMODCACHE)/golang.zx2c4.com/wireguard@$wg" "$work/wireguard-patched"
chmod -R u+w "$work/wireguard-patched"
patch -s -f -d "$work/wireguard-patched" -p1 < ../patches/wireguard-go-disable-rack.patch
build wgpatch "$before" "" "golang.zx2c4.com/wireguard=$work/wireguard-patched"
