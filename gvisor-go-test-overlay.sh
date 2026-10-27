#!/usr/bin/env bash
# Builds a tree in which gVisor's TCP tests run under plain `go test`, without
# Bazel: the "go" branch export of a master commit (which carries the generated
# sources) plus that commit's TCP test files and test-only packages.
#
# Usage: gvisor-go-test-overlay.sh <master checkout> <go-branch checkout of the
#        same master commit> <output dir>
# Then:  cd <output dir> && go test ./pkg/tcpip/transport/tcp/...
set -euo pipefail
master="$(cd "$1" && pwd)"; gobranch="$(cd "$2" && pwd)"; out="$3"
rm -rf "$out"
mkdir -p "$out"
(cd "$gobranch" && tar --exclude=.git -cf - .) | (cd "$out" && tar -xf -)
tcp=pkg/tcpip/transport/tcp

# Package-internal tests and the test helpers keep their master paths.
for f in "$master/$tcp"/*_test.go; do cp "$f" "$out/$tcp/"; done
mkdir -p "$out/$tcp/test/e2e" "$out/$tcp/testing/context"
cp "$master/$tcp/test/e2e/e2e.go" "$out/$tcp/test/e2e/"
cp "$master/$tcp/testing/context/"*.go "$out/$tcp/testing/context/"

# Why: Bazel builds each go_test target in test/e2e as its own binary, so the
# files in that directory declare several different packages, which `go test`
# rejects. Give every target a directory of its own.
python3 - "$master/$tcp/test/e2e" "$out/$tcp/test/e2e" <<'PY'
import os, re, shutil, sys
src, dst = sys.argv[1:]
build = open(os.path.join(src, "BUILD")).read()
for m in re.finditer(r'go_test\(\s*name = "([^"]+)",.*?srcs = \[(.*?)\]', build, re.S):
    d = os.path.join(dst, m.group(1))
    os.makedirs(d, exist_ok=True)
    for f in re.findall(r'"([^"]+)"', m.group(2)):
        shutil.copy(os.path.join(src, f), d)
PY

# Test-only packages that the go branch leaves out.
for pkg in pkg/tcpip/checker pkg/tcpip/testutil pkg/test/testutil; do
  mkdir -p "$out/$pkg"
  find "$master/$pkg" -maxdepth 1 -name '*.go' ! -name '*_test.go' -exec cp {} "$out/$pkg/" \;
done
echo "test tree ready: $out"
