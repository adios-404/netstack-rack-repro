#!/usr/bin/env bash
# Builds the reproduction against the gVisor that Tailscale v1.104.2 pins and
# against that gVisor with the fixes applied, into ./bin:
#   bin/repro-pin      gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72
#   bin/repro-pr15432  + google/gvisor#15432 (SetPipe, DoRecovery, clone)
#   bin/repro-guard    + no new RACK recovery during RTO recovery (alone)
#   bin/repro-fix      + both
#   bin/repro-fix-notsecr  + both + no TSecr test in rackControl.update (an
#                          experiment, not a proposed change)
#   bin/repro-master       gVisor master 4777ee78 (go branch a68437cf), 2026-10-27
#   bin/repro-master-fix   + both fixes, rebased onto master
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
variant pr15432 "$src" patches/1-gvisor-pr-15432-core.patch
variant guard "$src" patches/2-rack-no-recovery-in-rto-recovery.patch
variant fix "$src" patches/1-gvisor-pr-15432-core.patch patches/2-rack-no-recovery-in-rto-recovery.patch
variant fix-notsecr "$src" patches/1-gvisor-pr-15432-core.patch patches/2-rack-no-recovery-in-rto-recovery.patch \
  patches/3-experiment-no-rack-tsecr-test.patch

go mod download gvisor.dev/gvisor@"$master"
master_src="$(go env GOMODCACHE)/gvisor.dev/gvisor@$master"
variant master "$master_src"
variant master-fix "$master_src" patches/master-1-and-2-code.patch
ls -l bin
