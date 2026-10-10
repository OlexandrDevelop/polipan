// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/polipan/polipan/proto/agentv1"
)

// ErrInvalidToken text, AC-NODE-02.
const invalidTokenMsg = "join token is invalid, used or expired"

// HashToken is the stored form of a join token: sha256, hex.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type joinServer struct {
	pb.UnimplementedAgentJoinServer
	g *Gateway
}

// Join checks the token (single use, TTL), creates the node (state pending), assigns the mesh subnet
// and signs the CSR. All in one transaction: a bad CSR does not burn the token.
func (s *joinServer) Join(ctx context.Context, req *pb.JoinRequest) (*pb.JoinResponse, error) {
	g := s.g
	if req.GetJoinToken() == "" || len(req.GetCsrPem()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "join_token and csr_pem are required")
	}
	tx, err := g.DB.Begin(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "database error")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var orgID, nodeName string
	var labelsRaw []byte
	err = tx.QueryRow(ctx,
		`UPDATE node_join_tokens SET used_at = $2
		  WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING org_id, node_name, labels`,
		HashToken(req.GetJoinToken()), g.Clock.Now()).Scan(&orgID, &nodeName, &labelsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.PermissionDenied, invalidTokenMsg)
	}
	if err != nil {
		g.Log.Error("join: claim token", "err", err)
		return nil, status.Error(codes.Internal, "database error")
	}

	// One join at a time, so two nodes never get the same subnet.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(726001)`); err != nil {
		return nil, status.Error(codes.Internal, "database error")
	}
	subnet, meshIP, err := nextSubnet(ctx, tx, g.Config.MeshCIDR)
	if err != nil {
		if errors.Is(err, errNoSubnet) {
			return nil, status.Error(codes.ResourceExhausted, "no free mesh subnet")
		}
		g.Log.Error("join: subnet", "err", err)
		return nil, status.Error(codes.Internal, "mesh subnet error")
	}

	nodeID := NewID("nod")
	cert, err := g.CA.SignAgentCert(req.GetCsrPem(), nodeID, g.Config.AgentCertValidity)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	info := req.GetNodeInfo()
	var publicIP *string
	if ip := net.ParseIP(info.GetPublicIp()); ip != nil {
		v := ip.String()
		publicIP = &v
	}
	capacity, _ := json.Marshal(map[string]uint64{
		"cpu_millis": info.GetCpuMillis(), "memory_bytes": info.GetMemoryBytes(), "disk_bytes": info.GetDiskBytes(),
	})
	var wg *string
	if k := req.GetWireguardPublicKey(); k != "" {
		wg = &k
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO nodes (id, org_id, name, state, labels, public_ip, mesh_ip, mesh_subnet, wireguard_public_key,
		                    os, arch, docker_version, agent_version, capacity, cert_serial, cert_expires_at,
		                    desired_state_version)
		 VALUES ($1,$2,$3,'pending',$4,$5::inet,$6::inet,$7::cidr,$8,$9,$10,$11,$12,$13,$14,$15,1)`,
		nodeID, orgID, nodeName, labelsRaw, publicIP, meshIP, subnet, wg,
		info.GetOs(), info.GetArch(), info.GetDockerVersion(), info.GetAgentVersion(), capacity,
		cert.Serial, cert.NotAfter)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, status.Error(codes.AlreadyExists, "a node with this name exists")
		}
		g.Log.Error("join: insert node", "err", err)
		return nil, status.Error(codes.Internal, "database error")
	}
	payload, _ := json.Marshal(map[string]string{"kind": "joined", "name": nodeName, "state": "pending"})
	if _, err := tx.Exec(ctx,
		`INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'node',$2,$3)`, orgID, nodeID, payload); err != nil {
		return nil, status.Error(codes.Internal, "database error")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, status.Error(codes.Internal, "database error")
	}
	g.Log.Info("node joined", "node", nodeID, "name", nodeName)
	return &pb.JoinResponse{
		NodeId:        nodeID,
		ClientCertPem: cert.PEM,
		CaCertPem:     g.CA.CertPEM(),
		MeshIp:        meshIP,
		MeshSubnet:    subnet,
	}, nil
}

var errNoSubnet = errors.New("no free subnet")

// nextSubnet returns the first free /24 of the pool (the first one is skipped) and its .1 address.
func nextSubnet(ctx context.Context, tx pgx.Tx, cidr string) (subnet, ip string, err error) {
	_, pool, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("mesh_cidr: %w", err)
	}
	base := pool.IP.To4()
	ones, bits := pool.Mask.Size()
	if base == nil || bits != 32 || ones > 24 {
		return "", "", errors.New("mesh_cidr must be an IPv4 range of /24 or larger")
	}
	rows, err := tx.Query(ctx, `SELECT host(network(mesh_subnet)) FROM nodes WHERE mesh_subnet IS NOT NULL`)
	if err != nil {
		return "", "", err
	}
	used := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return "", "", err
		}
		used[h] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	start := binary.BigEndian.Uint32(base)
	count := uint32(1) << (24 - ones)
	for i := uint32(1); i < count; i++ {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], start+i<<8)
		n := net.IP(b[:]).String()
		if used[n] {
			continue
		}
		b[3] = 1
		return n + "/24", net.IP(b[:]).String(), nil
	}
	return "", "", errNoSubnet
}
