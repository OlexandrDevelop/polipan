#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# End-to-end test of milestone M7 (T-E2E-07, T-E2E-08, AC-NODE-01, 03 to 10, REQ-SEC-106).
# Three nodes are three Docker-in-Docker containers (docker:dind) on one Docker network, each with its own Docker,
# agent and Caddy. Node 1 also runs the control plane. WireGuard is real: the kernel module of the host is used.
# Nothing of the real Docker of the machine is changed, apart from the network, the volume and the images that
# are named below. They are removed at the end.
#
#   sh test/e2e/run-m7.sh            builds the images, runs the whole test and cleans up
#   KEEP=1 sh ...                    keep the test containers after the run
#   SKIP_BUILD=1 sh ...              use the images polipan-server:m7 and polipan-agent:m7 that exist
#
# Disk: it needs about 6 GB. It stops when less than 5 GB is free.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
net=pp-m7
bk=pp-m7-bk
n1=pp-m7-n1
n2=pp-m7-n2
n3=pp-m7-n3
ip1=172.29.7.11
ip2=172.29.7.12
ip3=172.29.7.13
step() { printf '\n== %s\n' "$*"; }
free_gb() { df -BG --output=avail / | tail -1 | tr -dc '0-9'; }
dx() { host=$1; shift; docker exec "$host" "$@"; }
driver() { python3 -I "$root/test/e2e/driver_m7.py" "$@"; }

start_free=$(free_gb)
[ "$start_free" -ge 5 ] || { echo "only $start_free GB free: stop"; exit 1; }
docker system df > /tmp/pp-m7-df-before.txt 2>/dev/null || true

cleanup() {
  code=$?
  if [ "$code" -ne 0 ]; then
    echo "--- server log (last lines) ---"
    docker exec "$n1" docker logs --tail 60 server 2>&1 || true
    for name in "$n1" "$n2" "$n3"; do
      echo "--- agent log of $name (last lines) ---"
      docker exec "$name" docker logs --tail 40 agent 2>&1 || true
    done
  fi
  if [ -z "${KEEP:-}" ]; then
    docker rm -fv "$n1" "$n2" "$n3" >/dev/null 2>&1 || true
    docker network rm "$net" >/dev/null 2>&1 || true
    docker volume rm "$bk" >/dev/null 2>&1 || true
    [ -n "${SKIP_BUILD:-}" ] || docker image rm polipan-server:m7 polipan-agent:m7 >/dev/null 2>&1 || true
  fi
  end_free=$(free_gb)
  echo "disk: $start_free GB free at the start, $end_free GB free now"
}
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  step "Build the server and the agent"
  out=$(mktemp -d)
  src=$(mktemp -d)
  # Build the committed code only: other work in the tree must not change the test.
  git archive HEAD | tar -x -C "$src"
  docker run --rm -v "$src":/src -v pp-gocache:/root/.cache/go-build -v pp-gomod:/go/pkg/mod -v "$out":/out -w /src \
    -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.26 \
    go build -trimpath -ldflags "-s -w" -o /out/polipan-server ./server/cmd/polipan-server
  cp test/e2e/m7/server.Dockerfile "$out/Dockerfile"
  docker build -q -t polipan-server:m7 "$out" >/dev/null
  rm -rf "$out"
  docker build -q -f deploy/docker/agent.Dockerfile --build-arg VERSION=m7 -t polipan-agent:m7 "$src" >/dev/null
  rm -rf "$src"
fi

step "Start three nodes"
docker rm -fv "$n1" "$n2" "$n3" >/dev/null 2>&1 || true
docker network rm "$net" >/dev/null 2>&1 || true
docker volume rm "$bk" >/dev/null 2>&1 || true
docker network create --subnet 172.29.7.0/24 "$net" >/dev/null
docker volume create "$bk" >/dev/null
for pair in "$n1:$ip1" "$n2:$ip2" "$n3:$ip3"; do
  name=${pair%%:*}
  addr=${pair##*:}
  docker run -d --privileged --name "$name" --hostname "$name" --network "$net" --ip "$addr" -v "$bk":/var/lib/polipan/backups \
    -e DOCKER_TLS_CERTDIR= docker:27-dind >/dev/null
done
for name in $n1 $n2 $n3; do
  i=0
  until dx "$name" docker version >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -lt 60 ] || { echo "$name: Docker did not start"; exit 1; }
    sleep 1
  done
  dx "$name" chmod 0777 /var/lib/polipan/backups
done

step "Load the images"
for name in $n1 $n2 $n3; do
  docker save polipan-agent:m7 | docker exec -i "$name" docker load >/dev/null
done
docker save polipan-server:m7 | docker exec -i "$n1" docker load >/dev/null
for name in $n1 $n2 $n3; do
  # The test services use nginx. Load it from the host, so a slow pull cannot make a deploy slow.
  for img in caddy:2.10.2 nginx:alpine alpine:3.20; do
    docker image inspect "$img" >/dev/null 2>&1 || docker pull -q "$img" >/dev/null
    docker save "$img" | docker exec -i "$name" docker load >/dev/null
  done
done
docker save postgres:16-alpine | docker exec -i "$n1" docker load >/dev/null

step "Control plane on node 1"
dx $n1 sh -c 'mkdir -p /etc/polipan /var/lib/polipan-agent && head -c 32 /dev/urandom | base64 > /etc/polipan/master.key && chown 65532:65532 /etc/polipan/master.key && chmod 0400 /etc/polipan/master.key'
# The network of the node is its mesh subnet. The first node gets 10.210.1.0/24. Docker keeps its own addresses in the upper half.
dx $n1 docker network create --subnet 10.210.1.0/24 --ip-range 10.210.1.128/25 --gateway 10.210.1.1 polipan >/dev/null
dx $n1 docker network create --subnet 172.31.251.0/24 cp >/dev/null
dx $n1 docker run -d --name pg --network cp --network-alias pg -e POSTGRES_USER=polipan -e POSTGRES_DB=polipan -e POSTGRES_PASSWORD=pw postgres:16-alpine >/dev/null
i=0
until dx $n1 docker exec pg pg_isready -U polipan -d polipan >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "PostgreSQL did not start"; exit 1; }
  sleep 1
done
dx $n1 docker run -d --name server --network cp --restart unless-stopped -p 9443:9443 \
  -v /etc/polipan/master.key:/etc/polipan/master.key:ro \
  -e POLIPAN_DATABASE_URL='postgres://polipan:pw@pg:5432/polipan?sslmode=disable' -e POLIPAN_LISTEN=:8080 \
  -e POLIPAN_EXTERNAL_URL=http://panel.test -e POLIPAN_AGENT_HOSTS="$ip1,localhost" -e POLIPAN_MASTER_KEY_FILE=/etc/polipan/master.key \
  -e POLIPAN_NODE_SUSPECT_AFTER=10s -e POLIPAN_NODE_DOWN_AFTER=30s polipan-server:m7 >/dev/null
# Caddy reaches the panel by this name on the network of the node.
dx $n1 docker network connect --alias polipan-server --ip 10.210.1.250 polipan server
i=0
until dx $n1 docker exec server /polipan-server healthcheck >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { dx $n1 docker logs server; exit 1; }
  sleep 2
done
fp=$(dx $n1 docker exec server /polipan-server ca-fingerprint)

agent_up() { # node-container server-address token
  name=$1
  dx "$name" docker run --rm --net host -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/polipan-agent:/var/lib/polipan-agent \
    polipan-agent:m7 join --server "$2" --token "$3" --ca-fingerprint "$fp" --data-dir /var/lib/polipan-agent
  dx "$name" docker run -d --name agent --restart always --net host --privileged \
    -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/polipan-agent:/var/lib/polipan-agent \
    -v /var/lib/docker/volumes:/var/lib/docker/volumes -v /var/lib/polipan/backups:/var/lib/polipan/backups \
    -e POLIPAN_AGENT_SERVER="$2" polipan-agent:m7 >/dev/null
}
token_of() { dx $n1 docker exec server /polipan-server join-token --name "$1" --label ingress=true | sed -n 's/^token=//p'; }

step "Node 1 joins (the control plane host)"
agent_up $n1 127.0.0.1:9443 "$(token_of n1)"
dx $n1 docker exec server /polipan-server setup-url > /tmp/pp-m7-setup-url
driver setup /tmp/pp-m7-setup-url

step "AC-NODE-01: node 2 and node 3 join. They must be ready with a mesh address in 60 s"
for pair in "n2:$n2" "n3:$n3"; do
  nname=${pair%%:*}
  cname=${pair##*:}
  tok=$(token_of "$nname")
  t0=$(date +%s)
  agent_up "$cname" "$ip1:9443" "$tok"
  driver wait-ready "$nname" 60
  echo "ok: $nname was ready $(( $(date +%s) - t0 )) s after the install command"
done

step "The scenario: mesh, placement, move, DNS, isolation, drain, failover with and without data, returning node, no reachable node"
driver scenario "$n1" "$n2" "$n3" "$ip1" "$ip2" "$ip3"

echo
echo "E2E M7 PASSED"
