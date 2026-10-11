#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Chaos suite (docs/spec/13-testing-and-quality.md section 2): the scenarios kill the server, kill an agent, drop the
# network, fill the disk and kill a node while it deploys. Each scenario is a Go test with real PostgreSQL and a real gRPC
# gateway. Run from the repository root. It needs POLIPAN_TEST_DATABASE_URL.
#
#   sh test/chaos/run.sh            run every scenario 3 times
#   COUNT=1 sh test/chaos/run.sh    run once
#
# The disk full scenario needs a small file system. Set POLIPAN_CHAOS_SMALLFS to a folder on one (for example a tmpfs
# of 1 MB: docker run --tmpfs /smallfs:size=1m ...). Without it that scenario is skipped and the script says so.
# The live scenarios (control plane down for minutes, kill of a node with Docker) are in test/e2e: run.sh (T-E2E-13) and
# run-m7.sh (T-E2E-07, T-E2E-08).
set -eu

count=${COUNT:-3}
fail=0
run() { # NAME PACKAGE REGEX
  printf '%-34s ' "$1"
  if out=$(go test "$2" -run "$3" -count="$count" 2>&1); then
    if printf '%s' "$out" | grep -q 'no tests to run'; then echo "NOTHING RAN"; fail=1; else echo "ok (x$count)"; fi
  else
    echo "FAIL"; printf '%s\n' "$out" | tail -20; fail=1
  fi
}

run "kill server (switching)"      ./server/internal/control 'TestChaosKillServerInSwitching|TestRecoveryBeforeTheSwitch|TestRecoveryRecreateBeforeTheSwitch'
run "kill agent, restart offline"  ./agent/internal/reconcile 'TestOfflineRestartRunsPersistedState'
run "kill log shipper"             ./agent/internal/logship 'TestRestartAtEveryPoint|TestKillPoints'
run "drop network, log buffer"     ./agent/internal/logship 'TestOfflineBufferThenOnline|TestBufferCapDropsOldest'
run "switch not acked, rollback"   ./server/internal/control 'TestSwitchNotAckedRollsBack|TestPostSwitchCheckRollsBack'
run "node down, failover"          ./server/internal/control 'TestAC_NODE_04_07_FailoverStateless|TestAC_NODE_10_NoFailoverWhenNoNodeIsReachable|TestAC_NODE_05_FailoverBlocked'
run "kill node while deploying"    ./server/internal/control 'TestChaos_KillNodeWhileDeploying'
if [ -n "${POLIPAN_CHAOS_SMALLFS:-}" ]; then
  run "fill disk (agent state)"    ./agent/internal/state 'TestChaos_DiskFull'
else
  echo "fill disk (agent state)            SKIPPED: set POLIPAN_CHAOS_SMALLFS to a folder on a small file system"
fi
exit $fail
