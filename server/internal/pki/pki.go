// SPDX-License-Identifier: Apache-2.0

// Package pki is the server CA. It makes the gateway TLS certificate and signs the agent
// client certificates (09-multi-server, section 2). The CA key is stored encrypted
// with AES-256-GCM, using the master key (KEK), in the table server_ca.
package pki

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	caValidity     = 10 * 365 * 24 * time.Hour
	serverValidity = 365 * 24 * time.Hour
	keyAAD         = "polipan-server-ca"
)

// CA is the server certificate authority.
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey
}

// Load reads the CA from the table server_ca. If there is none, it creates one
// (ECDSA P-256, 10 years) and stores it. kek must be 32 bytes.
func Load(ctx context.Context, pool *pgxpool.Pool, kek []byte) (*CA, error) {
	if len(kek) != KEKSize {
		return nil, errors.New("pki: the master key must be 32 bytes")
	}
	ca, err := read(ctx, pool, kek)
	if err == nil {
		return ca, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	fresh, certPEM, keyEnc, err := create(kek)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO server_ca (id, cert_pem, key_enc) VALUES (1, $1, $2) ON CONFLICT (id) DO NOTHING`,
		string(certPEM), keyEnc)
	if err != nil {
		return nil, fmt.Errorf("pki: store CA: %w", err)
	}
	// Read again: if another server won the race, we use its CA.
	ca, err = read(ctx, pool, kek)
	if err != nil {
		return nil, err
	}
	_ = fresh
	return ca, nil
}

func read(ctx context.Context, pool *pgxpool.Pool, kek []byte) (*CA, error) {
	var certPEM string
	var keyEnc []byte
	err := pool.QueryRow(ctx, `SELECT cert_pem, key_enc FROM server_ca WHERE id = 1`).Scan(&certPEM, &keyEnc)
	if err != nil {
		return nil, err
	}
	keyDER, err := decrypt(kek, keyEnc)
	if err != nil {
		return nil, fmt.Errorf("pki: decrypt CA key (wrong master key?): %w", err)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, errors.New("pki: CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA certificate: %w", err)
	}
	return &CA{cert: cert, certPEM: []byte(certPEM), key: key}, nil
}

func create(kek []byte) (*CA, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now().UTC()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Polipan server CA", Organization: []string{"Polipan"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	keyEnc, err := encrypt(kek, keyDER)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &CA{cert: cert, certPEM: certPEM, key: key}, certPEM, keyEnc, nil
}

// CertPEM returns the CA certificate in PEM.
func (c *CA) CertPEM() []byte { return c.certPEM }

// Pool returns a pool with only this CA, to verify agent client certificates.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// Fingerprint is the sha256 of the CA certificate DER bytes, lower case hex.
func (c *CA) Fingerprint() string {
	sum := sha256.Sum256(c.cert.Raw)
	return hex.EncodeToString(sum[:])
}

// ServerCert makes a TLS certificate for the gateway. The names are the given hosts
// plus localhost and 127.0.0.1. Empty and duplicate names are skipped.
func (c *CA) ServerCert(hosts []string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	var dns []string
	var ips []net.IP
	seen := map[string]bool{}
	for _, h := range append(append([]string{}, hosts...), "localhost", "127.0.0.1") {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	now := time.Now().UTC()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "polipan-server", Organization: []string{"Polipan"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(serverValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	// The chain has the CA too, so an agent can check the pinned CA fingerprint (09, section 2).
	return &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// AgentCert is a signed agent certificate.
type AgentCert struct {
	PEM      []byte
	Serial   string // hex
	NotAfter time.Time
}

// SignAgentCert checks the CSR and signs a client certificate with subject CN nodeID.
// The subject of the CSR is ignored. The caller stores Serial and NotAfter on the node row.
func (c *CA) SignAgentCert(csrPEM []byte, nodeID string, validity time.Duration) (*AgentCert, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("pki: the CSR is not PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: CSR signature: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	notAfter := now.Add(validity)
	if notAfter.After(c.cert.NotAfter) {
		notAfter = c.cert.NotAfter
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"Polipan"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("pki: sign: %w", err)
	}
	return &AgentCert{
		PEM:      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Serial:   hex.EncodeToString(serial.Bytes()),
		NotAfter: notAfter,
	}, nil
}

// SerialHex returns the serial of a certificate in the same form as AgentCert.Serial.
func SerialHex(cert *x509.Certificate) string { return hex.EncodeToString(cert.SerialNumber.Bytes()) }

// IsRevoked tells if the serial is in the table revoked_certs.
func IsRevoked(ctx context.Context, pool *pgxpool.Pool, serial string) (bool, error) {
	var revoked bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM revoked_certs WHERE serial = $1)`, serial).Scan(&revoked)
	return revoked, err
}

// Revoke puts a serial of a node on the revocation list.
func Revoke(ctx context.Context, pool *pgxpool.Pool, nodeID, serial string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO revoked_certs (serial, node_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, serial, nodeID)
	return err
}

func randomSerial() (*big.Int, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	b[0] &= 0x7f // positive
	b[0] |= 0x01 // never leading zero, so hex is stable
	return new(big.Int).SetBytes(b), nil
}

func encrypt(kek, plain []byte) ([]byte, error) {
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte(keyAAD)), nil
}

func decrypt(kek, data []byte) ([]byte, error) {
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("data too short")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte(keyAAD))
}

func newGCM(kek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
