#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Installs the Polipan agent on a new server and joins it to the panel.
# The panel serves this file at /install-agent.sh. The "Add node" dialog and `polipan node add` print the command:
#
#   curl -fsSL https://panel.example.com/install-agent.sh | sudo sh -s -- --server panel.example.com:9443 --token pj_... --ca-fingerprint ...
#
# Options:
#   --server HOST:PORT     the agent gateway of the panel (required)
#   --token TOKEN          the one-use join token (required)
#   --ca-fingerprint HEX   sha256 of the CA certificate of the panel (required): the join checks the server with it
#   --agent-image REF      the agent image the binary is taken from (default: ghcr.io/polipan/polipan-agent:latest)
#   --public-ip IP         the address other servers use for WireGuard (default: the address of the default route)
#   --wireguard-port PORT  the WireGuard UDP port (default: 51820)
#
# The script installs Docker (if missing), WireGuard tools, nftables, iptables and iproute2, puts the agent in
# /usr/local/bin and starts it as the systemd service polipan-agent. It is safe to run again.
# The source of this file is server/internal/api/install-agent.sh in the repository.
set -eu

server=""
token=""
fingerprint=""
agent_image="ghcr.io/polipan/polipan-agent:latest"
public_ip=""
wg_port="51820"

say() { printf '==> %s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --server) server=$2; shift 2 ;;
    --token) token=$2; shift 2 ;;
    --ca-fingerprint) fingerprint=$2; shift 2 ;;
    --agent-image) agent_image=$2; shift 2 ;;
    --public-ip) public_ip=$2; shift 2 ;;
    --wireguard-port) wg_port=$2; shift 2 ;;
    -h|--help) sed -n '3,17p' "$0"; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[ -n "$server" ] || fail "--server is required"
[ -n "$token" ] || fail "--token is required"
[ -n "$fingerprint" ] || fail "--ca-fingerprint is required"
[ "$(id -u)" -eq 0 ] || fail "run this script as root (use sudo)"
[ "$(uname -s)" = "Linux" ] || fail "Polipan needs Linux"
command -v systemctl >/dev/null 2>&1 || fail "systemd is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"

# ---- Packages ----
install_packages() {
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq wireguard-tools nftables iptables iproute2
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q wireguard-tools nftables iptables iproute
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache wireguard-tools nftables iptables iproute2
  else
    fail "install wireguard-tools, nftables, iptables and iproute2, then run this script again"
  fi
}
need=""
for c in wg nft iptables ip; do
  command -v "$c" >/dev/null 2>&1 || need="$need $c"
done
if [ -n "$need" ]; then
  say "Installing packages for:$need"
  install_packages
fi
modprobe wireguard 2>/dev/null || true
ip link add wgprobe type wireguard 2>/dev/null && ip link del wgprobe \
  || fail "this kernel has no WireGuard. Use Linux 5.6 or newer, or install the wireguard module"

if ! command -v docker >/dev/null 2>&1; then
  say "Installing Docker"
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker >/dev/null 2>&1 || true

# ---- The agent binary and restic ----
say "Getting the agent from $agent_image"
cid=$(docker create "$agent_image")
docker cp "$cid:/polipan-agent" /usr/local/bin/polipan-agent
if ! command -v restic >/dev/null 2>&1; then
  docker cp "$cid:/usr/local/bin/restic" /usr/local/bin/restic
  chmod 0755 /usr/local/bin/restic
fi
docker rm "$cid" >/dev/null
chmod 0755 /usr/local/bin/polipan-agent

# ---- Join ----
umask 077
mkdir -p /etc/polipan /var/lib/polipan-agent
if [ ! -f /var/lib/polipan-agent/node.json ]; then
  say "Joining the panel at $server"
  /usr/local/bin/polipan-agent join --server "$server" --token "$token" \
    --ca-fingerprint "$fingerprint" --data-dir /var/lib/polipan-agent
fi

# Docker pulls built images from the registry of the panel over TLS with the CA of the panel.
host=${server%:*}
mkdir -p /etc/docker/certs.d/172.31.251.10:5000
cp /var/lib/polipan-agent/ca.crt /etc/docker/certs.d/172.31.251.10:5000/ca.crt

cat > /etc/polipan/agent.env <<ENV
POLIPAN_AGENT_SERVER=$server
POLIPAN_AGENT_DATA_DIR=/var/lib/polipan-agent
POLIPAN_AGENT_WIREGUARD_PORT=$wg_port
ENV
[ -z "$public_ip" ] || echo "POLIPAN_AGENT_PUBLIC_IP=$public_ip" >> /etc/polipan/agent.env
chmod 0600 /etc/polipan/agent.env

cat > /etc/systemd/system/polipan-agent.service <<'UNIT'
[Unit]
Description=Polipan agent
After=docker.service network-online.target
Wants=network-online.target
Requires=docker.service

[Service]
EnvironmentFile=/etc/polipan/agent.env
ExecStart=/usr/local/bin/polipan-agent
Restart=always
RestartSec=2
# The agent needs root for Docker, nftables, WireGuard and the Caddy admin socket (documented limit of v1).
User=root

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now polipan-agent

say "The agent runs. The node shows up as pending, then ready, in the panel (host $host)."
say "Open UDP port $wg_port (WireGuard) and TCP 80 and 443 (ingress) in the firewall of this server."
