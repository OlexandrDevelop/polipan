# F06 – Many servers in one panel, failover and move — Gap 6

## Summary

A user manages many servers from one panel. Services are placed on nodes. A user can move a service to another node. If a node fails, services with `failover: auto` move to healthy nodes. The protocol and algorithms are in [09-multi-server](../09-multi-server.md).

## User stories

- **US-NODE-01** As an admin, I add a server with one command and see it in the panel.
- **US-NODE-02** As a developer, I pick which node (or label) my service runs on.
- **US-NODE-03** As an admin, I drain a node for maintenance and services move away without downtime.
- **US-NODE-04** As an admin, I move a service to another server with one click.
- **US-NODE-05** As an operator, when a server dies my stateless services come back on other servers automatically.
- **US-NODE-06** As an operator, I decide per service whether automatic failover may lose data.

## Rules

- **REQ-NODE-001** (MUST) Join: `polipan node add` or the UI creates a one-use token (`join_token_ttl`) and a one-line install command. The node appears as `pending`, then `ready` ([09](../09-multi-server.md#2-join-flow)).
- **REQ-NODE-002** (MUST) A node has: name, labels (key=value), state, capacity, usage, agent and Docker versions, mesh IP, public IP, last heartbeat. Special label `ingress=true` makes it an ingress node. Label `preview=true` marks nodes for previews.
- **REQ-NODE-003** (MUST) Node actions: `cordon`, `uncordon`, `drain`, `maintenance`, `delete`. `drain` moves every placement away with the blue-green flow and finishes when the node has no workloads. A `drain` that cannot place something reports what is left and stays `draining`.
- **REQ-NODE-004** (MUST) Service placement settings: `nodes` (names, optional), `labels` (required labels), `spread` (bool), `replicas`. The scheduler of [09](../09-multi-server.md#7-placement-and-scheduling) is used.
- **REQ-NODE-005** (MUST) Node state follows heartbeat timings (`ready → suspect → down`) and raises alerts `node_suspect`, `node_down`.
- **REQ-NODE-006** (MUST) Move: `POST /services/{id}/move`. Stateless services use blue-green on the target node (no downtime). Services with a local volume use the data-copy path with downtime and a visible warning.
- **REQ-NODE-007** (MUST) Failover policy per service: `failover: none | auto`, `allow_data_loss_failover` (bool, default false). Rules in [09](../09-multi-server.md#9-failover). The UI shows the policy on the service page and the time since the last backup when a volume exists.
- **REQ-NODE-008** (MUST) Deleting a node with workloads is refused (409 `node_not_empty`) unless `force: true`, which marks its placements as lost (then failover rules apply to services with `auto`, others stay `stopped` with a visible reason).
- **REQ-NODE-009** (MUST) Services reach each other across nodes by the internal DNS name over the WireGuard mesh ([02](../02-architecture.md#6-agent-internals)).
- **REQ-NODE-010** (MUST) Every ingress node can route to any service on any node. DNS records are the user's job, and the UI shows the list of ingress IPs.
- **REQ-NODE-011** (SHOULD) The node page shows containers, CPU/RAM/disk charts, agent logs and a "run self-check" button.
- **REQ-NODE-012** (MUST) A node that lost connection and returns re-syncs by reading desired state first ([REQ-AGENT-009](../09-multi-server.md#requirements)). Containers that moved away are removed from it.

## States

Node states: see [09](../09-multi-server.md#6-node-states). Failover state per service: see [09](../09-multi-server.md#9-failover).

## Edge cases

- Server cannot reach any node (its own network is down): no failover happens ([09](../09-multi-server.md#9-failover) rule 7).
- Two nodes down at once: failover goes in order of service creation, limited by capacity. Those that cannot be placed stay `stopped` and raise an alert.
- Node is only slow (suspect), then returns: nothing moves.
- Control plane host node goes down: the control plane itself is down (no HA in v1). Other nodes keep running services ([REQ-CORE-007](../02-architecture.md#requirements)).
- Clock drift on a node: certificate checks need correct time. The agent reports skew in the heartbeat. A skew over 60 s shows a warning and over 5 min stops new deployments to that node.
- Different CPU arch in one service with replicas: image must be multi-arch, else the scheduler restricts to nodes of the built arch.
- Replica count 3 on 2 nodes with `spread`: 2+1 and a warning.

## Touch points

API: `/nodes`, `/nodes/{id}`, `/nodes/{id}/cordon|uncordon|drain|maintenance`, `/services/{id}/move`. CLI: `polipan node add|ls|drain|rm`, `polipan service move`. Config: `services.<name>.placement`, `.failover`. UI: Nodes page.

## Acceptance criteria

- **AC-NODE-01** Given a fresh server, When the install command runs with a valid token, Then within 60 s the node is `ready` and has a mesh IP. (REQ-NODE-001)
- **AC-NODE-02** Given a used token, When reused, Then the join fails. Given an expired token, Then the join fails. (REQ-AGENT-002)
- **AC-NODE-03** Given a stateless service on node A, When the user moves it to B, Then no request fails during the move and the container runs on B. (REQ-NODE-006)
- **AC-NODE-04** Given a stateless service with `failover: auto` on node A, When A stops sending heartbeats, Then within `node_down_after` + 60 s the service runs on another node and an audit event and an alert exist. (REQ-NODE-005, REQ-NODE-007)
- **AC-NODE-05** Given a service with a local volume and `allow_data_loss_failover: false`, When its node goes down, Then it is not moved and an alert says why. (REQ-NODE-007)
- **AC-NODE-06** Given a service with a local volume, a successful backup and `allow_data_loss_failover: true`, When the node goes down, Then the service starts on another node with the restored snapshot and the warning about lost data is recorded. (REQ-NODE-007)
- **AC-NODE-07** Given node A returns after failover, When it reconnects, Then the moved service's old container is removed from A and not started. (REQ-NODE-012)
- **AC-NODE-08** Given a node with workloads, When `drain` runs, Then all placements move away and the node ends in `maintenance`. (REQ-NODE-003)
- **AC-NODE-09** Given a service on node A and a client on service B on node C, When B calls the internal DNS name of A, Then it connects over the mesh. (REQ-NODE-009)
- **AC-NODE-10** Given the server cannot reach any node, When nodes look `down`, Then no failover starts. (REQ-NODE-007)

## Out of scope

Automatic scaling (adding servers from a cloud API). Multi-region active-active databases. Control-plane HA.
