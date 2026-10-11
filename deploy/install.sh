#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Installs the Polipan control plane and the agent of this host.
#
#   curl -fsSL https://github.com/polipan/polipan/releases/latest/download/install.sh | sudo sh -s -- --domain panel.example.com
#
# The script of a release installs exactly the files of that release: the images are pinned by digest, the other
# files are checked against the checksums in this script, and the signatures of the images are checked when
# cosign is installed. A copy from the main branch is a development build and uses the tag "latest".
#
# Options:
#   --domain HOST        public name of the panel (DNS must point to this server). Without it the panel
#                        is served over plain HTTP on the server address.
#   --address HOST[:PORT] address of the panel when there is no domain (default: the address of this server).
#                        Use it when the browser reaches the server through another address, for example a
#                        forwarded port: --address 127.0.0.1:8088
#   --acme-email EMAIL   contact address for the certificate authority (optional)
#   --version TAG        image tag (default: the release of this script, or latest in a development build)
#   --registry NAME      image registry prefix (default: ghcr.io/polipan)
#   --server-image REF   full server image reference (overrides --registry and --version)
#   --agent-image REF    full agent image reference (overrides --registry and --version)
#   --acme-ca-url URL    ACME directory for the agent (tests use a staging CA)
#   --dir DIR            folder for the compose file (default: /opt/polipan)
#   --require-signature  stop if the signature of an image cannot be checked (needs cosign)
set -eu

# The release build puts the values of the release here (deploy/release/stamp.sh).
# In a development build they stay as they are.
stamped_version="@VERSION@"
stamped_server_digest="@SERVER_IMAGE_DIGEST@"
stamped_agent_digest="@AGENT_IMAGE_DIGEST@"
stamped_compose_sha256="@COMPOSE_SHA256@"
stamped_firewall_sha256="@FIREWALL_SHA256@"

domain=""
address=""
acme_email=""
version="latest"
registry="ghcr.io/polipan"
server_image=""
agent_image=""
acme_ca_url=""
dir="/opt/polipan"
require_signature=""

say() { printf '==> %s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) domain=$2; shift 2 ;;
    --address) address=$2; shift 2 ;;
    --acme-email) acme_email=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --registry) registry=$2; shift 2 ;;
    --server-image) server_image=$2; shift 2 ;;
    --agent-image) agent_image=$2; shift 2 ;;
    --acme-ca-url) acme_ca_url=$2; shift 2 ;;
    --dir) dir=$2; shift 2 ;;
    --require-signature) require_signature=1; shift ;;
    -h|--help) sed -n '3,27p' "$0"; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || fail "run this script as root (use sudo)"
[ "$(uname -s)" = "Linux" ] || fail "Polipan needs Linux"
command -v systemctl >/dev/null 2>&1 || fail "systemd is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"

# A release script installs the images of its release by digest. A development build, or another --version, uses tags.
is_digest() { case "$1" in sha256:????????????????????????????????????????????????????????????????) return 0 ;; *) return 1 ;; esac; }
pinned=""
is_version() { case "$1" in v[0-9]*) return 0 ;; *) return 1 ;; esac; }
if is_digest "$stamped_server_digest" && is_digest "$stamped_agent_digest" && is_version "$stamped_version"; then
  pinned=1
  if [ "$version" = "latest" ]; then
    # The default registry is the one of the release. Another registry is a mirror: the digest is the same.
    [ -n "$server_image" ] || server_image="$registry/polipan-server@$stamped_server_digest"
    [ -n "$agent_image" ] || agent_image="$registry/polipan-agent@$stamped_agent_digest"
    version="$stamped_version"
  fi
fi
[ -n "$server_image" ] || server_image="$registry/polipan-server:$version"
[ -n "$agent_image" ] || agent_image="$registry/polipan-agent:$version"
for ref in "$server_image" "$agent_image"; do
  case "$ref" in
    *@sha256:*) ;;
    *) say "Warning: the image is not pinned by digest: $ref" ;;
  esac
done

# Files of a release come from the release page and must match the checksums in this script.
if [ -n "$pinned" ]; then
  base_url="${POLIPAN_BASE_URL:-https://github.com/polipan/polipan/releases/download/$stamped_version}"
  compose_url="$base_url/docker-compose.yml"
  firewall_url="$base_url/polipan-build.sh"
else
  base_url="${POLIPAN_BASE_URL:-https://raw.githubusercontent.com/polipan/polipan/main/deploy}"
  compose_url="$base_url/compose/docker-compose.yml"
  firewall_url="$base_url/nftables/polipan-build.sh"
fi

# check_sha256 FILE WANT: stop when the file is not the one of the release.
check_sha256() {
  [ -n "$pinned" ] || return 0
  got=$(sha256sum "$1" | cut -d ' ' -f 1)
  [ "$got" = "$2" ] || fail "the checksum of $(basename "$1") is wrong: the download is not the file of release $stamped_version"
}

verify_image() {
  if command -v cosign >/dev/null 2>&1; then
    say "Checking the signature of $1"
    cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      --certificate-identity-regexp '^https://github\.com/polipan/polipan/\.github/workflows/release\.yml@refs/tags/v' \
      "$1" >/dev/null 2>&1 || fail "the signature of $1 is not valid"
  elif [ -n "$require_signature" ]; then
    fail "--require-signature needs cosign. Install it from https://docs.sigstore.dev/cosign/installation/"
  else
    say "cosign is not installed: the signatures are not checked, the digest pin still holds."
  fi
}
if [ -n "$pinned" ] || [ -n "$require_signature" ]; then
  verify_image "$server_image"
  verify_image "$agent_image"
fi

# The address users open in the browser.
if [ -n "$domain" ]; then
  external_url="https://$domain"
  host="$domain"
elif [ -n "$address" ]; then
  external_url="http://$address"
  host=${address%%:*}
else
  host=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1)
  [ -n "$host" ] || host=$(hostname -I 2>/dev/null | cut -d ' ' -f 1)
  [ -n "$host" ] || fail "cannot find the address of this server, use --domain"
  external_url="http://$host"
fi

# ---- Docker and nftables ----
if ! command -v docker >/dev/null 2>&1; then
  say "Installing Docker"
  curl -fsSL https://get.docker.com | sh
fi
docker compose version >/dev/null 2>&1 || fail "docker compose plugin is missing"
systemctl enable --now docker >/dev/null 2>&1 || true
if ! command -v nft >/dev/null 2>&1; then
  say "Installing nftables"
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y nftables
  else
    fail "install nftables and run this script again"
  fi
fi
# The WireGuard mesh and the forwarding rules of the agent (M7).
for c in wg iptables ip; do
  if ! command -v "$c" >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      say "Installing wireguard-tools, iptables and iproute2"
      DEBIAN_FRONTEND=noninteractive apt-get install -y wireguard-tools iptables iproute2
      break
    fi
    fail "install wireguard-tools, iptables and iproute2, then run this script again"
  fi
done
modprobe wireguard 2>/dev/null || true

# ---- Files ----
umask 077
mkdir -p /etc/polipan "$dir" /var/lib/polipan-agent
if [ ! -s /etc/polipan/master.key ]; then
  say "Creating the master key /etc/polipan/master.key"
  head -c 32 /dev/urandom | base64 > /etc/polipan/master.key
fi
# The server container runs as uid 65532.
chown 65532:65532 /etc/polipan/master.key
chmod 0400 /etc/polipan/master.key

if [ -f "$dir/.env" ]; then
  pg_password=$(sed -n 's/^POSTGRES_PASSWORD=//p' "$dir/.env")
  victoria_password=$(sed -n 's/^VICTORIA_PASSWORD=//p' "$dir/.env")
else
  pg_password=$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')
  victoria_password=""
fi
[ -n "$victoria_password" ] || victoria_password=$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')
cat > "$dir/.env" <<ENV
POSTGRES_PASSWORD=$pg_password
VICTORIA_PASSWORD=$victoria_password
POLIPAN_SERVER_IMAGE=$server_image
POLIPAN_EXTERNAL_URL=$external_url
POLIPAN_AGENT_HOSTS=$host,localhost
POLIPAN_ACME_EMAIL=$acme_email
ENV
chmod 0600 "$dir/.env"

script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)
if [ -n "$script_dir" ] && [ -f "$script_dir/compose/docker-compose.yml" ]; then
  cp "$script_dir/compose/docker-compose.yml" "$dir/docker-compose.yml"
  cp "$script_dir/nftables/polipan-build.sh" /usr/local/sbin/polipan-build-firewall
else
  say "Downloading the compose file"
  curl -fsSL "$compose_url" -o "$dir/docker-compose.yml"
  curl -fsSL "$firewall_url" -o /usr/local/sbin/polipan-build-firewall
  check_sha256 "$dir/docker-compose.yml" "$stamped_compose_sha256"
  check_sha256 /usr/local/sbin/polipan-build-firewall "$stamped_firewall_sha256"
fi
chmod 0755 /usr/local/sbin/polipan-build-firewall

# ---- Build network isolation (REQ-SEC-108): nftables rules for the build network, also at boot ----
cat > /etc/systemd/system/polipan-build-firewall.service <<'UNIT'
[Unit]
Description=Polipan build network isolation (nftables)
After=docker.service nftables.service
Wants=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
EnvironmentFile=-/etc/polipan/build-firewall.env
ExecStart=/bin/sh /usr/local/sbin/polipan-build-firewall

[Install]
WantedBy=multi-user.target
UNIT
[ -f /etc/polipan/build-firewall.env ] || printf 'MESH_CIDR=10.210.0.0/16\nALLOW_PRIVATE=0\n' > /etc/polipan/build-firewall.env
systemctl daemon-reload
systemctl enable polipan-build-firewall >/dev/null 2>&1 || true
say "Loading the build network firewall"
systemctl restart polipan-build-firewall || fail "could not load the build network firewall (nftables)"

# ---- Control plane ----
if ! docker network inspect polipan >/dev/null 2>&1; then
  # The first node of the panel gets the subnet 10.210.1.0/24 of mesh_cidr: the network of the node is its mesh subnet.
  docker network create --subnet 10.210.1.0/24 --ip-range 10.210.1.128/25 --gateway 10.210.1.1 polipan >/dev/null
fi
say "Starting PostgreSQL, the metric and log stores and the server"
(cd "$dir" && docker compose pull --quiet 2>/dev/null || true)
(cd "$dir" && docker compose up -d)

i=0
until (cd "$dir" && docker compose exec -T server /polipan-server healthcheck >/dev/null 2>&1); do
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then
    (cd "$dir" && docker compose logs --tail 50 server)
    fail "the server did not become healthy"
  fi
  sleep 2
done

# ---- Agent of this host ----
say "Installing the agent"
cid=$(docker create "$agent_image")
# A running agent keeps its old binary open: write the new file next to it and move it over (atomic).
docker cp "$cid:/polipan-agent" /usr/local/bin/polipan-agent.new
chmod 0755 /usr/local/bin/polipan-agent.new
mv -f /usr/local/bin/polipan-agent.new /usr/local/bin/polipan-agent
# restic makes the backups (F10). The agent image has a static restic, so no package is needed.
if ! command -v restic >/dev/null 2>&1; then
  docker cp "$cid:/usr/local/bin/restic" /usr/local/bin/restic
  chmod 0755 /usr/local/bin/restic
fi
docker rm "$cid" >/dev/null
chmod 0755 /usr/local/bin/polipan-agent
mkdir -p /var/lib/polipan/backups
command -v ssh >/dev/null 2>&1 || say "Note: install an ssh client (openssh-client) to use SFTP backup targets."

if [ ! -f /var/lib/polipan-agent/node.json ]; then
  say "Joining this host to the panel"
  out=$(cd "$dir" && docker compose exec -T server /polipan-server join-token --name local --label ingress=true)
  token=$(printf '%s\n' "$out" | sed -n 's/^token=//p')
  fingerprint=$(printf '%s\n' "$out" | sed -n 's/^ca_fingerprint=//p')
  [ -n "$token" ] && [ -n "$fingerprint" ] || fail "could not create the join token"
  /usr/local/bin/polipan-agent join --server 127.0.0.1:9443 --token "$token" \
    --ca-fingerprint "$fingerprint" --data-dir /var/lib/polipan-agent
fi

# Docker of this host pulls built images from the registry over TLS with the server CA.
mkdir -p /etc/docker/certs.d/172.31.251.10:5000
cp /var/lib/polipan-agent/ca.crt /etc/docker/certs.d/172.31.251.10:5000/ca.crt

cat > /etc/polipan/agent.env <<ENV
POLIPAN_AGENT_SERVER=127.0.0.1:9443
POLIPAN_AGENT_DATA_DIR=/var/lib/polipan-agent
POLIPAN_AGENT_ACME_CA_URL=$acme_ca_url
ENV
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
# The agent needs root for Docker, nftables and the Caddy admin socket (documented limit of v1).
User=root

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable polipan-agent
# Start it, or restart it when it runs: a running agent must use the new binary and the new settings.
systemctl restart polipan-agent

# ---- Wait for the panel behind Caddy ----
say "Waiting for the panel at $external_url"
i=0
ok=""
while [ "$i" -lt 60 ]; do
  if [ -n "$domain" ]; then
    curl -fsSk --resolve "$domain:443:127.0.0.1" "https://$domain/healthz" >/dev/null 2>&1 && ok=1 && break
  else
    curl -fsS -H "Host: $host" "http://127.0.0.1/healthz" >/dev/null 2>&1 && ok=1 && break
  fi
  i=$((i + 1))
  sleep 2
done

echo
if [ -n "$ok" ]; then
  say "Polipan is running."
else
  say "The agent is still starting. Check: journalctl -u polipan-agent -f"
fi
setup_url=$(cd "$dir" && docker compose exec -T server /polipan-server setup-url 2>/dev/null || true)
echo "Open this one-time link and create the first user:"
echo "  ${setup_url:-$external_url}"
[ -z "$domain" ] || echo "DNS of $domain must point to this server so the certificate can be issued."
echo
echo "IMPORTANT: back up /etc/polipan/master.key. Without it the secrets cannot be read."
