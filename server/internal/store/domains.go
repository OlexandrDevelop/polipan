// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Domain is a domain of a service.
type Domain struct {
	ID             string
	ServiceID      string
	Host           string
	PathPrefix     string
	ContainerPort  int
	HTTPS          bool
	Options        json.RawMessage
	RedirectWWW    *string
	IngressNodeIDs []string
	TLSStatus      string
	TLSExpiresAt   *time.Time
	TLSError       *string
}

const domCols = `d.id, d.service_id, d.host, d.path_prefix, d.container_port, d.https, d.options, d.redirect_www,
	d.ingress_node_ids, d.tls_status, d.tls_expires_at, d.tls_error`
const domFrom = ` FROM domains d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id
	JOIN projects p ON p.id = e.project_id`
const domWhere = ` AND s.deleted_at IS NULL AND e.deleted_at IS NULL AND p.deleted_at IS NULL`

// ListDomains lists the domains of a service of the org.
func (s *Store) ListDomains(ctx context.Context, orgID, serviceID string) ([]Domain, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+domCols+domFrom+` WHERE d.service_id = $1 AND p.org_id = $2`+domWhere+` ORDER BY d.created_at, d.id`, serviceID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.Host, &d.PathPrefix, &d.ContainerPort, &d.HTTPS, &d.Options, &d.RedirectWWW,
			&d.IngressNodeIDs, &d.TLSStatus, &d.TLSExpiresAt, &d.TLSError); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDomain reads a domain of the org. EnvProtected comes from the environment of the service.
func (s *Store) GetDomain(ctx context.Context, orgID, id string) (*Domain, bool, error) {
	var protected bool
	var d Domain
	err := s.Pool.QueryRow(ctx, `SELECT `+domCols+`, e.protected`+domFrom+` WHERE d.id = $1 AND p.org_id = $2`+domWhere, id, orgID).
		Scan(&d.ID, &d.ServiceID, &d.Host, &d.PathPrefix, &d.ContainerPort, &d.HTTPS, &d.Options, &d.RedirectWWW,
			&d.IngressNodeIDs, &d.TLSStatus, &d.TLSExpiresAt, &d.TLSError, &protected)
	if err != nil {
		return nil, false, notFound(err)
	}
	return &d, protected, nil
}

// CreateDomain adds a domain. A used (host, path_prefix) is a unique violation (see IsUnique).
func (s *Store) CreateDomain(ctx context.Context, d Domain) (*Domain, error) {
	d.ID = NewID("dom")
	_, err := s.Pool.Exec(ctx, `INSERT INTO domains (id, service_id, host, path_prefix, container_port, https, options, redirect_www, ingress_node_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		d.ID, d.ServiceID, d.Host, d.PathPrefix, d.ContainerPort, d.HTTPS, string(jsonOrEmpty(d.Options)), d.RedirectWWW, d.IngressNodeIDs)
	if err != nil {
		return nil, err
	}
	out, _, err := s.getDomainByID(ctx, d.ID)
	return out, err
}

func (s *Store) getDomainByID(ctx context.Context, id string) (*Domain, bool, error) {
	var d Domain
	err := s.Pool.QueryRow(ctx, `SELECT `+domCols+` FROM domains d WHERE d.id = $1`, id).
		Scan(&d.ID, &d.ServiceID, &d.Host, &d.PathPrefix, &d.ContainerPort, &d.HTTPS, &d.Options, &d.RedirectWWW,
			&d.IngressNodeIDs, &d.TLSStatus, &d.TLSExpiresAt, &d.TLSError)
	if err != nil {
		return nil, false, notFound(err)
	}
	return &d, false, nil
}

// UpdateDomain stores all fields of d except the id and the service.
func (s *Store) UpdateDomain(ctx context.Context, d Domain) (*Domain, error) {
	_, err := s.Pool.Exec(ctx, `UPDATE domains SET host = $2, path_prefix = $3, container_port = $4, https = $5, options = $6,
		redirect_www = $7, ingress_node_ids = $8, tls_status = $9, tls_expires_at = $10, tls_error = $11 WHERE id = $1`,
		d.ID, d.Host, d.PathPrefix, d.ContainerPort, d.HTTPS, string(jsonOrEmpty(d.Options)), d.RedirectWWW, d.IngressNodeIDs,
		d.TLSStatus, d.TLSExpiresAt, d.TLSError)
	if err != nil {
		return nil, err
	}
	out, _, err := s.getDomainByID(ctx, d.ID)
	return out, err
}

// DeleteDomain removes a domain.
func (s *Store) DeleteDomain(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM domains WHERE id = $1`, id)
	return err
}

// Node is a node for the API (read only).
type Node struct {
	ID              string
	Name            string
	State           string
	Labels          json.RawMessage
	PublicIP        *string
	MeshIP          *string
	OS              *string
	Arch            *string
	DockerVersion   *string
	AgentVersion    *string
	Capacity        json.RawMessage
	LastHeartbeatAt *time.Time
	WorkloadCount   int
	MeshSubnet      *string
	DrainNote       *string
	Usage           json.RawMessage
}

const nodeCols = `n.id, n.name, n.state, n.labels, host(n.public_ip), host(n.mesh_ip), n.os, n.arch, n.docker_version, n.agent_version,
	n.capacity, n.last_heartbeat_at,
	(SELECT count(*) FROM placements pl WHERE pl.node_id = n.id AND pl.role IN ('live','candidate','draining')),
	host(network(n.mesh_subnet)) || '/' || masklen(n.mesh_subnet), n.drain_note, n.usage`

func scanNode(row pgx.Row) (*Node, error) {
	var n Node
	if err := row.Scan(&n.ID, &n.Name, &n.State, &n.Labels, &n.PublicIP, &n.MeshIP, &n.OS, &n.Arch, &n.DockerVersion, &n.AgentVersion,
		&n.Capacity, &n.LastHeartbeatAt, &n.WorkloadCount, &n.MeshSubnet, &n.DrainNote, &n.Usage); err != nil {
		return nil, notFound(err)
	}
	return &n, nil
}

// ListNodes lists the nodes of the org.
func (s *Store) ListNodes(ctx context.Context, orgID string) ([]Node, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+nodeCols+` FROM nodes n WHERE n.org_id = $1 AND n.deleted_at IS NULL ORDER BY n.created_at, n.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.State, &n.Labels, &n.PublicIP, &n.MeshIP, &n.OS, &n.Arch, &n.DockerVersion, &n.AgentVersion,
			&n.Capacity, &n.LastHeartbeatAt, &n.WorkloadCount, &n.MeshSubnet, &n.DrainNote, &n.Usage); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNode reads a node of the org.
func (s *Store) GetNode(ctx context.Context, orgID, id string) (*Node, error) {
	return scanNode(s.Pool.QueryRow(ctx, `SELECT `+nodeCols+` FROM nodes n WHERE n.id = $1 AND n.org_id = $2 AND n.deleted_at IS NULL`, id, orgID))
}

// NodeNameUsed tells if a node of the org has the name.
func (s *Store) NodeNameUsed(ctx context.Context, orgID, name string) (bool, error) {
	var used bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE org_id = $1 AND name = $2)`, orgID, name).Scan(&used)
	return used, err
}

// ReadyNodes returns the id and the public IP (may be empty) of the nodes in state ready.
func (s *Store) ReadyNodes(ctx context.Context, orgID string) (ids, publicIPs []string, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, COALESCE(host(public_ip), '') FROM nodes WHERE org_id = $1 AND state = 'ready' ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ip string
		if err := rows.Scan(&id, &ip); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		if ip != "" {
			publicIPs = append(publicIPs, ip)
		}
	}
	return ids, publicIPs, rows.Err()
}
