// SPDX-License-Identifier: Apache-2.0

package ingress

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/polipan/polipan/proto/agentv1"
)

const maxErrorLen = 500

// DomainStatus returns the TLS status of every host of spec (REQ-DOM-003).
// Only hosts with https get an entry. A host with a valid certificate in the Caddy
// storage is issued; else the last ACME error in the Caddy log makes it failed; else pending.
func (m *Manager) DomainStatus(ctx context.Context, spec *agentv1.IngressSpec) []*agentv1.DomainStatus {
	hosts := httpsHosts(spec)
	var out []*agentv1.DomainStatus
	var open []*agentv1.DomainStatus
	for _, h := range hosts {
		st := &agentv1.DomainStatus{Host: h, TlsStatus: "pending"}
		if exp, ok := m.certExpiry(h); ok {
			st.TlsStatus = "issued"
			st.ExpiresUnixMs = exp.UnixMilli()
		} else {
			open = append(open, st)
		}
		out = append(out, st)
	}
	if len(open) == 0 || m.Docker == nil {
		return out
	}
	logs, err := m.Docker.Logs(ctx, ContainerName, 200)
	if err != nil {
		return out
	}
	errs := acmeErrors(logs)
	for _, st := range open {
		if e, ok := errs[st.Host]; ok {
			st.TlsStatus = "failed"
			st.Error = e
		}
	}
	return out
}

// httpsHosts returns the sorted hosts that need a certificate (same rule as BuildConfig).
func httpsHosts(spec *agentv1.IngressSpec) []string {
	set := map[string]bool{}
	for _, r := range spec.GetRoutes() {
		if !r.GetHttps() {
			continue
		}
		hosts := cleanHosts(r.GetHosts())
		switch r.GetRedirectWww() {
		case "to_www", "to_apex":
			serve, from := wwwSplit(hosts, r.GetRedirectWww() == "to_www")
			hosts = append(serve, from...)
		}
		for _, h := range hosts {
			set[h] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// certExpiry reads the newest valid certificate of host from the Caddy storage.
func (m *Manager) certExpiry(host string) (time.Time, bool) {
	dir := strings.ReplaceAll(host, "*", "wildcard_")
	pattern := filepath.Join(m.Config.DataDir, "caddy", "certificates", "*", dir, dir+".crt")
	files, _ := filepath.Glob(pattern)
	var best time.Time
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		block, _ := pem.Decode(data)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if cert.NotAfter.After(time.Now()) && cert.NotAfter.After(best) {
			best = cert.NotAfter
		}
	}
	return best, !best.IsZero()
}

// acmeErrors returns the last ACME error text per host found in Caddy JSON log lines.
func acmeErrors(logs string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(logs))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, "{")
		if i < 0 {
			continue
		}
		var e struct {
			Level      string `json:"level"`
			Logger     string `json:"logger"`
			Identifier string `json:"identifier"`
			Host       string `json:"host"`
			Error      string `json:"error"`
			Msg        string `json:"msg"`
		}
		if json.Unmarshal([]byte(line[i:]), &e) != nil || e.Level != "error" {
			continue
		}
		if !strings.HasPrefix(e.Logger, "tls.obtain") && !strings.HasPrefix(e.Logger, "tls.issuance") &&
			!strings.HasPrefix(e.Logger, "tls.renew") {
			continue
		}
		host := strings.ToLower(e.Identifier)
		if host == "" {
			host = strings.ToLower(e.Host)
		}
		if host == "" {
			continue
		}
		text := e.Error
		if text == "" {
			text = e.Msg
		}
		out[host] = cut(text, maxErrorLen)
	}
	return out
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // do not cut inside a UTF-8 character
		n--
	}
	return s[:n]
}
