// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KEKSize is the size of the master key (AES-256).
const KEKSize = 32

// LoadKEK returns the master key (KEK).
//
// Order:
//  1. The env POLIPAN_MASTER_KEY (base64 of 32 bytes) wins.
//  2. The file masterKeyFile. It holds 32 raw bytes or the base64 text of 32 bytes.
//  3. If the file does not exist, 32 random bytes are written to it with mode 0400
//     (the installer also creates this file) and used.
//
// A key with the wrong size is an error. The key is never logged.
func LoadKEK(masterKeyFile string, getenv func(string) string) ([]byte, error) {
	if v := strings.TrimSpace(getenv("POLIPAN_MASTER_KEY")); v != "" {
		return decodeKEK([]byte(v))
	}
	if masterKeyFile == "" {
		return nil, errors.New("kek: no POLIPAN_MASTER_KEY and no master key file set")
	}
	raw, err := os.ReadFile(masterKeyFile)
	if err == nil {
		return decodeKEK(raw)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("kek: read %s: %w", masterKeyFile, err)
	}
	key := make([]byte, KEKSize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(masterKeyFile), 0o700); err != nil {
		return nil, fmt.Errorf("kek: %w", err)
	}
	// O_EXCL: if another process made the file at the same time, read that one.
	f, err := os.OpenFile(masterKeyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if errors.Is(err, fs.ErrExist) {
		raw, rerr := os.ReadFile(masterKeyFile)
		if rerr != nil {
			return nil, fmt.Errorf("kek: read %s: %w", masterKeyFile, rerr)
		}
		return decodeKEK(raw)
	}
	if err != nil {
		return nil, fmt.Errorf("kek: create %s: %w", masterKeyFile, err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("kek: write %s: %w", masterKeyFile, err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeKEK(raw []byte) ([]byte, error) {
	if len(raw) == KEKSize {
		return bytes.Clone(raw), nil
	}
	text := strings.TrimSpace(string(raw))
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(text); err == nil && len(b) == KEKSize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("kek: the master key must be %d bytes (raw or base64)", KEKSize)
}
