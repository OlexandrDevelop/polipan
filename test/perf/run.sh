#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Performance suite (docs/spec/13-testing-and-quality.md section 2). Run from the repository root. It needs Go,
# Docker and POLIPAN_TEST_DATABASE_URL.
#
#   1. Scale: 200 services on 20 fake nodes (real gRPC gateway and PostgreSQL), 200 first deploys and 50 redeploys.
#      The target is 50 deploys per hour. The test fails when a deploy does not go live or one deploy takes longer than 72 s.
#   2. Log search: about 1 GB of JSON log lines in a throw-away VictoriaLogs, four searches, three runs each. The target is 3 s.
#
# POLIPAN_PERF_MB changes the size of the log data (default 1024). Everything the script starts is removed at the end,
# and it prints the free disk space before and after.
set -eu

free() { df -h . | awk 'NR==2 {print $4}'; }
echo "free disk before: $(free)"

POLIPAN_PERF=1 go test ./server/internal/control -run TestPerf_ScaleTarget -v -count=1 -timeout 15m | grep -E 'PERF|^(---|ok|FAIL)'

name=pp-perf-vl-$$
trap 'docker rm -fv "$name" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$name" -p 127.0.0.1:19428:9428 victoriametrics/victoria-logs:v1.9.1-victorialogs -retentionPeriod=30d >/dev/null
i=0
until curl -fs http://127.0.0.1:19428/health >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 30 ] || { echo "VictoriaLogs did not start"; exit 1; }
  sleep 1
done
POLIPAN_PERF_VL_URL=http://127.0.0.1:19428 go test ./server/internal/obs -run TestPerf_LogSearch1GB -v -count=1 -timeout 15m | grep -E 'PERF|^(---|ok|FAIL)'
docker rm -fv "$name" >/dev/null
trap - EXIT
echo "free disk after: $(free)"
