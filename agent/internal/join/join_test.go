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
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type refuseServer struct {
	agentv1.UnimplementedAgentJoinServer
}

func (refuseServer) Join(context.Context, *agentv1.JoinRequest) (*agentv1.JoinResponse, error) {
	return nil, status.Error(codes.PermissionDenied, "join token is invalid, used or expired")
}

func TestJoinRefused(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "srv"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	srvDER, _ := x509.CreateCertificate(rand.Reader, srvTpl, caCert, &srvKey.PublicKey, caKey)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{srvDER, caDER}, PrivateKey: srvKey}},
	})))
	agentv1.RegisterAgentJoinServer(gs, refuseServer{})
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	sum := sha256.Sum256(caDER)
	fp := hex.EncodeToString(sum[:])
	dir := t.TempDir()
	_, err = Run(context.Background(), Options{Server: lis.Addr().String(), Token: "pj_x", CAFingerprint: fp, DataDir: dir})
	if err == nil || !strings.Contains(err.Error(), "join token is invalid, used or expired") {
		t.Fatalf("err = %v", err)
	}
	if Exists(dir) {
		t.Fatal("node.json must not exist after a refused join")
	}
	// A wrong fingerprint must fail before any request is sent.
	_, err = Run(context.Background(), Options{Server: lis.Addr().String(), Token: "pj_x", CAFingerprint: strings.Repeat("00", 32), DataDir: dir})
	if err == nil || strings.Contains(err.Error(), "join token") {
		t.Fatalf("wrong fingerprint err = %v", err)
	}
}
