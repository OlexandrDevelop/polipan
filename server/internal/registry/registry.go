// SPDX-License-Identifier: Apache-2.0

// Package registry is the client side of the built-in registry (CNCF distribution).
//
//   - The server pushes built images (OCI layout from BuildKit) with its own user. Build steps
//     never push: the build network cannot reach the registry (REQ-SEC-108).
//   - Every node gets its own pull user (htpasswd, bcrypt). Its password is derived from the KEK,
//     so nothing secret is stored. The credential goes to the node in DesiredState.registry_auths,
//     and workloads point to it with registry_auth_ref. Pulls are by digest.
//   - The registry serves TLS with a certificate from the server CA. The server writes the
//     htpasswd file and the certificate into a folder shared with the registry container.
//     The registry reloads htpasswd when the file changes.
package registry

import (
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/pki"
)

// AuthRef is the registry_auth_ref of images in the built-in registry.
const AuthRef = "polipan-registry"

// ServerUser is the push user of the server.
const ServerUser = "polipan-server"

// Config is where the registry is.
type Config struct {
	// URL is the address the server pushes to, for example https://registry:5000.
	URL string
	// PullHost is host:port the nodes pull from (image refs use it), for example 172.31.251.10:5000.
	PullHost string
	// Dir is the folder shared with the registry container (htpasswd, tls.crt, tls.key).
	Dir string
}

// ConfigFromEnv reads POLIPAN_REGISTRY_URL, POLIPAN_REGISTRY_PULL_HOST and POLIPAN_REGISTRY_DIR.
// An empty URL means there is no built-in registry (git builds then fail with a clear message).
func ConfigFromEnv(getenv func(string) string) Config {
	c := Config{URL: getenv("POLIPAN_REGISTRY_URL"), PullHost: getenv("POLIPAN_REGISTRY_PULL_HOST"), Dir: getenv("POLIPAN_REGISTRY_DIR")}
	if c.Dir == "" {
		c.Dir = "/var/lib/polipan/registry"
	}
	if c.PullHost == "" && c.URL != "" {
		c.PullHost = strings.TrimPrefix(strings.TrimPrefix(c.URL, "https://"), "http://")
	}
	return c
}

// Registry is the built-in registry.
type Registry struct {
	Cfg    Config
	DB     *pgxpool.Pool
	CA     *pki.CA
	kek    []byte
	client *http.Client

	mu    sync.Mutex
	known map[string]bool // users written to htpasswd
}

// New makes the registry client. ca signs the registry certificate and is trusted for pushes.
func New(cfg Config, db *pgxpool.Pool, ca *pki.CA, kek []byte) *Registry {
	r := &Registry{Cfg: cfg, DB: db, CA: ca, kek: kek, known: map[string]bool{}}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != nil {
		tlsCfg.RootCAs = ca.Pool()
	}
	r.client = &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: nil}}
	return r
}

// Enabled tells if a built-in registry is configured.
func (r *Registry) Enabled() bool { return r != nil && r.Cfg.URL != "" }

// password derives the password of a user from the KEK.
func (r *Registry) password(user string) string {
	m := hmac.New(sha256.New, r.kek)
	m.Write([]byte("polipan-registry-user:" + user))
	return hex.EncodeToString(m.Sum(nil))
}

// NodeUser is the pull user of a node.
func NodeUser(nodeID string) string { return "node-" + nodeID }

// Auth returns the pull credential of a node, for DesiredState.registry_auths.
func (r *Registry) Auth(nodeID string) *pb.RegistryAuth {
	u := NodeUser(nodeID)
	return &pb.RegistryAuth{Ref: AuthRef, Server: r.Cfg.PullHost, Username: u, Password: r.password(u)}
}

// IsOwnImage tells if an image ref points to the built-in registry.
func (r *Registry) IsOwnImage(ref string) bool {
	return r.Enabled() && r.Cfg.PullHost != "" && strings.HasPrefix(ref, r.Cfg.PullHost+"/")
}

// Setup writes the TLS certificate (when missing or near its end) and the htpasswd file.
func (r *Registry) Setup(ctx context.Context) error {
	if !r.Enabled() {
		return nil
	}
	if err := os.MkdirAll(r.Cfg.Dir, 0o750); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if err := r.writeCert(); err != nil {
		return err
	}
	// After a KEK rotation the derived passwords change: then all users are made again
	// (node users come back at the next desired state of each node).
	var hash string
	err := r.DB.QueryRow(ctx, `SELECT password_hash FROM registry_credentials WHERE username = $1`, ServerUser).Scan(&hash)
	if err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.password(ServerUser))) != nil {
		if _, err := r.DB.Exec(ctx, `DELETE FROM registry_credentials`); err != nil {
			return err
		}
	}
	if err := r.ensureUser(ctx, ServerUser, nil); err != nil {
		return err
	}
	return r.writeHtpasswd(ctx)
}

func (r *Registry) writeCert() error {
	certFile := filepath.Join(r.Cfg.Dir, "tls.crt")
	if raw, err := os.ReadFile(certFile); err == nil {
		if b, _ := pem.Decode(raw); b != nil {
			if c, err := x509.ParseCertificate(b.Bytes); err == nil && time.Until(c.NotAfter) > 30*24*time.Hour {
				return nil
			}
		}
	}
	if r.CA == nil {
		return errors.New("registry: no CA for the certificate")
	}
	hosts := []string{"registry"}
	for _, h := range []string{r.Cfg.PullHost, strings.TrimPrefix(strings.TrimPrefix(r.Cfg.URL, "https://"), "http://")} {
		if i := strings.LastIndex(h, ":"); i > 0 {
			h = h[:i]
		}
		hosts = append(hosts, h)
	}
	cert, err := r.CA.ServerCert(hosts)
	if err != nil {
		return err
	}
	var chain []byte
	for _, der := range cert.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(r.Cfg.Dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o640); err != nil {
		return err
	}
	return writeAtomic(certFile, chain, 0o644)
}

// EnsureNode adds the pull user of a node to htpasswd when it is not there yet.
func (r *Registry) EnsureNode(ctx context.Context, nodeID string) error {
	if !r.Enabled() {
		return nil
	}
	u := NodeUser(nodeID)
	r.mu.Lock()
	done := r.known[u]
	r.mu.Unlock()
	if done {
		return nil
	}
	if err := r.ensureUser(ctx, u, &nodeID); err != nil {
		return err
	}
	return r.writeHtpasswd(ctx)
}

func (r *Registry) ensureUser(ctx context.Context, user string, nodeID *string) error {
	var exists bool
	if err := r.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM registry_credentials WHERE username = $1)`, user).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(r.password(user)), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = r.DB.Exec(ctx, `INSERT INTO registry_credentials (username, node_id, password_hash) VALUES ($1,$2,$3)
		ON CONFLICT (username) DO NOTHING`, user, nodeID, string(hash))
	return err
}

// writeHtpasswd writes all users of the table. Users of deleted nodes are gone (ON DELETE CASCADE).
func (r *Registry) writeHtpasswd(ctx context.Context) error {
	rows, err := r.DB.Query(ctx, `SELECT username, password_hash FROM registry_credentials ORDER BY username`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lines []string
	known := map[string]bool{}
	for rows.Next() {
		var u, h string
		if err := rows.Scan(&u, &h); err != nil {
			return err
		}
		lines = append(lines, u+":"+h)
		known[u] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(lines)
	if err := writeAtomic(filepath.Join(r.Cfg.Dir, "htpasswd"), []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		return err
	}
	r.mu.Lock()
	r.known = known
	r.mu.Unlock()
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	return nil
}

// Ref is the pull address of an image by digest.
func (r *Registry) Ref(repo, digest string) string { return r.Cfg.PullHost + "/" + repo + "@" + digest }
