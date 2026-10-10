// SPDX-License-Identifier: Apache-2.0

// Package join registers the agent with the server and keeps the node identity files.
package join

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// File names in the data dir. node.json is written last and marks a finished join.
// node.key is NOT here: it is the local AES key of the state package. The TLS key is tls.key.
const (
	fileTLSKey = "tls.key"
	fileCert   = "client.crt"
	fileCA     = "ca.crt"
	fileNode   = "node.json"
)

// NodeFile is the content of node.json.
type NodeFile struct {
	NodeID     string `json:"node_id"`
	MeshIP     string `json:"mesh_ip"`
	MeshSubnet string `json:"mesh_subnet"`
}

// Identity is what the agent needs to open the mTLS stream.
type Identity struct {
	Node   NodeFile
	Key    *ecdsa.PrivateKey
	KeyPEM []byte
	Cert   tls.Certificate
	CAPool *x509.CertPool
}

// Exists reports whether a join was finished in dir.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, fileNode))
	return err == nil
}

// Load reads the identity from dir.
func Load(dir string) (*Identity, error) {
	nb, err := os.ReadFile(filepath.Join(dir, fileNode))
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(nb, &id.Node); err != nil {
		return nil, err
	}
	if id.KeyPEM, err = os.ReadFile(filepath.Join(dir, fileTLSKey)); err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, fileCert))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, fileCA))
	if err != nil {
		return nil, err
	}
	if id.Cert, err = tls.X509KeyPair(certPEM, id.KeyPEM); err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(id.KeyPEM)
	if blk == nil {
		return nil, errors.New("join: bad tls.key")
	}
	if id.Key, err = x509.ParseECPrivateKey(blk.Bytes); err != nil {
		return nil, err
	}
	id.CAPool = x509.NewCertPool()
	if !id.CAPool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("join: bad ca.crt")
	}
	return &id, nil
}

// TLSConfig returns the mTLS client config. It trusts only the saved CA.
func (id *Identity) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.Cert},
		RootCAs:      id.CAPool,
	}
}

// CertExpiry returns the end of the client certificate.
func (id *Identity) CertExpiry() time.Time {
	if len(id.Cert.Certificate) == 0 {
		return time.Time{}
	}
	c, err := x509.ParseCertificate(id.Cert.Certificate[0])
	if err != nil {
		return time.Time{}
	}
	return c.NotAfter
}

// SaveCert replaces the client certificate (after a renewal).
func SaveCert(dir string, certPEM []byte) error {
	p := filepath.Join(dir, fileCert)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, certPEM, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
