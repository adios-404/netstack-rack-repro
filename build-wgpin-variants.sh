#!/usr/bin/env bash
# Builds the reproduction against the gVisor that wireguard-go pins, which is
# also what Pangolin's newt 1.19.0 runs (and flyctl v0.4.123+, wireproxy and
# Sliver), and against that gVisor with the open upstream fixes ported to it,
# into ./bin. Run the binaries with -profile wireguard-go.
#   bin/repro-wgpin              gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
#                                (master 5c4b4f51c, 2025-05-02)
#   bin/repro-wgpin-pr16659-16660    + google/gvisor#16659 (TSecr fix) and
#                                #16660 (probe fix), at their heads 581d0bc94 and
#                                f739c6928
#   bin/repro-wgpin-pr16081      + google/gvisor#16081 (no new recovery during RTO
#                                recovery)
#   bin/repro-wgpin-pr15432-16081    + #15432 (SetPipe skips RACK-lost segments,
#                                with its resendSegment change) and #16081
#   bin/repro-wgpin-pr15432-16081-16659-16660    + all four
#
# The wgpin-* patches carry the PRs' non-test code, ported by hand to this
# pin: the code is the same, only the surrounding lines differ (the pin's
# reorderTimerExpired still reads the clock, and its RACK comments cite draft
# 08). Like the pin-* patches, #15432's includes the resendSegment change,
# which this pin lacks too.
#
# Usage: ./build-wgpin-variants.sh
set -euo pipefail
cd "$(dirname "$0")"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.3}"
pin=v0.0.0-20250503011706-39ed1f5ac29c
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

go mod download gvisor.dev/gvisor@"$pin"
src="$(go env GOMODCACHE)/gvisor.dev/gvisor@$pin"
mkdir -p bin

variant() {
  local name="$1"; shift
  local tree="$work/gvisor-$name"
  cp -r "$src" "$tree"
  # Why: files copied out of the module cache are read-only.
  chmod -R u+w "$tree"
  for p in "$@"; do
    patch -s -f -d "$tree" -p1 < "$p"
  done
  cp go.mod "$work/go.$name.mod"
  cp go.sum "$work/go.$name.sum"
  go mod edit -modfile="$work/go.$name.mod" -require=gvisor.dev/gvisor@"$pin"
  if [[ $# -gt 0 ]]; then
    echo "replace gvisor.dev/gvisor => $tree" >> "$work/go.$name.mod"
  fi
  # Why: -mod=mod lets the older pin settle the scratch go.mod's requirements.
  go build -mod=mod -modfile="$work/go.$name.mod" -o "bin/repro-$name" .
}
variant wgpin
variant wgpin-pr16659-16660 patches/wgpin-3-pr-16659-tsecr-fix-code.patch patches/wgpin-4-pr-16660-tlp-new-data-code.patch
variant wgpin-pr16081 patches/wgpin-2-pr-16081-code.patch
variant wgpin-pr15432-16081 patches/wgpin-1-pr-15432-code.patch patches/wgpin-2-pr-16081-code.patch
variant wgpin-pr15432-16081-16659-16660 patches/wgpin-1-pr-15432-code.patch patches/wgpin-2-pr-16081-code.patch \
  patches/wgpin-3-pr-16659-tsecr-fix-code.patch patches/wgpin-4-pr-16660-tlp-new-data-code.patch
