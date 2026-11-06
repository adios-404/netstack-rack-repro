#!/usr/bin/env bash
# Builds the stall-test variants of the netstack-rack-repro harness on
# Tailscale v1.104.x's gVisor pin (a6f909f08a72), with the method of the
# harness's build-variants.sh (copy the module tree, patch -p1, go build with
# a -modfile that replaces gvisor.dev/gvisor), but keeping the trees and
# scratch go.mod files under this folder and writing nothing into repos/.
#
#   bin/repro-pin                  the pin, as the harness's go.mod requires
#   bin/repro-pin-16659            + #16659's revised code (patches/pin-16659-revised.patch)
#   bin/repro-pin-16659orig        + the original TSecr fix (harness patches/pin-4-tsecr-fix-code.patch)
#   bin/repro-pin-16660            + #16660's revised code (patches/pin-16660-revised.patch)
#   bin/repro-pin-16659-16660      + both revised fixes, 16659 first
#   bin/repro-pin-16081            + #16081 (harness patches/pin-2-pr-16081-code.patch)
#   bin/repro-pin-15432-16081      + #15432 and #16081 (pin-1 then pin-2)
#
# Usage: ./build.sh            (rebuilds every variant from scratch)
set -euo pipefail

FT="$(cd "$(dirname "$0")" && pwd)"
HARNESS="$FT/../../../../repos/netstack-rack-repro"
HARNESS="$(cd "$HARNESS" && pwd)"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.3}"
pin=v0.0.0-20260915211658-a6f909f08a72
expected_head=94a018b

cd "$HARNESS"
head="$(git rev-parse --short=7 HEAD)"
if [[ "$head" != "$expected_head" ]]; then
  echo "harness HEAD is $head, expected $expected_head" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "harness working tree is not clean" >&2
  git status --short >&2
  exit 1
fi

go mod download gvisor.dev/gvisor@"$pin"
src="$(go env GOMODCACHE)/gvisor.dev/gvisor@$pin"
hp="$HARNESS/patches"
fp="$FT/patches"

mkdir -p "$FT/bin" "$FT/trees" "$FT/modfiles" "$FT/logs"

echo "== repro-pin"
go build -o "$FT/bin/repro-pin" .

variant() {
  local name="$1" from="$2"; shift 2
  local tree="$FT/trees/gvisor-$name"
  local log="$FT/logs/patch-$name.log"
  echo "== repro-$name"
  rm -rf "$tree"
  cp -r "$from" "$tree"
  # Why: files copied out of the module cache are read-only.
  chmod -R u+w "$tree"
  : > "$log"
  for p in "$@"; do
    echo "## $p" >> "$log"
    # Why: -V none, because this patch(1) leaves a .orig backup whenever a
    # hunk lands at an offset, as the second fix does after the first.
    patch -V none -d "$tree" -p1 --verbose < "$p" >> "$log" 2>&1
    # Why: patch applies hunks with fuzz silently; every hunk here must
    # match its context exactly (an offset is fine).
    if grep -qi "fuzz" "$log"; then
      echo "fuzz while applying $p; see $log" >&2
      exit 1
    fi
  done
  if find "$tree" -name '*.rej' -o -name '*.orig' | grep -q .; then
    echo "patch left .rej or .orig files in $tree" >&2
    exit 1
  fi
  cp go.mod "$FT/modfiles/go.$name.mod"
  cp go.sum "$FT/modfiles/go.$name.sum"
  echo "replace gvisor.dev/gvisor => $tree" >> "$FT/modfiles/go.$name.mod"
  # Why: -mod=mod lets the go command update the scratch go.mod, as in
  # build-variants.sh; the harness's own go.mod and go.sum are not touched.
  go build -mod=mod -modfile="$FT/modfiles/go.$name.mod" -o "$FT/bin/repro-$name" .
}

variant pin-16659 "$src" "$fp/pin-16659-revised.patch"
variant pin-16659orig "$src" "$hp/pin-4-tsecr-fix-code.patch"
variant pin-16660 "$src" "$fp/pin-16660-revised.patch"
variant pin-16659-16660 "$src" "$fp/pin-16659-revised.patch" "$fp/pin-16660-revised.patch"
variant pin-16081 "$src" "$hp/pin-2-pr-16081-code.patch"
variant pin-15432-16081 "$src" "$hp/pin-1-pr-15432-code.patch" "$hp/pin-2-pr-16081-code.patch"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "warning: the build changed the harness working tree" >&2
  git status --short >&2
  exit 1
fi
ls -l "$FT/bin"
