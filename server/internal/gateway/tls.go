// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"crypto/tls"
)

// IngestTLSConfig is the TLS setup of the ingest listener (metrics and log batches of the agents). It needs a
// client certificate of the server CA, like the stream does, and uses the same server certificate.
func (g *Gateway) IngestTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      g.CA.Pool(),
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return g.serverCert() },
	}
}
