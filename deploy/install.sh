#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Installs the Polipan control plane and the agent of this host.
#
#   curl -fsSL https://raw.githubusercontent.com/OlexandrDevelop/polipan/main/deploy/install.sh | sudo sh -s -- --domain panel.example.com
#   curl -fsSL https://github.com/OlexandrDevelop/polipan/releases/latest/download/install.sh | sudo sh -s -- --domain panel.example.com   (a release)
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
#   --registry NAME      image registry prefix (default: ghcr.io/olexandrdevelop)
#   --server-image REF   full server image reference (overrides --registry and --version)
#   --agent-image REF    full agent image reference (overrides --registry and --version)
#   --acme-ca-url URL    ACME directory for the agent (tests use a staging CA)
#   --dir DIR            folder for the compose file (default: /opt/polipan)
#   --require-signature  stop if the signature of an image cannot be checked (needs cosign)
#   --allow-userns-sysctl  only for a host where the AppArmor profile of BuildKit cannot be loaded: turn off the
#                        system-wide Ubuntu limit on user namespaces (kernel.apparmor_restrict_unprivileged_userns)
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
registry="ghcr.io/olexandrdevelop"
server_image=""
agent_image=""
acme_ca_url=""
dir="/opt/polipan"
require_signature=""
allow_userns_sysctl=""

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
    --allow-userns-sysctl) allow_userns_sysctl=1; shift ;;
    -h|--help) sed -n '3,27p' "$0"; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

# What the user gave is saved for the updates. The images of a release are not: a later update picks its own.
user_server_image=$server_image
user_agent_image=$agent_image

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
  base_url="${POLIPAN_BASE_URL:-https://github.com/OlexandrDevelop/polipan/releases/download/$stamped_version}"
  compose_url="$base_url/docker-compose.yml"
  firewall_url="$base_url/polipan-build.sh"
else
  base_url="${POLIPAN_BASE_URL:-https://raw.githubusercontent.com/OlexandrDevelop/polipan/main/deploy}"
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
      --certificate-identity-regexp '^https://github\.com/OlexandrDevelop/polipan/\.github/workflows/release\.yml@refs/tags/v' \
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
# The panel and the updater talk through this folder (Settings, Updates). The server container runs as uid 65532.
mkdir -p /var/lib/polipan/updater
chown 65532:65532 /var/lib/polipan/updater
chmod 0750 /var/lib/polipan/updater
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

# ---- Rootless BuildKit on Ubuntu 24.04: user namespaces (REQ-SEC-102) ----
# Ubuntu 24.04 limits user namespaces for processes without an AppArmor profile. Rootless BuildKit needs them.
# We load a small profile that allows them for the BuildKit container only. The system-wide limit stays on.
userns_sysctl=/proc/sys/kernel/apparmor_restrict_unprivileged_userns
userns_restricted=""
[ "$(cat "$userns_sysctl" 2>/dev/null || echo 0)" = "1" ] && userns_restricted=1
if [ -n "$userns_restricted" ] && [ -d /sys/kernel/security/apparmor ]; then
  if ! command -v apparmor_parser >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
    say "Installing apparmor"
    DEBIAN_FRONTEND=noninteractive apt-get install -y apparmor >/dev/null 2>&1 || true
  fi
  cat > /etc/apparmor.d/polipan-buildkit <<'PROFILE'
# Polipan: allows user namespaces for the BuildKit container only (REQ-SEC-102).
abi <abi/4.0>,
include <tunables/global>

profile polipan-buildkit flags=(unconfined) {
  userns,
}
PROFILE
  chmod 0644 /etc/apparmor.d/polipan-buildkit
  if command -v apparmor_parser >/dev/null 2>&1 && apparmor_parser -r -W /etc/apparmor.d/polipan-buildkit 2>/dev/null; then
    say "Loaded the AppArmor profile polipan-buildkit (user namespaces for BuildKit only)"
  elif [ -n "$allow_userns_sysctl" ]; then
    say "WARNING: the AppArmor profile could not be loaded. --allow-userns-sysctl is set."
    say "WARNING: this turns off the Ubuntu limit on user namespaces for the whole server, for every program."
    sed -i 's/apparmor=polipan-buildkit/apparmor=unconfined/' "$dir/docker-compose.yml"
    printf '# Polipan: rootless BuildKit needs user namespaces (install.sh --allow-userns-sysctl)\nkernel.apparmor_restrict_unprivileged_userns = 0\n' > /etc/sysctl.d/60-polipan-userns.conf
    sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 >/dev/null
  else
    rm -f /etc/apparmor.d/polipan-buildkit
    fail "could not load the AppArmor profile for BuildKit (apparmor_parser is missing or failed). Install the package apparmor and run this script again. Only if you accept the risk, run it with --allow-userns-sysctl: it turns off the Ubuntu limit on user namespaces for the whole server (sysctl kernel.apparmor_restrict_unprivileged_userns=0)."
  fi
elif [ -z "$userns_restricted" ]; then
  # No limit on this host (for example Ubuntu 22.04 or Debian 12, AppArmor 3): the profile is not needed and its
  # AppArmor 4 syntax would not load. Keep the old setting.
  sed -i 's/apparmor=polipan-buildkit/apparmor=unconfined/' "$dir/docker-compose.yml"
else
  if [ -n "$allow_userns_sysctl" ]; then
    say "WARNING: --allow-userns-sysctl is set. This turns off the limit on user namespaces for the whole server."
    printf '# Polipan: rootless BuildKit needs user namespaces (install.sh --allow-userns-sysctl)\nkernel.apparmor_restrict_unprivileged_userns = 0\n' > /etc/sysctl.d/60-polipan-userns.conf
    sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 >/dev/null
  else
    say "WARNING: user namespaces are limited on this host and AppArmor is not active, so builds from Git cannot run."
    say "WARNING: images still work. To allow builds, run this script again with --allow-userns-sysctl,"
    say "WARNING: or run: sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 && docker restart polipan-buildkitd-1"
  fi
fi

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

# BuildKit must stay up. If it restarts all the time, say why (images still work, Git builds do not).
sleep 5
if [ "$(docker inspect -f '{{.State.Restarting}}' polipan-buildkitd-1 2>/dev/null || echo false)" = "true" ]; then
  say "WARNING: BuildKit does not start, so builds from Git cannot run. Apps from an image still work."
  if [ "$(cat "$userns_sysctl" 2>/dev/null || echo 0)" = "1" ]; then
    say "WARNING: reason: this host limits user namespaces (kernel.apparmor_restrict_unprivileged_userns=1)."
    say "WARNING: fix: run this script again with --allow-userns-sysctl, or run: sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 && docker restart polipan-buildkitd-1"
  else
    say "WARNING: look at: docker logs polipan-buildkitd-1"
  fi
fi

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
# The agent starts the workloads after a boot (Docker restart policy no): it must never give up restarting.
StartLimitIntervalSec=0

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

# ---- Updates: the panel can update itself, like a hosting panel (Settings, Updates) ----
# polipan-update runs this install script again with the options of this install. A timer runs it every night and
# updates when automatic updates are on. The panel asks for a check or an update through /var/lib/polipan/updater.
say "Setting up updates"
if [ -n "$pinned" ]; then
  source_url="https://github.com/OlexandrDevelop/polipan/releases/latest/download/install.sh"
else
  source_url="https://raw.githubusercontent.com/OlexandrDevelop/polipan/main/deploy/install.sh"
fi
cat > /etc/polipan/update.conf <<CONF
SOURCE_URL=$source_url
DIR=$dir
CONF
{
  [ -z "$domain" ] || printf '%s\n%s\n' --domain "$domain"
  [ -z "$address" ] || printf '%s\n%s\n' --address "$address"
  [ -z "$acme_email" ] || printf '%s\n%s\n' --acme-email "$acme_email"
  [ -z "$acme_ca_url" ] || printf '%s\n%s\n' --acme-ca-url "$acme_ca_url"
  [ "$dir" = /opt/polipan ] || printf '%s\n%s\n' --dir "$dir"
  [ "$registry" = "ghcr.io/olexandrdevelop" ] || printf '%s\n%s\n' --registry "$registry"
  [ -z "$user_server_image" ] || printf '%s\n%s\n' --server-image "$user_server_image"
  [ -z "$user_agent_image" ] || printf '%s\n%s\n' --agent-image "$user_agent_image"
  [ -z "$require_signature" ] || printf '%s\n' --require-signature
  [ -z "$allow_userns_sysctl" ] || printf '%s\n' --allow-userns-sysctl
} > /etc/polipan/update.args
chmod 0600 /etc/polipan/update.conf /etc/polipan/update.args
# Automatic updates are on for a new install. A choice made in the panel stays.
if [ ! -f /var/lib/polipan/updater/auto ]; then
  echo on > /var/lib/polipan/updater/auto
  chown 65532:65532 /var/lib/polipan/updater/auto
fi
cat > /usr/local/sbin/polipan-update <<'UPDATER'
#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Updates Polipan on the control-plane host. The panel asks for it (Settings, Updates), and a timer runs it every
# night when automatic updates are on. An update runs the install script again, with the options of the first
# install: the script keeps the master key, the .env file and the data. Before it starts, the database is dumped.
# When the new server does not become healthy, the old images come back.
#
#   polipan-update run      do what the panel asked for, or the nightly pass (used by systemd)
#   polipan-update check    look for a new version
#   polipan-update apply    update now
set -u

CONF=${POLIPAN_UPDATE_CONF:-/etc/polipan/update.conf}
ARGS=${POLIPAN_UPDATE_ARGS:-/etc/polipan/update.args}
STATE=${POLIPAN_UPDATE_STATE:-/var/lib/polipan/updater}
LOCK=${POLIPAN_UPDATE_LOCK:-/run/polipan-update.lock}
BACKUPS=${POLIPAN_UPDATE_BACKUPS:-/var/lib/polipan/backups}
[ -f "$CONF" ] || exit 0
# shellcheck disable=SC1090
. "$CONF"
: "${DIR:=/opt/polipan}"
mkdir -p "$STATE" "$BACKUPS"
exec 9>"$LOCK"
flock -n 9 || exit 0

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
clean() { printf '%s' "$1" | tr -d '"\\' | tr '\n\r\t' '   ' | cut -c1-300; }

# write_status STATE MESSAGE: the panel reads this file.
write_status() {
  tmp="$STATE/status.json.new"
  printf '{"state":"%s","message":"%s","current":"%s","latest":"%s","version":"%s","checked_at":"%s","updated_at":"%s"}\n' \
    "$1" "$(clean "$2")" "${current:-}" "${latest:-}" "${version:-}" "${checked_at:-}" "$(now)" > "$tmp"
  chmod 0644 "$tmp"
  mv -f "$tmp" "$STATE/status.json"
}

# digest_of REF: the digest of an image of ghcr.io in the registry, without pulling it.
digest_of() {
  repo=${1#ghcr.io/}
  repo=${repo%%:*}
  tag=${1##*:}
  token=$(curl -fsS "https://ghcr.io/token?scope=repository:$repo:pull" 2>/dev/null | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [ -n "$token" ] || return 1
  curl -fsSI -H "Authorization: Bearer $token" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
    "https://ghcr.io/v2/$repo/manifests/$tag" 2>/dev/null | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p' | head -n 1
}

# check: sets current, latest, version and the downloaded script in $tmpdir. Returns 0 when it could look.
check() {
  checked_at=$(now)
  tmpdir=$(mktemp -d)
  if ! curl -fsSL "$SOURCE_URL" -o "$tmpdir/install.sh" 2>/dev/null; then
    write_status error "Could not download the install script from $SOURCE_URL"
    return 1
  fi
  image=$(sed -n 's/^POLIPAN_SERVER_IMAGE=//p' "$DIR/.env")
  case "$image" in
    *@sha256:*) current=${image##*@} ;;
    *) current=$(docker image inspect -f '{{index .RepoDigests 0}}' "$image" 2>/dev/null | sed 's/.*@//') ;;
  esac
  stamped=$(sed -n 's/^stamped_server_digest="\(sha256:[0-9a-f]*\)"$/\1/p' "$tmpdir/install.sh")
  if [ -n "$stamped" ]; then
    latest=$stamped
    version=$(sed -n 's/^stamped_version="\(v[^"]*\)"$/\1/p' "$tmpdir/install.sh")
  else
    registry=$(sed -n 's/^registry="\([^"]*\)"$/\1/p' "$tmpdir/install.sh")
    latest=$(digest_of "$registry/polipan-server:latest")
    version=main
  fi
  if [ -z "$latest" ]; then
    write_status error "Could not read the newest version from the registry"
    return 1
  fi
  if [ "$latest" = "$current" ]; then
    write_status up_to_date "Polipan is up to date."
  else
    write_status available "A new version is ready: ${version:-main}."
  fi
  return 0
}

# apply: dump the database, run the install script, go back to the old images when it fails.
apply() {
  write_status updating "Updating. The panel restarts for a moment."
  stamp=$(date -u +%Y%m%d-%H%M%S)
  dump="$BACKUPS/pre-update-$stamp.sql.gz"
  if (cd "$DIR" && docker compose exec -T postgres pg_dump -U polipan polipan) 2>/dev/null | gzip > "$dump" && [ -s "$dump" ]; then
    chmod 0600 "$dump"
    # Keep the last three dumps.
    ls -1t "$BACKUPS"/pre-update-*.sql.gz 2>/dev/null | tail -n +4 | while read -r old; do rm -f "$old"; done
  else
    rm -f "$dump"
  fi
  cp "$DIR/.env" "$DIR/.env.prev"
  cp "$DIR/docker-compose.yml" "$DIR/docker-compose.yml.prev"
  set --
  if [ -f "$ARGS" ]; then
    while IFS= read -r line; do [ -n "$line" ] && set -- "$@" "$line"; done < "$ARGS"
  fi
  if POLIPAN_UPDATING=1 sh "$tmpdir/install.sh" "$@" > "$STATE/update.log" 2>&1; then
    check || true
    write_status up_to_date "Updated to ${version:-main}."
    return 0
  fi
  cp "$DIR/.env.prev" "$DIR/.env"
  cp "$DIR/docker-compose.yml.prev" "$DIR/docker-compose.yml"
  (cd "$DIR" && docker compose up -d) >> "$STATE/update.log" 2>&1
  write_status rolled_back "The update failed and the old version is running again. See $STATE/update.log."
  return 1
}

action=${1:-run}
if [ "$action" = run ]; then
  action=nightly
  if [ -s "$STATE/request" ]; then
    action=$(head -n 1 "$STATE/request" | tr -cd 'a-z_')
    rm -f "$STATE/request"
  fi
fi

case "$action" in
  check) check ;;
  apply) check && [ "$latest" != "$current" ] && apply ;;
  nightly)
    check && [ "$latest" != "$current" ] && [ "$(cat "$STATE/auto" 2>/dev/null)" = on ] && apply
    ;;
esac
rm -rf "${tmpdir:-}"
exit 0
UPDATER
chmod 0755 /usr/local/sbin/polipan-update
cat > /etc/systemd/system/polipan-update.service <<'UNIT'
[Unit]
Description=Polipan update (a check, or an update)
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/polipan-update run
TimeoutStartSec=30min
UNIT
cat > /etc/systemd/system/polipan-update.path <<'UNIT'
[Unit]
Description=Polipan update request from the panel

[Path]
PathExists=/var/lib/polipan/updater/request
Unit=polipan-update.service

[Install]
WantedBy=multi-user.target
UNIT
cat > /etc/systemd/system/polipan-update.timer <<'UNIT'
[Unit]
Description=Polipan nightly update check

[Timer]
OnCalendar=*-*-* 03:30:00
RandomizedDelaySec=45min
Persistent=true
Unit=polipan-update.service

[Install]
WantedBy=timers.target
UNIT
systemctl daemon-reload
systemctl enable --now polipan-update.path polipan-update.timer >/dev/null 2>&1 || say "Note: could not start the update timer. Updates by hand: sudo polipan-update apply"
# The first look at the version, so the panel has a status. It must not block the install.
[ -n "${POLIPAN_UPDATING:-}" ] || rm -f /var/lib/polipan/updater/status.json
[ -n "${POLIPAN_UPDATING:-}" ] || (/usr/local/sbin/polipan-update check >/dev/null 2>&1 &) || true

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
