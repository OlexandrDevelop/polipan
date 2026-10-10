#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# End-to-end test of milestone M4 (T-E2E-09 without alert delivery, AC-DB-01..04, AC-TPL-01, AC-BKP-01, 03, 04, 06, 07, 08, 09,
# AC-OSS-05). It runs a throw-away Docker host (docker:dind) with the control plane, one agent, a MinIO server and an SFTP
# server. Postgres, MySQL and WordPress images are pulled inside the test Docker host and removed with it.
#
#   sh test/e2e/run-m4.sh            run the test and clean up
#   KEEP=1 sh ...                    keep the test Docker host after the run
#   SKIP_BUILD=1 sh ...              use the polipan-server:dev and polipan-agent:dev images that exist
#   SKIP_TEMPLATE=1 sh ...           skip the WordPress template step (it pulls about 1.5 GB)
#
# Disk: the run needs about 5 GB. It stops when less than 5 GB is free.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
host=${E2E_HOST:-pp-e2e-m4}
dx() { docker exec "$host" "$@"; }
step() { printf '\n== %s\n' "$*"; }
free_gb() { df -BG --output=avail / | tail -1 | tr -dc '0-9'; }
compose="docker compose -f /opt/polipan/docker-compose.yml"

start_free=$(free_gb)
[ "$start_free" -ge 5 ] || { echo "only $start_free GB free: stop"; exit 1; }

cleanup() {
  [ -n "${KEEP:-}" ] || docker rm -fv "$host" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  step "Build the server and agent images"
  v=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
  docker build -q -f deploy/docker/server.Dockerfile --build-arg VERSION="$v" -t polipan-server:dev . >/dev/null
  docker build -q -f deploy/docker/agent.Dockerfile --build-arg VERSION="$v" -t polipan-agent:dev . >/dev/null
fi

step "Start the test Docker host"
docker rm -fv "$host" >/dev/null 2>&1 || true
docker run -d --privileged --name "$host" -e DOCKER_TLS_CERTDIR= docker:27-dind >/dev/null
i=0
until dx docker version >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the test Docker host did not start"; exit 1; }
  sleep 1
done
for img in polipan-server:dev polipan-agent:dev python:3.12-slim postgres:16-alpine alpine:latest; do
  docker pull -q "$img" >/dev/null 2>&1 || true
  docker save "$img" | docker exec -i "$host" docker load >/dev/null
done
dx sh -c 'for i in caddy:2.10.2 nginx:alpine ghcr.io/letsencrypt/pebble:latest ghcr.io/letsencrypt/pebble-challtestsrv:latest chainguard/minio:latest postgres:16; do docker pull -q $i >/dev/null; done'
dx mkdir -p /e2e/sftp
docker cp test/e2e/. "$host:/e2e/"
docker cp deploy/compose/docker-compose.yml "$host:/e2e/docker-compose.yml"

step "Install the control plane and the agent"
dx sh /e2e/setup.sh

step "Start MinIO and an SFTP server"
# The MinIO images moved away from Docker Hub and Quay is not always reachable: Chainguard builds MinIO from source.
dx mkdir -p /minio-data && dx chmod 777 /minio-data
dx docker run -d --name minio --net host -v /minio-data:/data -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio-secret-123 \
  chainguard/minio:latest server /data --address 127.0.0.1:9000 --console-address 127.0.0.1:9001 >/dev/null
dx docker run -d --name sftp --net host -v /e2e/sftp:/keys alpine:latest sh -c '
  apk add --no-cache openssh >/dev/null &&
  ssh-keygen -q -t ed25519 -N "" -f /keys/id_ed25519 &&
  adduser -D -s /bin/sh bk && sed -i "s/^bk:!/bk:*/" /etc/shadow &&
  mkdir -p /home/bk/.ssh /srv/restic && cp /keys/id_ed25519.pub /home/bk/.ssh/authorized_keys &&
  chown -R bk:bk /home/bk /srv/restic && chmod 700 /home/bk/.ssh && chmod 600 /home/bk/.ssh/authorized_keys &&
  ssh-keygen -A >/dev/null && exec /usr/sbin/sshd -D -e -p 2222 -o PasswordAuthentication=no -o Subsystem="sftp internal-sftp"' >/dev/null
i=0
until dx test -s /e2e/sftp/id_ed25519 && dx docker logs sftp 2>&1 | grep -q "Server listening"; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { dx docker logs sftp; exit 1; }
  sleep 1
done
i=0
until dx wget -q -O /dev/null http://127.0.0.1:9000/minio/health/live; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { dx docker logs minio; exit 1; }
  sleep 1
done

driver() { dx docker run --rm --net host -v /e2e:/e2e python:3.12-slim python -I /e2e/driver.py "$@"; }
st() { dx docker run --rm -v /e2e:/e2e python:3.12-slim python -I -c "import json;print(json.load(open('/e2e/m4.json'))['$1'])"; }
pg() { dx $compose exec -T postgres psql -tA -U polipan -d polipan -c "$1"; }
ctr() { dx docker ps -q --filter "label=polipan.service=$1" | head -1; }
dbexec() { dx docker exec "$(ctr "$1")" sh -c "PGPASSWORD=\$DB_PASSWORD psql -U \$DB_USER -d \$DB_NAME -tA -c \"$2\""; }

step "AC-DB-01..04 and the two targets"
driver m4-setup
api=$(st api)
url=$(dx docker exec "$(ctr "$api")" printenv DATABASE_URL)
dx docker run --rm --network polipan postgres:16-alpine psql "$url" -tA -c 'select 1' | grep -q '^1$'
echo 'ok: the reference ${{db.main.URL}} gives a working connection from the other service'
dx docker run --rm --network polipan postgres:16-alpine psql "$url" -q -c "create table items (id int primary key, name text); insert into items select g, 'row ' || g from generate_series(1, 500) g"
db=$(st db)

step "AC-BKP-01, AC-BKP-03, AC-BKP-07 (two targets, manual run, automatic restore check)"
driver m4-backup
policy=$(st policy)
left=$(dx docker ps -a -q --filter label=polipan.role=restore-check | wc -l)
vols=$(dx docker volume ls -q --filter label=polipan.role=restore-check | wc -l)
[ "$left" = 0 ] && [ "$vols" = 0 ] || { echo "FAIL: the restore check left $left containers and $vols volumes"; exit 1; }
echo "ok: the temporary container and its volume of the check are gone"

step "AC-OSS-05: plain restic restores the repositories (MinIO and SFTP) with the password from the admin command"
pw=$(dx $compose exec -T server /polipan-server admin backup-password --policy "$policy" | sed -n 's/^repository password: //p')
[ -n "$pw" ]
dx docker exec -e RESTIC_PASSWORD="$pw" -e AWS_ACCESS_KEY_ID=minio -e AWS_SECRET_ACCESS_KEY=minio-secret-123 agent \
  restic -r "s3:http://127.0.0.1:9000/backups/pp/$policy" dump latest /dump.pgdump \
  | dx docker run --rm -i postgres:16-alpine pg_restore -l | grep -q "items"
echo "ok: the MinIO repository restores with the restic binary and holds the table"
dx docker exec -e RESTIC_PASSWORD="$pw" agent restic -r "sftp:bk@127.0.0.1:/srv/restic/$policy" \
  -o sftp.command="ssh -p 2222 -i /e2e/sftp/id_ed25519 -o StrictHostKeyChecking=no bk@127.0.0.1 -s sftp" dump latest /dump.pgdump \
  | dx docker run --rm -i postgres:16-alpine pg_restore -l | grep -q "items"
echo "ok: the SFTP repository restores with the restic binary and holds the table"

step "AC-BKP-04: restore into a new service, the original stays"
driver m4-restore
copy=$(st copy)
n=$(dbexec "$copy" "select count(*) from items")
[ "$n" = 500 ] || { echo "FAIL: the new service has $n rows"; exit 1; }
[ "$(dbexec "$db" "select count(*) from items")" = 500 ]
echo "ok: the new service has 500 rows, the original has 500 rows"

step "Restore in place from the SFTP target (safety backup first)"
dbexec "$db" "delete from items where id > 100" >/dev/null
[ "$(dbexec "$db" "select count(*) from items")" = 100 ]
driver m4-restore-inplace
n=$(dbexec "$db" "select count(*) from items")
[ "$n" = 500 ] || { echo "FAIL: after the restore in place there are $n rows"; exit 1; }
echo "ok: 500 rows again after the restore in place"

step "AC-BKP-08: corrupt the snapshot on the SFTP target, the check fails and a critical alert is raised"
dx docker exec sftp sh -c "for f in \$(find /srv/restic/$policy/data -type f); do echo corrupted > \$f; done"
driver m4-check-corrupt
a=$(pg "select count(*) from events where topic = 'alert' and payload->>'rule' = 'restore_check_failed' and payload->>'severity' = 'critical'")
[ "$a" -ge 1 ] || { echo "FAIL: no critical alert"; exit 1; }
echo "ok: $a critical alert event(s) for restore_check_failed (delivery to channels is M8)"
left=$(dx docker ps -a -q --filter label=polipan.role=restore-check | wc -l)
[ "$left" = 0 ] || { echo "FAIL: a failed check left a container"; exit 1; }

step "AC-BKP-09: a volume policy, file count and size within 1 %"
driver m4-volume
files=$(st files)
dx docker exec "$(ctr "$files")" sh -c 'mkdir -p /data/d; i=0; while [ $i -lt 200 ]; do echo "file $i" > /data/d/f$i; i=$((i+1)); done'
driver m4-volume-backup

step "AC-BKP-06: the control-plane database policy"
driver m4-controlplane
cp=$(st cp)
pw=$(dx $compose exec -T server /polipan-server admin backup-password --policy "$cp" | sed -n 's/^repository password: //p')
dx docker exec -e RESTIC_PASSWORD="$pw" agent sh -c "restic -r /var/lib/polipan/backups/control-plane/$cp dump latest /dump.pgdump > /tmp/cp.dump && ls -l /tmp/cp.dump"
key=$(dx cat /etc/polipan/master.key)
if dx docker exec agent grep -c -F "$key" /tmp/cp.dump >/dev/null 2>&1; then echo "FAIL: the master key is in the dump"; exit 1; fi
dx docker exec agent sh -c 'test -s /tmp/cp.dump'
echo "ok: the control-plane dump holds no master key"

step "Redis: dump, restore check and restore in place (the agent stops and starts the container)"
driver m4-redis-start
cache=$(st cache)
rexec() { dx docker exec "$(ctr "$cache")" sh -c "$1"; }
rexec 'i=1; while [ $i -le 100 ]; do echo "SET k$i v$i"; i=$((i+1)); done | REDISCLI_AUTH=$DB_PASSWORD redis-cli --pipe' >/dev/null
[ "$(rexec 'REDISCLI_AUTH=$DB_PASSWORD redis-cli dbsize')" = 100 ]
driver m4-redis-backup
rexec 'REDISCLI_AUTH=$DB_PASSWORD redis-cli flushall' >/dev/null
[ "$(rexec 'REDISCLI_AUTH=$DB_PASSWORD redis-cli dbsize')" = 0 ]
driver m4-redis-restore
i=0
until [ "$(rexec 'REDISCLI_AUTH=$DB_PASSWORD redis-cli dbsize' 2>/dev/null || true)" = 100 ]; do
  i=$((i + 1)); [ "$i" -lt 30 ] || { echo "FAIL: redis has not 100 keys after the restore"; exit 1; }
  sleep 2
done
echo "ok: redis has its 100 keys again after the restore in place"

if [ -z "${SKIP_TEMPLATE:-}" ]; then
  step "AC-TPL-01: WordPress with MySQL goes live"
  free=$(free_gb)
  [ "$free" -ge 5 ] || { echo "only $free GB free: stop before the big images"; exit 1; }
  driver m4-template
fi

step "Leftovers"
test "$(dx docker ps -a -q --filter label=polipan.role=restore-check | wc -l)" = 0
echo "ok: no restore check container is left"
echo
echo "M4 E2E PASSED"
