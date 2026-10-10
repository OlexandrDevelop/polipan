// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
)

// PeerKeepalive is the WireGuard keepalive in seconds. It keeps a path open through NAT.
const PeerKeepalive = 25

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// MeshPeers lists the WireGuard peers of a node: every other node of the org that has a public key and a mesh
// subnet (REQ-AGENT-008, the mesh is complete). The endpoint is the public IP and the WireGuard port, empty when
// the node reported no public IP (the other side then waits for the first packet).
func MeshPeers(ctx context.Context, q querier, orgID, nodeID string, port int) ([]*pb.WireguardPeer, error) {
	rows, err := q.Query(ctx,
		`SELECT id, wireguard_public_key, COALESCE(host(public_ip), ''), mesh_subnet::text FROM nodes
		  WHERE org_id = $1 AND id <> $2 AND deleted_at IS NULL AND wireguard_public_key IS NOT NULL AND mesh_subnet IS NOT NULL
		  ORDER BY id`, orgID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.WireguardPeer
	for rows.Next() {
		var id, key, ip, subnet string
		if err := rows.Scan(&id, &key, &ip, &subnet); err != nil {
			return nil, err
		}
		p := &pb.WireguardPeer{NodeId: id, PublicKey: key, AllowedIps: subnet, KeepaliveSeconds: PeerKeepalive}
		if ip != "" {
			p.Endpoint = fmt.Sprintf("%s:%d", ip, port)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
