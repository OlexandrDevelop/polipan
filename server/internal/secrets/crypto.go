// SPDX-License-Identifier: Apache-2.0

// Package secrets is the secrets store (F07 7d, 10-security section 4).
//
// Key hierarchy: KEK (master key) -> one DEK per project (data_keys.dek_enc, AES-256-GCM)
// -> one AES-256-GCM ciphertext per secret version (secret_versions), with a fresh 96-bit
// nonce and AAD = secret id || version. The KEK never leaves the server process.
//
// SecretProvider is the interface for later backends (Vault, OpenBao). Only the built-in
// database provider (DBProvider) ships in v1 (REQ-SEC-010).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// KeySize is the size of the KEK and of a DEK (AES-256).
const KeySize = 32

// Errors of the package.
var (
	ErrBadKey           = errors.New("secrets: a key must be 32 bytes")
	ErrDecrypt          = errors.New("secrets: cannot decrypt (wrong key or changed data)")
	ErrVersionDestroyed = errors.New("secrets: the secret version was destroyed")
	ErrWrongKEK         = errors.New("secrets: the data key was wrapped with another master key (restart the server after rotate-kek)")
)

// KEKID names a KEK without showing it: the first 8 bytes of SHA-256 over a label and the key.
func KEKID(kek []byte) string {
	h := sha256.Sum256(append([]byte("polipan-kek-id:"), kek...))
	return "kek_" + hex.EncodeToString(h[:8])
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// dekAAD binds a wrapped DEK to its project.
func dekAAD(projectID string) []byte { return []byte("polipan-dek:" + projectID) }

// versionAAD is secret id || version (4 bytes, big endian).
func versionAAD(secretID string, version int) []byte {
	out := make([]byte, len(secretID)+4)
	copy(out, secretID)
	binary.BigEndian.PutUint32(out[len(secretID):], uint32(version))
	return out
}

// NewDEK makes a random data key.
func NewDEK() ([]byte, error) {
	k := make([]byte, KeySize)
	_, err := rand.Read(k)
	return k, err
}

// WrapDEK encrypts a DEK with the KEK. The result is nonce || ciphertext.
func WrapDEK(kek, dek []byte, projectID string) ([]byte, error) {
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, dek, dekAAD(projectID)), nil
}

// UnwrapDEK decrypts a DEK made by WrapDEK.
func UnwrapDEK(kek, wrapped []byte, projectID string) ([]byte, error) {
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < gcm.NonceSize() {
		return nil, ErrDecrypt
	}
	dek, err := gcm.Open(nil, wrapped[:gcm.NonceSize()], wrapped[gcm.NonceSize():], dekAAD(projectID))
	if err != nil {
		return nil, ErrDecrypt
	}
	return dek, nil
}

// Seal encrypts one secret version with the DEK. It returns a fresh nonce and the ciphertext.
func Seal(dek []byte, secretID string, version int, plain []byte) (nonce, ciphertext []byte, err error) {
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plain, versionAAD(secretID, version)), nil
}

// Open decrypts one secret version.
func Open(dek []byte, secretID string, version int, nonce, ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, ErrDecrypt
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, versionAAD(secretID, version))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// caKeyAAD is the AAD of server_ca.key_enc (same format as package pki: nonce || ciphertext).
const caKeyAAD = "polipan-server-ca"

// rewrapCAKey decrypts the server CA key with the old KEK and encrypts it with the new one.
func rewrapCAKey(oldKEK, newKEK, keyEnc []byte) ([]byte, error) {
	gcm, err := newGCM(oldKEK)
	if err != nil {
		return nil, err
	}
	if len(keyEnc) < gcm.NonceSize() {
		return nil, ErrDecrypt
	}
	plain, err := gcm.Open(nil, keyEnc[:gcm.NonceSize()], keyEnc[gcm.NonceSize():], []byte(caKeyAAD))
	if err != nil {
		return nil, fmt.Errorf("server CA key: %w", ErrDecrypt)
	}
	ngcm, err := newGCM(newKEK)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, ngcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return ngcm.Seal(nonce, nonce, plain, []byte(caKeyAAD)), nil
}
