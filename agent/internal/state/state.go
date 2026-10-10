// SPDX-License-Identifier: Apache-2.0

// Package state keeps the last accepted desired state and the last command acks on disk
// (REQ-AGENT-004, REQ-AGENT-005, REQ-AGENT-006).
package state

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	sealPrefix  = "enc:v1:"
	maxCommands = 1000
)

// Store is the state folder of the agent.
type Store struct {
	dir string

	mu       sync.Mutex
	key      []byte
	version  int64
	loaded   bool
	cmdOrder []string
	cmdAcks  map[string]*agentv1.CommandAck
}

// New opens the store in dir. The folder is created with mode 0700.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, cmdAcks: map[string]*agentv1.CommandAck{}}, nil
}

// nodeKey returns the AES key. It is created on first use (mode 0400).
func (s *Store) nodeKey() ([]byte, error) {
	if s.key != nil {
		return s.key, nil
	}
	p := filepath.Join(s.dir, "node.key")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, b, 0o400); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("state: node.key must have 32 bytes")
	}
	s.key = b
	return b, nil
}

func (s *Store) seal(plain string) (string, error) {
	key, err := s.nodeKey()
	if err != nil {
		return "", err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) open(sealed string) (string, error) {
	if !strings.HasPrefix(sealed, sealPrefix) {
		return sealed, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil {
		return "", err
	}
	key, err := s.nodeKey()
	if err != nil {
		return "", err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("state: sealed value too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

// mapSecrets applies f to every env value listed in secret_env_keys, on a copy.
func mapSecrets(ds *agentv1.DesiredState, f func(string) (string, error)) (*agentv1.DesiredState, error) {
	out := proto.Clone(ds).(*agentv1.DesiredState)
	for _, w := range out.Workloads {
		for _, k := range w.SecretEnvKeys {
			if v, ok := w.Env[k]; ok {
				nv, err := f(v)
				if err != nil {
					return nil, err
				}
				w.Env[k] = nv
			}
		}
	}
	return out, nil
}

func (s *Store) desiredPath() string { return filepath.Join(s.dir, "desired.json") }

// LoadDesired returns the persisted desired state, or nil when there is none.
func (s *Store) LoadDesired() (*agentv1.DesiredState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (*agentv1.DesiredState, error) {
	b, err := os.ReadFile(s.desiredPath())
	if errors.Is(err, os.ErrNotExist) {
		s.loaded = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ds := &agentv1.DesiredState{}
	if err := protojson.Unmarshal(b, ds); err != nil {
		return nil, fmt.Errorf("state: read desired.json: %w", err)
	}
	plain, err := mapSecrets(ds, s.open)
	if err != nil {
		return nil, err
	}
	s.version, s.loaded = plain.Version, true
	return plain, nil
}

// SaveDesired stores ds when its version is higher than the stored one. It reports if it was stored.
func (s *Store) SaveDesired(ds *agentv1.DesiredState) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		if _, err := s.loadLocked(); err != nil {
			return false, err
		}
	}
	if ds.Version <= s.version {
		return false, nil
	}
	sealed, err := mapSecrets(ds, s.seal)
	if err != nil {
		return false, err
	}
	b, err := protojson.Marshal(sealed)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(s.desiredPath(), b, 0o600); err != nil {
		return false, err
	}
	s.version = ds.Version
	return true, nil
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type ackFile struct {
	Order []string                   `json:"order"`
	Acks  map[string]json.RawMessage `json:"acks"`
}

func (s *Store) commandsPath() string { return filepath.Join(s.dir, "commands.json") }

// LoadCommands reads the stored acks from disk. A missing file is fine.
func (s *Store) LoadCommands() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.commandsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f ackFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	for _, id := range f.Order {
		ack := &agentv1.CommandAck{}
		if err := protojson.Unmarshal(f.Acks[id], ack); err != nil {
			return err
		}
		s.cmdOrder = append(s.cmdOrder, id)
		s.cmdAcks[id] = ack
	}
	return nil
}

// Ack returns the stored ack of a command id.
func (s *Store) Ack(id string) (*agentv1.CommandAck, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.cmdAcks[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(a).(*agentv1.CommandAck), true
}

// PutAck stores the ack of a command. Only the last 1000 are kept.
func (s *Store) PutAck(ack *agentv1.CommandAck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ack.CommandId
	if _, ok := s.cmdAcks[id]; !ok {
		s.cmdOrder = append(s.cmdOrder, id)
	}
	s.cmdAcks[id] = proto.Clone(ack).(*agentv1.CommandAck)
	for len(s.cmdOrder) > maxCommands {
		delete(s.cmdAcks, s.cmdOrder[0])
		s.cmdOrder = s.cmdOrder[1:]
	}
	f := ackFile{Order: s.cmdOrder, Acks: map[string]json.RawMessage{}}
	for _, k := range s.cmdOrder {
		b, err := protojson.Marshal(s.cmdAcks[k])
		if err != nil {
			return err
		}
		f.Acks[k] = b
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeAtomic(s.commandsPath(), b, 0o600)
}
