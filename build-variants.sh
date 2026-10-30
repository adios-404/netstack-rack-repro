#!/usr/bin/env bash
# Builds the reproduction against the gVisor that Tailscale v1.104.2 pins, and
# against that gVisor and gVisor master with the open upstream fixes applied,
# into ./bin:
#   bin/repro-pin                gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72
#   bin/repro-pr15432            + google/gvisor#15432 (SetPipe skips RACK-lost segments)
#   bin/repro-pr16081            + google/gvisor#16081 (no new recovery during RTO recovery)
#   bin/repro-pr15432-16081      + both
#   bin/repro-pr16081-notsecr, bin/repro-pr15432-16081-notsecr
#                                + #16081, or both, + no TSecr test in
#                                rackControl.update (an experiment, not a
#                                proposed change)
#   bin/repro-pr16081-tsecr, bin/repro-pr15432-16081-tsecr
#                                + #16081, or both, + the RACK TSecr fix proposed
#                                here (patches/pin-4-tsecr-fix-code.patch)
#   bin/repro-pr15432-16081-tlpnew, bin/repro-pr15432-16081-tsecr-tlpnew
#                                + both, + the loss-probe new-data fix proposed here
#                                (patches/pin-5-tlp-new-data-code.patch), with or
#                                without the TSecr fix
#   bin/repro-master             gVisor master 4777ee78 (go branch a68437cf), 2026-10-27
#   bin/repro-master-tsecr       + the RACK TSecr fix alone
#   bin/repro-master-pr15432-16081  + both, rebased onto master
#   bin/repro-master-pr15432-16081-tsecr  + both + the RACK TSecr fix
#   bin/repro-master-pr15432-16081-tlpnew  + both + the loss-probe new-data fix
#   bin/repro-master-pr15432-16081-tsecr-tlpnew  + both + both proposed fixes
#   bin/repro-master-tsecr2, bin/repro-master-pr15432-16081-tsecr2,
#   bin/repro-master-pr15432-16081-tlpnew2, bin/repro-master-pr15432-16081-tsecr2-tlpnew2
#                                the same with the revised versions of the two
#                                fixes, after review (*-revised-code.patch)
#   bin/repro-master-pr16495     + google/gvisor#16495 (no new recovery after an RTO
#                                that interrupts SACK recovery)
#   bin/repro-master-pr15432-16081-16495-16419
#                                + those three + google/gvisor#16419 (keep ssthresh
#                                on repeated RTOs), stacked in that order
#
# The upstream PR patches (pin-1, pin-2 and master-pr-*) carry only the PRs'
# non-test code; their code lines are the PRs' own, as of 2026-10-27. The
# master patches leave out #15432's resendSegment change, which master already
# has (7c8d7811a). In the four-PR stack, #16495 and #16081 change the same
# line, and the stack keeps both guards: s.state != tcpip.RTORecovery &&
# s.enterRACKRecovery(). pin-3 is an experiment. pin-4 and master-tsecr-fix,
# and pin-5 and master-tlp-new-data, are the two fixes proposed here, not
# upstream code; master-tsecr-fix-revised and master-tlp-new-data-revised are
# their versions after review on google/gvisor#16659 and #16660 (2026-10-30).
#
# Usage: ./build-variants.sh [path to a gVisor "go" branch checkout at a6f909f08a72]
# Without an argument, the pinned module is copied out of the Go module cache.
set -euo pipefail
cd "$(dirname "$0")"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.3}"
pin=v0.0.0-20260915211658-a6f909f08a72
master=v0.0.0-20261027095827-a68437cfc713
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

src="${1:-}"
if [[ -z "$src" ]]; then
  go mod download gvisor.dev/gvisor@"$pin"
  src="$(go env GOMODCACHE)/gvisor.dev/gvisor@$pin"
fi

mkdir -p bin
go build -o bin/repro-pin .

variant() {
  local name="$1" from="$2"; shift 2
  local tree="$work/gvisor-$name"
  cp -r "$from" "$tree"
  # Why: files copied out of the module cache are read-only.
  chmod -R u+w "$tree"
  for p in "$@"; do
    patch -s -d "$tree" -p1 < "$p"
  done
  cp go.mod "$work/go.$name.mod"
  cp go.sum "$work/go.$name.sum"
  echo "replace gvisor.dev/gvisor => $tree" >> "$work/go.$name.mod"
  # Why: -mod=mod lets master's newer dependencies update the scratch go.mod.
  go build -mod=mod -modfile="$work/go.$name.mod" -o "bin/repro-$name" .
}
variant pr15432 "$src" patches/pin-1-pr-15432-code.patch
variant pr16081 "$src" patches/pin-2-pr-16081-code.patch
variant pr15432-16081 "$src" patches/pin-1-pr-15432-code.patch patches/pin-2-pr-16081-code.patch
variant pr16081-notsecr "$src" patches/pin-2-pr-16081-code.patch patches/pin-3-experiment-no-rack-tsecr-test.patch
variant pr15432-16081-notsecr "$src" patches/pin-1-pr-15432-code.patch patches/pin-2-pr-16081-code.patch \
  patches/pin-3-experiment-no-rack-tsecr-test.patch
variant pr16081-tsecr "$src" patches/pin-2-pr-16081-code.patch patches/pin-4-tsecr-fix-code.patch
variant pr15432-16081-tsecr "$src" patches/pin-1-pr-15432-code.patch patches/pin-2-pr-16081-code.patch \
  patches/pin-4-tsecr-fix-code.patch
variant pr15432-16081-tlpnew "$src" patches/pin-1-pr-15432-code.patch patches/pin-2-pr-16081-code.patch \
  patches/pin-5-tlp-new-data-code.patch
variant pr15432-16081-tsecr-tlpnew "$src" patches/pin-1-pr-15432-code.patch patches/pin-2-pr-16081-code.patch \
  patches/pin-4-tsecr-fix-code.patch patches/pin-5-tlp-new-data-code.patch

go mod download gvisor.dev/gvisor@"$master"
master_src="$(go env GOMODCACHE)/gvisor.dev/gvisor@$master"
variant master "$master_src"
variant master-tsecr "$master_src" patches/master-tsecr-fix-code.patch
variant master-pr15432-16081 "$master_src" patches/master-pr-15432-16081-code.patch
variant master-pr15432-16081-tsecr "$master_src" patches/master-pr-15432-16081-code.patch \
  patches/master-tsecr-fix-code.patch
variant master-pr15432-16081-tlpnew "$master_src" patches/master-pr-15432-16081-code.patch \
  patches/master-tlp-new-data-code.patch
variant master-pr15432-16081-tsecr-tlpnew "$master_src" patches/master-pr-15432-16081-code.patch \
  patches/master-tsecr-fix-code.patch patches/master-tlp-new-data-code.patch
variant master-tsecr2 "$master_src" patches/master-tsecr-fix-revised-code.patch
variant master-pr15432-16081-tsecr2 "$master_src" patches/master-pr-15432-16081-code.patch \
  patches/master-tsecr-fix-revised-code.patch
variant master-pr15432-16081-tlpnew2 "$master_src" patches/master-pr-15432-16081-code.patch \
  patches/master-tlp-new-data-revised-code.patch
variant master-pr15432-16081-tsecr2-tlpnew2 "$master_src" patches/master-pr-15432-16081-code.patch \
  patches/master-tsecr-fix-revised-code.patch patches/master-tlp-new-data-revised-code.patch
variant master-pr16495 "$master_src" patches/master-pr-16495-code.patch
variant master-pr15432-16081-16495-16419 "$master_src" patches/master-pr-15432-16081-16495-16419-code.patch
ls -l bin
