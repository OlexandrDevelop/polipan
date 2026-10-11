# Performance and chaos report (M10b-1)

Date: 2026-10-11. Machine: one developer PC (8 cores, 15 GB RAM, NVMe), Docker 27, PostgreSQL 17 in a container,
Go 1.26. These are real numbers from real runs of the suites in this repository. Where the target could not be measured
here, the table says so. Nothing is estimated.

Run the suites again with `make perf` and `make chaos`, and the live runs with `sh test/e2e/run.sh`.

## 1. Performance (targets of [13](../spec/13-testing-and-quality.md#2-test-levels))

| Target | Result | How it was measured | Verdict |
|---|---|---|---|
| 200 services, 20 nodes | 200 services (no resource limits) live on 20 nodes, 10 per node, 0 failed deploys | `TestPerf_ScaleTarget`: real PostgreSQL, real gRPC gateway with mTLS, 20 fake agents that send a heartbeat every second | Met (control plane only, see limits) |
| 50 deploys per hour | First deploy of the 200 services: 28.7 s in waves of 20 = 418 deploys per minute. 50 redeploys: 11.5 s = 261 per minute. | same test | Met: the rate is more than 300 times the target of 0.83 per minute |
| Log search over 1 GB in 3 s | 3.9 million lines, 1024 MB of JSON (189 MB on disk), 773 matches. Search "timeout" in 7 days: 62 ms. One service: 15 ms. Regex in one environment: 75 ms. Newest 100 lines with no text: 209-243 ms. Three runs each. | `TestPerf_LogSearch1GB`: a real VictoriaLogs v1.9.1 and the panel's own `Backends.Search` | Met: the slowest search is 0.24 s |

Limits of these numbers (said plainly):

- The 20 nodes are fake agents. They answer commands at once, so the numbers show the control plane (scheduler,
  database, gateway, state push), not Docker, image pulls or health check waits. A real deploy is dominated by the pull
  and the health check, not by the control plane. This machine has no second server, so the lab setup of 3 VMs or 20 real
  nodes was not built.
- The deploy waves get slower as placements grow, because the desired state of
  a node is rebuilt from its placements. This is fine at 200 services. It is the place to look first for 1000 services.

### Bugs the performance run found

1. **Deadlock of the database pool (fixed).** `createRelease` held a transaction and asked the pool for a second
   connection (`releaseSource`). With more simultaneous deploys than pool connections (the default pool is the number of
   CPU cores) every connection waited for another one and the whole control plane stopped. The first run with 50 deploys at
   once hung for 12 minutes at 0 % CPU. The query now runs before the transaction starts.
2. **Scheduler piled services on one node (F-27, fixed).** Services without `resources` count as zero load, so all
   20 nodes tied and 118 services went to the first node until its address range was full. A tie now goes to the node with
   fewer workloads, scheduler decisions are taken one at a time, and the nodes of a decision are reserved until its
   placements are saved. The same run now puts 10 services on each node. A burst of 200 deploys at once also spreads evenly,
   but the fake agents of the test then lose their streams, so the test runs in waves of 20.

## 2. Chaos (scenarios of [13](../spec/13-testing-and-quality.md#2-test-levels))

`sh test/chaos/run.sh` runs each scenario 3 times in a row. All passed (3 of 3):

| Scenario | Test | What is checked |
|---|---|---|
| Kill the server while switching | `TestChaosKillServerInSwitching`, `TestRecoveryBeforeTheSwitch`, `TestRecoveryRecreateBeforeTheSwitch` | After a restart the deployment ends in a clean state and the old release keeps serving |
| Kill the agent | `TestOfflineRestartRunsPersistedState`, `TestRestartAtEveryPoint`, `TestKillPoints` | The agent restarts from its saved state without the server, and the log shipper loses no batch |
| Drop the network | `TestOfflineBufferThenOnline`, `TestBufferCapDropsOldest`, `TestSwitchNotAckedRollsBack`, `TestPostSwitchCheckRollsBack`, `TestAC_NODE_10_NoFailoverWhenNoNodeIsReachable` | Logs are buffered on disk and sent later. A switch that is not acked is rolled back. A server that can reach no node moves nothing. |
| Fill the disk | `TestChaos_DiskFull` (on a 1 MB tmpfs) | A save fails with an error, the last good state stays readable, no partial file stays, and saving works again when space is free |
| Kill a node while deploying | `TestChaos_KillNodeWhileDeploying` | The deployment ends as `failed: node_unavailable` 4.0 s after the kill with `node_down_after` of 3 s (the default is 60 s). The live release does not change. |
| Node down, failover | `TestAC_NODE_04_07_FailoverStateless`, `TestAC_NODE_05_FailoverBlocked` | The service moves once, never runs twice |

The disk scenario found a real defect: `writeAtomic` left a partial `desired.json.tmp` on a full disk, which kept the disk
full. It is fixed (the temporary file is removed on every error).

### Live run (real Docker, Caddy and ACME)

`sh test/e2e/run.sh` with `OUTAGE_SECONDS=60` passed on this machine (36 checks). It runs in a throw-away Docker host
(docker:dind) with a local ACME CA. Selected results:

- 5 blue-green deploys under 20 requests per second: **5380 requests, none failed**, and the answer changed from the old
  to the new version at one moment, 5 times (AC-DEPLOY-01).
- A failing deploy while 20 requests per second run: **1164 requests, none failed**, blue kept serving (AC-DEPLOY-02).
- An open stream closed **10.0 s** after the switch with `drain_seconds` 10 (AC-DEPLOY-09).
- A fast rollback switched traffic in **5.3 s**, with no build and no image pull (AC-REL-02, target 10 s).
- The server was killed in `switching` and in `health_checking` and restarted (REQ-DEPLOY-107). The server was stopped
  for 60 s and the app kept serving over HTTPS. After the restart the login worked and the node was `ready` (T-E2E-13).
  The done criterion of 5 minutes (`OUTAGE_SECONDS=300`) was not run, to save time.

Not run here (needs more than one machine): kill of a real node with Docker (T-E2E-07, `run-m7.sh`), and the
7 nights in a row of the nightly E2E that done criterion 2 of M10 asks for. These need the CI environment.

`sh test/e2e/run-m8.sh` (logs, metrics, alerts, canary on a real node with VictoriaMetrics and VictoriaLogs) also passed:

- Logs: new lines are searchable within 3 s (AC-OBS-01). Logs of a 50 s gap and of an agent kill are all there, none twice
  (AC-OBS-04). Search for `timeout` in production gives the 6 production lines, newest first (AC-OBS-06). Lines older
  than the retention are not returned (AC-OBS-07).
- Metrics: the last 20 minutes have 81 points 15 s apart (AC-OBS-03). 60 days give 1440 points, under the limit of 1500
  (AC-OBS-05).
