// SPDX-License-Identifier: Apache-2.0

package join

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// Options are the inputs of Run.
type Options struct {
	Server        string // host:port
	Token         string
	CAFingerprint string // sha256 hex of the CA certificate DER
	DataDir       string
	Info          *agentv1.NodeInfo
}

// PinnedTLS returns a TLS config that trusts a server whose chain has a CA with the given fingerprint.
func PinnedTLS(host, fingerprint string) *tls.Config {
	want := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fingerprint), ":", ""))
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // the chain is checked below against the pinned CA
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("join: server sent no certificate")
			}
			certs := make([]*x509.Certificate, 0, len(raw))
			for _, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				certs = append(certs, c)
			}
			pool := x509.NewCertPool()
			found := false
			for _, c := range certs {
				sum := sha256.Sum256(c.Raw)
				if c.IsCA && hex.EncodeToString(sum[:]) == want {
					pool.AddCert(c)
					found = true
				}
			}
			if !found {
				return errors.New("join: CA fingerprint does not match the server certificate chain")
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			_, err := certs[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter, DNSName: host})
			return err
		},
	}
}

// NewCSR creates a CSR for key.
func NewCSR(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "polipan-agent"},
	}, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// Run joins the server and stores the identity in DataDir. node.json is written last.
func Run(ctx context.Context, o Options) (*NodeFile, error) {
	if o.Server == "" || o.Token == "" || o.CAFingerprint == "" {
		return nil, errors.New("join: server, token and ca fingerprint are required")
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(o.Server)
	if err != nil {
		return nil, fmt.Errorf("join: bad server address: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := NewCSR(key)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(o.Server, grpc.WithTransportCredentials(credentials.NewTLS(PinnedTLS(host, o.CAFingerprint))))
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := agentv1.NewAgentJoinClient(conn).Join(cctx, &agentv1.JoinRequest{
		JoinToken: o.Token, CsrPem: csr, NodeInfo: o.Info,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, fmt.Errorf("join refused: %s", s.Message())
		}
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	nf := &NodeFile{NodeID: resp.NodeId, MeshIP: resp.MeshIp, MeshSubnet: resp.MeshSubnet}
	nb, _ := json.Marshal(nf)
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{fileTLSKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o400},
		{fileCert, resp.ClientCertPem, 0o600},
		{fileCA, resp.CaCertPem, 0o600},
		{fileNode, nb, 0o600}, // last: marks a finished join
	}
	for _, f := range files {
		p := filepath.Join(o.DataDir, f.name)
		_ = os.Remove(p)
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			return nil, err
		}
	}
	return nf, nil
}
