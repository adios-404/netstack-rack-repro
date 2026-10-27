#!/usr/bin/env bash
# Runs the reproduction with the Linux kernel as the TCP sender. The kernel
# sends over a TUN device that the reproduction's emulated path reads, so it
# sees the same delay, rate and scripted loss as the gVisor sender.
#
# Why: the TUN device and its address must not touch the host's network, so
# the whole run happens in a new, empty network namespace (needs root or
# unprivileged user namespaces; the second form maps the caller to root).
set -euo pipefail
bin="${REPRO_BIN:-$(dirname "$0")/bin/repro-pin}"
REPRO_HOST_NETNS="$(readlink /proc/self/ns/net)"
export REPRO_HOST_NETNS
if [[ $(id -u) -eq 0 ]]; then
  exec unshare --net -- "$bin" -sender=linux "$@"
fi
exec unshare --net --map-root-user -- "$bin" -sender=linux "$@"
