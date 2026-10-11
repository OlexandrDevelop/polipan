#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# AC-SEC-06 integration test (REQ-SEC-102, REQ-SEC-108). Needs Docker. Everything runs in a
# throw-away Docker host (docker:dind), so the Docker and the nftables of this machine are not touched.
#
# Inside it builds the networks of deploy/compose/docker-compose.yml (control-plane network
# 172.31.251.0/24, build network 172.31.250.0/24), starts PostgreSQL, the registry, listeners for
# VictoriaLogs and VictoriaMetrics, a mesh address, rootless buildkitd, loads
# deploy/nftables/polipan-build.sh, and builds test/e2e/sec06/Dockerfile with buildctl from the
# server address. The build must print PUBLIC_OK and ALL_BLOCKED.
#
#   sh test/e2e/sec06.sh        KEEP=1 keeps the Docker host, SEC06_HOST=name sets its name
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
host=${SEC06_HOST:-pp-sec06-dind}
dx() { docker exec "$host" "$@"; }
cleanup() { [ -n "${KEEP:-}" ] || docker rm -fv "$host" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker rm -fv "$host" >/dev/null 2>&1 || true
docker run -d --privileged --name "$host" -e DOCKER_TLS_CERTDIR= docker:27-dind >/dev/null
i=0
until dx docker version >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the test Docker host did not start"; exit 1; }
  sleep 1
done
dx apk add --no-cache nftables >/dev/null
dx mkdir -p /sec06
docker cp "$root/test/e2e/sec06/." "$host:/sec06/"
docker cp "$root/deploy/nftables/polipan-build.sh" "$host:/sec06/polipan-build.sh"

echo "== networks and targets"
dx docker network create --subnet 172.31.251.0/24 -o com.docker.network.bridge.name=pp-internal0 internal >/dev/null
dx docker network create --subnet 172.31.250.0/24 -o com.docker.network.bridge.name=pp-build0 polipan-build >/dev/null
dx docker run -d --name postgres --network internal --ip 172.31.251.5 -e POSTGRES_PASSWORD=pw postgres:16-alpine >/dev/null
dx docker run -d --name registry --network internal --ip 172.31.251.10 registry:3 >/dev/null
dx docker run -d --name victorialogs --network internal --ip 172.31.251.20 alpine:3.20 sh -c 'apk add -q netcat-openbsd && nc -lk -p 9428' >/dev/null
dx docker run -d --name victoriametrics --network internal --ip 172.31.251.21 alpine:3.20 sh -c 'apk add -q netcat-openbsd && nc -lk -p 8428' >/dev/null
# A mesh address on the Docker host (like wg0 with mesh_cidr) with a listener.
dx sh -c 'ip link add pp-mesh type dummy && ip addr add 10.210.0.1/24 dev pp-mesh && ip link set pp-mesh up'
dx docker run -d --name mesh --network host alpine:3.20 sh -c 'apk add -q netcat-openbsd && nc -lk -s 10.210.0.1 -p 8080' >/dev/null
# The server on the build network (its API port).
dx docker run -d --name server --network polipan-build --ip 172.31.250.3 alpine:3.20 sh -c 'apk add -q netcat-openbsd && nc -lk -p 8080' >/dev/null
dx docker run -d --name buildkitd --network polipan-build --ip 172.31.250.2 \
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined \
  --cpus 2 --memory 2g moby/buildkit:v0.34.0-rootless --addr tcp://0.0.0.0:1234 >/dev/null
sleep 8

echo "== nftables rules"
dx sh -c 'MESH_CIDR=10.210.0.0/16 sh /sec06/polipan-build.sh'
dx nft list table inet polipan_build | head -40

echo "== build the fixture (client at the server address, like the real server)"
targets="172.31.251.5:5432 172.31.251.20:9428 172.31.251.21:8428 172.31.251.10:5000 10.210.0.1:8080 172.31.250.3:8080 172.31.250.1:22 172.31.251.1:22"
dx docker rm -f server >/dev/null
out=$(dx docker run --rm --network polipan-build --ip 172.31.250.3 -v /sec06:/fx --entrypoint buildctl moby/buildkit:v0.34.0 \
  --addr tcp://172.31.250.2:1234 build --progress plain --no-cache --frontend dockerfile.v0 \
  --local context=/fx --local dockerfile=/fx --opt "build-arg:TARGETS=$targets" 2>&1) || { echo "$out"; echo "FAIL: the build failed (a target was reached or no public egress)"; exit 1; }
echo "$out" | grep -E "PUBLIC_OK|BLOCKED|REACHED|ALL_BLOCKED" || true
echo "$out" | grep -q PUBLIC_OK || { echo "FAIL: no public egress"; exit 1; }
echo "$out" | grep -q ALL_BLOCKED || { echo "FAIL: not all targets were checked"; exit 1; }
echo "AC-SEC-06 PASSED"
