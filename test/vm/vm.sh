#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# A small Ubuntu 24.04 virtual machine to test Polipan on a real Linux host. See test/vm/README.md.
#
#   test/vm/vm.sh create     download the image, make the disk, start the VM, wait for cloud-init
#   test/vm/vm.sh up         start the VM (create does this too)
#   test/vm/vm.sh down       stop the VM (clean power off)
#   test/vm/vm.sh status     state, ports and the real disk size
#   test/vm/vm.sh ssh [cmd]  open a shell in the VM, or run a command
#   test/vm/vm.sh destroy    stop the VM and delete its disk (destroy --all also deletes the image cache)
#   test/vm/vm.sh install    build the images, copy them to the VM, run deploy/install.sh, print the setup link
#   test/vm/vm.sh reset      delete all Polipan data in the VM and install again (fresh panel, new setup link)
#   test/vm/vm.sh verify     run the checks of test/vm/verify.py against the VM (one stage name runs only that stage)
#   test/vm/vm.sh verify-ui  open the panel in a headless browser and check the screens (needs the web dependencies)
#
# Everything runs as your user. No sudo, no bridge, no libvirt. The VM ports are open on 127.0.0.1 only.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
cache=${POLIPAN_VM_CACHE:-$HOME/.cache/polipan-vm}
dl=$cache/dl
vmdir=$cache/vm
ssh_port=${POLIPAN_VM_SSH_PORT:-2222}
http_port=${POLIPAN_VM_HTTP_PORT:-8088}
https_port=${POLIPAN_VM_HTTPS_PORT:-8443}
disk_max=${POLIPAN_VM_DISK:-10G}
mem=${POLIPAN_VM_MEMORY:-2048}
cpus=${POLIPAN_VM_CPUS:-2}
user=polipan

base_name=ubuntu-24.04-server-cloudimg-amd64.img
base_url=https://cloud-images.ubuntu.com/releases/noble/release
base=$dl/$base_name
disk=$vmdir/disk.qcow2
seed=$vmdir/seed.iso
key=$vmdir/id_ed25519
pidfile=$vmdir/qemu.pid
console=$vmdir/console.log
known=$vmdir/known_hosts

say() { printf '==> %s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

running() {
  [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null
}

ssh_args() {
  printf '%s\n' -i "$key" -p "$ssh_port" -o UserKnownHostsFile="$known" -o StrictHostKeyChecking=no \
    -o LogLevel=ERROR -o IdentitiesOnly=yes -o PasswordAuthentication=no -o ConnectTimeout=5 \
    -o ServerAliveInterval=15
}
vm_ssh() {
  local args=()
  while IFS= read -r a; do args+=("$a"); done < <(ssh_args)
  ssh "${args[@]}" "$user@127.0.0.1" "$@"
}

wait_ssh() {
  local i=0 max=${1:-180}
  until vm_ssh true >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -lt "$max" ] || fail "the VM does not answer on SSH. See $console"
    running || fail "the VM stopped. See $console"
    sleep 2
  done
}

accel() {
  if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then echo kvm; else echo tcg; fi
}

# ---- image ----
fetch_image() {
  mkdir -p "$dl"
  local sums=$dl/SHA256SUMS
  curl -fsSL -o "$sums" "$base_url/SHA256SUMS" || fail "cannot download $base_url/SHA256SUMS"
  local want
  want=$(sed -n "s/^\([0-9a-f]\{64\}\) \*\?$base_name\$/\1/p" "$sums")
  [ -n "$want" ] || fail "no checksum for $base_name in SHA256SUMS"
  if [ -f "$base" ] && [ "$(sha256sum "$base" | cut -d ' ' -f 1)" = "$want" ]; then
    say "Ubuntu image is in the cache and the sha256 is right"
    return 0
  fi
  say "Downloading the Ubuntu 24.04 image (about 600 MB, once)"
  curl -fL --retry 3 -C - -o "$base.part" "$base_url/$base_name"
  local got
  got=$(sha256sum "$base.part" | cut -d ' ' -f 1)
  if [ "$got" != "$want" ]; then
    rm -f "$base.part"
    fail "the sha256 of the image is wrong (got $got, want $want)"
  fi
  mv "$base.part" "$base"
  say "sha256 checked: $want"
}

make_seed() {
  [ -f "$key" ] || ssh-keygen -q -t ed25519 -N '' -C polipan-vm -f "$key"
  local pub tmp mk
  pub=$(cat "$key.pub")
  tmp=$(mktemp -d)
  cat > "$tmp/user-data" <<EOF
#cloud-config
hostname: polipan-vm
users:
  - name: $user
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
      - $pub
ssh_pwauth: false
disable_root: true
package_update: true
packages:
  - docker.io
  - docker-compose-v2
  - nftables
  - wireguard-tools
  - iptables
  - iproute2
  - curl
  - ca-certificates
runcmd:
  - [systemctl, enable, --now, docker]
  - [usermod, -aG, docker, $user]
  - [apt-get, clean]
  - [sh, -c, 'fstrim -av || true']
EOF
  printf 'instance-id: polipan-vm-1\nlocal-hostname: polipan-vm\n' > "$tmp/meta-data"
  for mk in genisoimage mkisofs xorriso; do command -v "$mk" >/dev/null 2>&1 && break || mk=""; done
  [ -n "$mk" ] || fail "install genisoimage (or xorriso) to make the cloud-init seed"
  if [ "$mk" = xorriso ]; then
    xorriso -as mkisofs -quiet -output "$seed" -volid cidata -joliet -rock "$tmp/user-data" "$tmp/meta-data" >/dev/null 2>&1
  else
    "$mk" -quiet -output "$seed" -volid cidata -joliet -rock "$tmp/user-data" "$tmp/meta-data" >/dev/null 2>&1
  fi
  rm -rf "$tmp"
}

# ---- commands ----
cmd_up() {
  [ -f "$disk" ] || fail "no VM yet. Run: test/vm/vm.sh create"
  if running; then say "The VM is already running (pid $(cat "$pidfile"))"; return 0; fi
  local mode cpu
  mode=$(accel)
  if [ "$mode" = kvm ]; then cpu=host; else cpu=max; say "No /dev/kvm: QEMU uses software emulation. It is slow."; fi
  for p in "$ssh_port" "$http_port" "$https_port"; do
    if ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then fail "port $p is already used on this computer"; fi
  done
  say "Starting the VM ($mode, $cpus CPUs, $mem MB memory)"
  rm -f "$pidfile"
  qemu-system-x86_64 -name polipan-vm -machine "q35,accel=$mode" -cpu "$cpu" -smp "$cpus" -m "$mem" \
    -display none -serial "file:$console" -daemonize -pidfile "$pidfile" \
    -drive "file=$disk,if=virtio,format=qcow2,discard=unmap" \
    -drive "file=$seed,if=virtio,format=raw,readonly=on" \
    -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$ssh_port-:22,hostfwd=tcp:127.0.0.1:$http_port-:80,hostfwd=tcp:127.0.0.1:$https_port-:443" \
    -device virtio-net-pci,netdev=n0 -device virtio-rng-pci
  local max=90
  [ "$mode" = kvm ] || max=600
  wait_ssh "$max"
  say "The VM answers on SSH (127.0.0.1:$ssh_port)"
}

cmd_create() {
  command -v qemu-system-x86_64 >/dev/null || fail "qemu-system-x86_64 is not installed"
  command -v qemu-img >/dev/null || fail "qemu-img is not installed"
  mkdir -p "$vmdir"
  chmod 0700 "$vmdir"
  fetch_image
  [ -f "$seed" ] && [ -f "$key" ] || make_seed
  if [ ! -f "$disk" ]; then
    say "Making the overlay disk (copy on write, at most $disk_max, grows only as used)"
    qemu-img create -q -f qcow2 -b "$base" -F qcow2 "$disk" "$disk_max"
  else
    say "The disk exists already"
  fi
  cmd_up
  say "Waiting for cloud-init (it installs Docker, a few minutes)"
  vm_ssh 'sudo cloud-init status --wait' || true
  vm_ssh 'sudo cloud-init status --long | head -5; docker --version; docker compose version'
  vm_ssh 'docker info >/dev/null 2>&1 || sudo docker info >/dev/null' || fail "Docker does not work in the VM"
  cmd_status
}

cmd_down() {
  if ! running; then say "The VM is not running"; rm -f "$pidfile"; return 0; fi
  local pid i=0
  pid=$(cat "$pidfile")
  say "Stopping the VM"
  vm_ssh 'sudo systemctl poweroff' >/dev/null 2>&1 || true
  while kill -0 "$pid" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
      say "The VM did not stop in 2 minutes: stopping the process"
      kill "$pid" 2>/dev/null || true
      sleep 3
      break
    fi
    sleep 2
  done
  rm -f "$pidfile"
  say "Stopped"
}

cmd_status() {
  if [ ! -f "$disk" ]; then echo "VM: not created"; return 0; fi
  if running; then
    echo "VM: running (pid $(cat "$pidfile"), accelerator: $(accel))"
    echo "SSH:   ssh -p $ssh_port -i $key $user@127.0.0.1   (or: test/vm/vm.sh ssh)"
    echo "Panel: http://127.0.0.1:$http_port   HTTPS: https://127.0.0.1:$https_port   (this computer only)"
    if vm_ssh true >/dev/null 2>&1; then echo "SSH answers: yes"; else echo "SSH answers: no (still starting?)"; fi
    if curl -fsS -m 5 "http://127.0.0.1:$http_port/healthz" >/dev/null 2>&1; then echo "Panel answers: yes"; else echo "Panel answers: no"; fi
  else
    echo "VM: stopped"
  fi
  local virt real_k base_k
  virt=$(qemu-img info -U --output=json "$disk" | sed -n 's/.*"virtual-size": \([0-9]*\).*/\1/p' | tail -n 1)
  real_k=$(du -k "$disk" | cut -f 1)
  base_k=$(du -k "$base" 2>/dev/null | cut -f 1 || echo 0)
  echo "Disk: max $((virt / 1024 / 1024 / 1024)) GB, real size of the VM disk: $((real_k / 1024)) MB"
  echo "Ubuntu image (cache, read only, shared): $((base_k / 1024)) MB"
  echo "Total on this computer: $(((real_k + base_k) / 1024)) MB ($(du -sh "$cache" | cut -f 1) in $cache)"
}

cmd_ssh() {
  running || fail "the VM is not running. Run: test/vm/vm.sh up"
  vm_ssh "$@"
}

cmd_destroy() {
  local all=${1:-}
  [ -d "$vmdir" ] || say "No VM to delete"
  if running; then
    say "Stopping the VM"
    kill "$(cat "$pidfile")" 2>/dev/null || true
    sleep 3
  fi
  local before after
  before=$(du -sk "$cache" 2>/dev/null | cut -f 1 || echo 0)
  rm -rf "$vmdir"
  [ "$all" != "--all" ] || rm -rf "$dl"
  after=$(du -sk "$cache" 2>/dev/null | cut -f 1 || echo 0)
  say "Deleted. Freed about $(((before - after) / 1024)) MB"
  [ "$all" = "--all" ] || say "The Ubuntu image stays in $dl. Use 'destroy --all' to delete it too."
}

# Copies the images and the files of the repository into the VM and runs the installer.
cmd_install() {
  running || fail "the VM is not running"
  cd "$root"
  if [ -z "${SKIP_BUILD:-}" ]; then
    say "Building the images (make images)"
    make images >/dev/null
  fi
  local img
  for img in polipan-server:dev polipan-agent:dev; do
    say "Copying $img to the VM (compressed)"
    docker save "$img" | gzip -1 | vm_ssh 'gunzip | sudo docker load' >/dev/null
  done
  say "Copying deploy/ to the VM"
  tar -C "$root" -c deploy | vm_ssh 'rm -rf /tmp/polipan && mkdir /tmp/polipan && tar -C /tmp/polipan -x'
  say "Running deploy/install.sh (local images, no domain)"
  vm_ssh "sudo sh /tmp/polipan/deploy/install.sh --server-image polipan-server:dev --agent-image polipan-agent:dev --address 127.0.0.1:$http_port" | tee "$vmdir/install.log"
  print_setup_url
}

print_setup_url() {
  local url
  url=$(vm_ssh "sudo sh -c 'cd /opt/polipan && docker compose exec -T server /polipan-server setup-url'" | tr -d '\r')
  echo
  echo "Open this one-time link in the browser on this computer to create the first user:"
  echo "  $url"
}

cmd_reset() {
  running || fail "the VM is not running"
  say "Deleting all Polipan data in the VM"
  vm_ssh 'sudo sh -s' <<'EOS'
set -e
systemctl disable --now polipan-agent 2>/dev/null || true
(cd /opt/polipan 2>/dev/null && docker compose down -v --remove-orphans) || true
docker ps -aq | xargs -r docker rm -f >/dev/null
docker volume ls -q | xargs -r docker volume rm -f >/dev/null || true
docker network rm polipan >/dev/null 2>&1 || true
rm -rf /var/lib/polipan-agent /var/lib/polipan /etc/docker/certs.d/172.31.251.10:5000 /etc/polipan/master.key
docker image prune -f >/dev/null
EOS
  say "Installing again from the images in the VM"
  vm_ssh "sudo sh /tmp/polipan/deploy/install.sh --server-image polipan-server:dev --agent-image polipan-agent:dev --address 127.0.0.1:$http_port" | tee "$vmdir/install.log"
  print_setup_url
}

cmd_verify() {
  running || fail "the VM is not running"
  cd "$root"
  POLIPAN_VM_SSH_PORT=$ssh_port POLIPAN_VM_HTTP_PORT=$http_port POLIPAN_VM_HTTPS_PORT=$https_port \
    POLIPAN_VM_KEY=$key POLIPAN_VM_KNOWN=$known POLIPAN_VM_SH="$root/test/vm/vm.sh" \
    python3 -I "$root/test/vm/verify.py" "$@"
}

cmd_verify_ui() {
  cd "$root"
  UI_URL="http://127.0.0.1:$http_port" UI_OUT="${UI_OUT:-$vmdir/screenshots}" node "$root/test/vm/ui-check.mjs"
}

case "${1:-}" in
  create) cmd_create ;;
  up) cmd_up ;;
  down) cmd_down ;;
  status) cmd_status ;;
  ssh) shift; cmd_ssh "$@" ;;
  destroy) shift; cmd_destroy "${1:-}" ;;
  install) cmd_install ;;
  reset) cmd_reset ;;
  verify) shift; cmd_verify "$@" ;;
  verify-ui) cmd_verify_ui ;;
  *) sed -n '3,16p' "$0"; [ -z "${1:-}" ] || exit 2 ;;
esac
