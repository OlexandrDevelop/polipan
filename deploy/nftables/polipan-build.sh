#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Build network isolation of the control-plane host (REQ-SEC-108, AC-SEC-06).
#
# buildkitd runs on the Docker network polipan-build (bridge pp-build0). Build steps share its
# network, so they must reach only the public internet. These rules drop every NEW connection
# that comes in on pp-build0, except from the server address (the build client), when it goes to:
#   - this host itself (loopback, the bridge gateway, every host address): input hook
#   - loopback, link-local, the control-plane network, the Docker networks, mesh_cidr,
#     RFC 1918 and other special ranges: forward hook
# Replies of connections the server opened to buildkitd are allowed (ct state established).
#
#   sh polipan-build.sh            load the rules (needs root and nft)
#   sh polipan-build.sh --print    only print them
# Settings (environment):
#   MESH_CIDR       mesh_cidr of the server (default 10.210.0.0/16)
#   BUILD_IFACE     bridge of the build network (default pp-build0)
#   BUILD_CLIENT    address of the server on the build network (default 172.31.250.3)
#   ALLOW_PRIVATE   1 = org setting allow_private_targets: RFC 1918 is not dropped
#                   (loopback, gateway, control-plane network, Docker networks and mesh still are)
set -eu

mesh=${MESH_CIDR:-10.210.0.0/16}
iface=${BUILD_IFACE:-pp-build0}
client=${BUILD_CLIENT:-172.31.250.3}

# Always dropped. 172.16.0.0/12 holds all Docker networks of the host (control plane, build, apps).
always="0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 224.0.0.0/4, 240.0.0.0/4, $mesh"
private="10.0.0.0/8, 192.168.0.0/16, 100.64.0.0/10"
if [ "${ALLOW_PRIVATE:-0}" = "1" ]; then
  deny="$always"
else
  deny="$always, $private"
fi

rules() {
  cat <<NFT
table inet polipan_build
delete table inet polipan_build
table inet polipan_build {
	set deny4 {
		type ipv4_addr
		flags interval
		auto-merge
		elements = { $deny }
	}

	chain forward {
		type filter hook forward priority -10; policy accept;
		iifname "$iface" ct state established,related accept
		iifname "$iface" ip saddr != $client ip daddr @deny4 counter drop
		iifname "$iface" meta nfproto ipv6 counter drop
	}

	chain input {
		type filter hook input priority -10; policy accept;
		iifname "$iface" ct state established,related accept
		iifname "$iface" ip saddr != $client counter drop
		iifname "$iface" meta nfproto ipv6 counter drop
	}
}
NFT
}

if [ "${1:-}" = "--print" ]; then
  rules
  exit 0
fi

command -v nft >/dev/null 2>&1 || { echo "nft is missing" >&2; exit 1; }
# Bridged traffic between two containers of pp-build0 must pass the forward hook too.
modprobe br_netfilter 2>/dev/null || true
sysctl -qw net.bridge.bridge-nf-call-iptables=1 2>/dev/null || true
rules | nft -f -
