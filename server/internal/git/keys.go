// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// DeployKey is a new SSH key pair for one service (REQ-APP-011).
type DeployKey struct {
	PrivatePEM string // OpenSSH private key, stored only as a secret
	Public     string // authorized_keys line, shown to the user and kept in the service source
}

// NewDeployKey makes an ed25519 key pair. comment is put at the end of the public key.
func NewDeployKey(comment string) (DeployKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return DeployKey{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return DeployKey{}, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return DeployKey{}, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return DeployKey{PrivatePEM: string(pem.EncodeToMemory(block)), Public: line}, nil
}

// NewWebhookSecret makes a random secret for a webhook (32 bytes, URL-safe base64).
func NewWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Scope tells where a stored secret belongs.
type Scope struct {
	OrgID     string
	ProjectID string // empty for org-level values (Git connections)
	ServiceID string
}

// KeyStore keeps the secret values of Git: tokens, private keys, webhook secrets and deploy keys.
// The value never leaves the server. The server implements it with the secrets store
// (server/internal/secrets); tests use MemoryKeyStore.
type KeyStore interface {
	// Put stores value and returns the id of the secret.
	Put(ctx context.Context, scope Scope, name, value string) (id string, err error)
	// Get returns the value of a secret.
	Get(ctx context.Context, id string) (string, error)
	// Delete removes a secret. A missing id is not an error.
	Delete(ctx context.Context, id string) error
}

// ErrNoSecret is returned by Get for an unknown id.
var ErrNoSecret = errors.New("git: secret not found")

// MemoryKeyStore is a KeyStore in memory, for tests.
type MemoryKeyStore struct {
	mu     sync.Mutex
	values map[string]string
	n      int
}

// Put implements KeyStore.
func (m *MemoryKeyStore) Put(_ context.Context, _ Scope, name, value string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.n++
	id := fmt.Sprintf("mem_%d_%s", m.n, name)
	m.values[id] = value
	return id, nil
}

// Get implements KeyStore.
func (m *MemoryKeyStore) Get(_ context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[id]
	if !ok {
		return "", ErrNoSecret
	}
	return v, nil
}

// Delete implements KeyStore.
func (m *MemoryKeyStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, id)
	return nil
}
