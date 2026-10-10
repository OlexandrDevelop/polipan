-- SPDX-License-Identifier: Apache-2.0
-- M7 migration: many servers. A fixed address for each placement (inside the mesh subnet of its node),
-- the time a node went down, the usage a node reports, and the failover state of a service.

-- The address of the container in the network of the node. The server picks it, so every other node knows it
-- for the internal DNS, the ingress upstreams and the environment firewall (09 sections 7 and 8, REQ-NODE-009).
ALTER TABLE placements ADD COLUMN ip inet;
CREATE UNIQUE INDEX placements_node_ip ON placements (node_id, ip) WHERE ip IS NOT NULL;

-- down_at: when the node state became down (the failover time target counts from here).
-- usage: the last heartbeat numbers {cpu_millis, memory_bytes, disk_bytes}.
-- drain_note: what a drain could not move (REQ-NODE-003).
ALTER TABLE nodes ADD COLUMN down_at timestamptz;
ALTER TABLE nodes ADD COLUMN usage jsonb;
ALTER TABLE nodes ADD COLUMN drain_note text;
-- A deleted node stays as a row (its certificate is revoked and its placements may still wait for a failover).
-- It has no mesh subnet and no key, and its name is changed so the name can be used again.
ALTER TABLE nodes ADD COLUMN deleted_at timestamptz;

-- Failover state of a service (09 section 9). blocked: the node is down and the service was not moved.
ALTER TABLE services ADD COLUMN failover_state text NOT NULL DEFAULT 'healthy' CHECK (failover_state IN ('healthy','blocked','moving','moved'));
ALTER TABLE services ADD COLUMN failover_reason text;
ALTER TABLE services ADD COLUMN failover_data_lost_since timestamptz;
ALTER TABLE services ADD COLUMN failover_at timestamptz;
