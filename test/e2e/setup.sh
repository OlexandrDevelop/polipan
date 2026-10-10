#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Runs INSIDE the test Docker host (docker:dind). Starts the control plane, a local ACME CA
# (Pebble) and the agent. The images must be loaded already. Used by test/e2e/run.sh.
set -eu

cd /e2e
mkdir -p /etc/polipan /opt/polipan /var/lib/polipan-agent /var/lib/polipan/backups
head -c 32 /dev/urandom | base64 > /etc/polipan/master.key
chown 65532:65532 /etc/polipan/master.key
chmod 0400 /etc/polipan/master.key

docker network create --subnet 172.30.0.0/24 polipan >/dev/null

# Local ACME CA. It resolves test host names through challtestsrv.
docker run -d --name challtest --network polipan --network-alias challtest -p 127.0.0.1:8055:8055 \
  ghcr.io/letsencrypt/pebble-challtestsrv:latest -http01 "" -https01 "" -tlsalpn01 "" -defaultIPv6 "" >/dev/null
docker run -d --name pebble --network polipan --network-alias pebble -p 127.0.0.1:15000:15000 \
  -e PEBBLE_VA_NOSLEEP=1 -v /e2e/pebble.json:/test/config/pebble-config.json \
  ghcr.io/letsencrypt/pebble:latest -config /test/config/pebble-config.json -dnsserver challtest:8053 >/dev/null
docker cp pebble:/test/certs/pebble.minica.pem /e2e/pebble.minica.pem

cat > /opt/polipan/.env <<ENV
POSTGRES_PASSWORD=pw
POLIPAN_SERVER_IMAGE=polipan-server:dev
POLIPAN_EXTERNAL_URL=http://panel.test
POLIPAN_AGENT_HOSTS=localhost,panel.test
ENV
cp /e2e/docker-compose.yml /opt/polipan/docker-compose.yml
cd /opt/polipan
docker compose up -d
i=0
until docker compose exec -T server /polipan-server healthcheck >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { docker compose logs server; exit 1; }
  sleep 2
done

out=$(docker compose exec -T server /polipan-server join-token --name local --label ingress=true)
token=$(printf '%s\n' "$out" | sed -n 's/^token=//p')
fp=$(printf '%s\n' "$out" | sed -n 's/^ca_fingerprint=//p')
echo "$token" > /e2e/join-token
docker compose exec -T server /polipan-server setup-url > /e2e/setup-url

agent_run() {
  docker run "$@" --net host --privileged -v /var/run/docker.sock:/var/run/docker.sock \
    -v /var/lib/polipan-agent:/var/lib/polipan-agent -v /e2e:/e2e \
    -v /var/lib/docker/volumes:/var/lib/docker/volumes -v /var/lib/polipan/backups:/var/lib/polipan/backups \
    -e POLIPAN_AGENT_SERVER=127.0.0.1:9443 -e POLIPAN_AGENT_FIREWALL=false \
    -e POLIPAN_AGENT_ACME_CA_URL=https://pebble:14000/dir \
    -e POLIPAN_AGENT_ACME_TRUSTED_ROOTS=/e2e/pebble.minica.pem \
    polipan-agent:dev
}
docker run --rm --net host -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/polipan-agent:/var/lib/polipan-agent polipan-agent:dev \
  join --server 127.0.0.1:9443 --token "$token" --ca-fingerprint "$fp" --data-dir /var/lib/polipan-agent
# Docker pulls built images from the built-in registry over TLS with the server CA (like install.sh).
mkdir -p /etc/docker/certs.d/172.31.251.10:5000
cp /var/lib/polipan-agent/ca.crt /etc/docker/certs.d/172.31.251.10:5000/ca.crt
agent_run -d --name agent --restart always >/dev/null
echo "setup done"
