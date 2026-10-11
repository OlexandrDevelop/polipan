// SPDX-License-Identifier: Apache-2.0

package ingress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func routesOf(t *testing.T, cfg map[string]any) []any {
	t.Helper()
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["polipan"].(map[string]any)
	var out []any
	for _, r := range srv["routes"].([]any) {
		if m, ok := r.(map[string]any)["match"].([]any); ok && m[0].(map[string]any)["protocol"] == "http" {
			continue // the HTTP to HTTPS redirect has its own test
		}
		if r.(map[string]any)["match"] == nil && handlerNames(r)[0] == "log_append" {
			continue // the access log route has its own test
		}
		out = append(out, r)
	}
	return out
}

func TestBuildHTTPRedirect(t *testing.T) {
	b, err := BuildConfig(spec1(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := decode(t, b)["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["polipan"].(map[string]any)
	first := srv["routes"].([]any)[1].(map[string]any) // the route before it only adds the access log field
	m := first["match"].([]any)[0].(map[string]any)
	if m["protocol"] != "http" || m["host"].([]any)[0] != "app.example.com" {
		t.Fatalf("first route must redirect http for the https host: %v", m)
	}
	h := first["handle"].([]any)[0].(map[string]any)
	if h["status_code"].(float64) != 308 {
		t.Fatalf("redirect status %v", h["status_code"])
	}
}

func handlerNames(route any) []string {
	var out []string
	for _, h := range route.(map[string]any)["handle"].([]any) {
		out = append(out, h.(map[string]any)["handler"].(string))
	}
	return out
}

func spec1() *agentv1.IngressSpec {
	return &agentv1.IngressSpec{
		AcmeEmail: "me@example.com",
		AcmeCa:    "letsencrypt",
		Routes: []*agentv1.Route{{
			RouteId: "r1", Hosts: []string{"App.example.com"}, Https: true,
			Upstreams: []*agentv1.Upstream{{Address: "pp-plc1:8080"}},
		}},
	}
}

func TestBuildOneRoute(t *testing.T) {
	b, err := BuildConfig(spec1(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, b)
	if cfg["admin"].(map[string]any)["listen"] != AdminListen {
		t.Fatal("admin must be the unix socket")
	}
	if cfg["storage"].(map[string]any)["root"] != "/data" {
		t.Fatal("storage root")
	}
	routes := routesOf(t, cfg)
	if len(routes) != 2 {
		t.Fatalf("want route + catch-all, got %d", len(routes))
	}
	r := routes[0].(map[string]any)
	host := r["match"].([]any)[0].(map[string]any)["host"].([]any)
	if len(host) != 1 || host[0] != "app.example.com" {
		t.Fatalf("host %v", host)
	}
	names := handlerNames(routes[0])
	if len(names) != 3 || names[0] != "encode" || names[1] != "request_body" || names[2] != "reverse_proxy" {
		t.Fatalf("handlers %v", names)
	}
	h := r["handle"].([]any)
	if h[1].(map[string]any)["max_size"] != float64(100<<20) {
		t.Fatal("body limit")
	}
	p := h[2].(map[string]any)
	if p["upstreams"].([]any)[0].(map[string]any)["dial"] != "pp-plc1:8080" || p["flush_interval"] != float64(-1) {
		t.Fatalf("proxy %v", p)
	}
	set := p["headers"].(map[string]any)["request"].(map[string]any)["set"].(map[string]any)
	for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host"} {
		if set[k] == nil {
			t.Fatalf("missing %s", k)
		}
	}
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["polipan"].(map[string]any)
	if srv["automatic_https"] != nil {
		t.Fatal("https host must use automatic https defaults")
	}
}

func TestAC_DOM_04_BuildZeroUpstreams503(t *testing.T) {
	s := spec1()
	s.Routes[0].Upstreams = nil
	b, _ := BuildConfig(s, Options{})
	r := routesOf(t, decode(t, b))[0]
	h := r.(map[string]any)["handle"].([]any)
	if len(h) != 1 {
		t.Fatalf("handlers %v", h)
	}
	m := h[0].(map[string]any)
	hd := m["headers"].(map[string]any)
	if m["handler"] != "static_response" || m["status_code"] != float64(503) ||
		hd["Cache-Control"].([]any)[0] != "no-store" || hd["Content-Type"] == nil {
		t.Fatalf("503 handler %v", m)
	}
	if body, _ := m["body"].(string); body == "" || !contains(body, "Service is stopped or not deployed yet") {
		t.Fatal("body")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestBuildRedirectWWW(t *testing.T) {
	s := spec1()
	s.Routes[0].Hosts = []string{"example.com"}
	s.Routes[0].RedirectWww = "to_www"
	b, err := BuildConfig(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, b)
	routes := routesOf(t, cfg)
	if len(routes) != 3 {
		t.Fatalf("routes %d", len(routes))
	}
	red := routes[0].(map[string]any)
	if red["match"].([]any)[0].(map[string]any)["host"].([]any)[0] != "example.com" {
		t.Fatal("redirect must match the apex")
	}
	hd := red["handle"].([]any)[0].(map[string]any)
	if hd["headers"].(map[string]any)["Location"].([]any)[0] != "https://www.example.com{http.request.uri}" {
		t.Fatalf("location %v", hd)
	}
	main := routes[1].(map[string]any)["match"].([]any)[0].(map[string]any)["host"].([]any)[0]
	if main != "www.example.com" {
		t.Fatalf("main host %v", main)
	}
	subjects := cfg["apps"].(map[string]any)["tls"].(map[string]any)["automation"].(map[string]any)["policies"].([]any)[0].(map[string]any)["subjects"].([]any)
	if len(subjects) != 2 {
		t.Fatalf("subjects %v", subjects)
	}
}

func TestBuildCatchAllAndPlainHTTP(t *testing.T) {
	s := spec1()
	s.Routes = append(s.Routes, &agentv1.Route{RouteId: "r2", Hosts: []string{"plain.example.com"}, Https: false,
		Upstreams: []*agentv1.Upstream{{Address: "pp-plc2:80"}}})
	b, _ := BuildConfig(s, Options{})
	cfg := decode(t, b)
	routes := routesOf(t, cfg)
	last := routes[len(routes)-1].(map[string]any)
	if last["match"] != nil {
		t.Fatal("catch-all must have no matcher")
	}
	h := last["handle"].([]any)[0].(map[string]any)
	if h["status_code"] != float64(404) || h["body"] != "no such site" {
		t.Fatalf("catch-all %v", h)
	}
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["polipan"].(map[string]any)
	skip := srv["automatic_https"].(map[string]any)["skip"].([]any)
	if len(skip) != 1 || skip[0] != "plain.example.com" {
		t.Fatalf("skip %v", skip)
	}
	subjects := cfg["apps"].(map[string]any)["tls"].(map[string]any)["automation"].(map[string]any)["policies"].([]any)[0].(map[string]any)["subjects"].([]any)
	if len(subjects) != 1 || subjects[0] != "app.example.com" {
		t.Fatalf("subjects %v", subjects)
	}
}

func issuerModules(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	pol := decode(t, b)["apps"].(map[string]any)["tls"].(map[string]any)["automation"].(map[string]any)["policies"].([]any)[0].(map[string]any)
	var out []map[string]any
	for _, i := range pol["issuers"].([]any) {
		out = append(out, i.(map[string]any))
	}
	return out
}

func TestBuildACMEIssuers(t *testing.T) {
	b, _ := BuildConfig(spec1(), Options{})
	is := issuerModules(t, b)
	if len(is) != 2 || is[0]["module"] != "acme" || is[0]["email"] != "me@example.com" ||
		!contains(is[0]["ca"].(string), "letsencrypt") || !contains(is[1]["ca"].(string), "zerossl") {
		t.Fatalf("issuers %v", is)
	}
	s := spec1()
	s.AcmeCa = "zerossl"
	b, _ = BuildConfig(s, Options{})
	if is = issuerModules(t, b); !contains(is[0]["ca"].(string), "zerossl") || !contains(is[1]["ca"].(string), "letsencrypt") {
		t.Fatalf("zerossl order %v", is)
	}
	b, _ = BuildConfig(spec1(), Options{ACMECAURL: "https://ca.test/dir", ACMETrustedRoots: "/roots.pem"})
	is = issuerModules(t, b)
	if len(is) != 1 || is[0]["ca"] != "https://ca.test/dir" || is[0]["trusted_roots_pem_files"].([]any)[0] != "/roots.pem" {
		t.Fatalf("custom ca %v", is)
	}
}

func TestBuildDeterministic(t *testing.T) {
	a := &agentv1.IngressSpec{AcmeEmail: "a@b.c", Routes: []*agentv1.Route{
		{RouteId: "1", Hosts: []string{"b.example.com", "a.example.com"}, Https: true, Upstreams: []*agentv1.Upstream{{Address: "x:1"}, {Address: "y:2"}}},
		{RouteId: "2", Hosts: []string{"a.example.com"}, PathPrefix: "/api/", Https: true},
		{RouteId: "3", Hosts: []string{"c.example.com"}},
	}}
	b := &agentv1.IngressSpec{AcmeEmail: "a@b.c", Routes: []*agentv1.Route{
		{RouteId: "3", Hosts: []string{"c.example.com"}},
		{RouteId: "2", Hosts: []string{"a.example.com"}, PathPrefix: "/api", Https: true},
		{RouteId: "1", Hosts: []string{"a.example.com", "b.example.com"}, Https: true, Upstreams: []*agentv1.Upstream{{Address: "y:2"}, {Address: "x:1"}}},
	}}
	x, _ := BuildConfig(a, Options{})
	y, _ := BuildConfig(b, Options{})
	if string(x) != string(y) {
		t.Fatal("output differs")
	}
	// The longer path prefix comes first.
	if routesOf(t, decode(t, x))[0].(map[string]any)["match"].([]any)[0].(map[string]any)["path"] == nil {
		t.Fatal("path route must be first")
	}
}

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestApplyLoadsOnlyOnChange(t *testing.T) {
	dir := shortDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "caddy-admin"), 0o700); err != nil {
		t.Fatal(err)
	}
	var loads atomic.Int32
	var fail atomic.Bool
	var gotType atomic.Value
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/load" {
			w.WriteHeader(404)
			return
		}
		loads.Add(1)
		gotType.Store(r.Header.Get("Content-Type"))
		_, _ = io.Copy(io.Discard, r.Body)
		if fail.Load() {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bad config"}`))
		}
	}))
	l, err := net.Listen("unix", filepath.Join(dir, "caddy-admin", "admin.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()

	m := &Manager{Config: Config{DataDir: dir}}
	ctx := context.Background()
	h1, err := m.Apply(ctx, spec1())
	if err != nil || loads.Load() != 1 || gotType.Load() != "application/json" {
		t.Fatalf("first apply: %v loads=%d", err, loads.Load())
	}
	if h2, err := m.Apply(ctx, spec1()); err != nil || h2 != h1 || loads.Load() != 1 {
		t.Fatalf("same spec must not load: %v loads=%d", err, loads.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "caddy-config-hash")); string(b) != h1 {
		t.Fatal("hash file")
	}
	// A new manager (agent restart) reads the saved hash.
	m2 := &Manager{Config: Config{DataDir: dir}}
	if _, err := m2.Apply(ctx, spec1()); err != nil || loads.Load() != 1 {
		t.Fatalf("restart must not load: %v", err)
	}
	s2 := spec1()
	s2.Routes[0].Upstreams = nil
	if h3, err := m.Apply(ctx, s2); err != nil || h3 == h1 || loads.Load() != 2 {
		t.Fatalf("changed spec: %v loads=%d", err, loads.Load())
	}
	// A failed load keeps the old hash.
	fail.Store(true)
	s3 := spec1()
	s3.Routes[0].Hosts = []string{"other.example.com"}
	_, err = m.Apply(ctx, s3)
	var le *LoadError
	ok := errors.As(err, &le)
	if !ok || le.Code() != "caddy_load_failed" || !contains(le.Error(), "bad config") {
		t.Fatalf("want load error, got %v", err)
	}
	fail.Store(false)
	if _, err := m.Apply(ctx, s3); err != nil || loads.Load() != 4 {
		t.Fatalf("retry must load again: %v loads=%d", err, loads.Load())
	}
}

type fakeDocker struct {
	docker.Client
	logs string
}

func (f *fakeDocker) Logs(context.Context, string, int) (string, error) { return f.logs, nil }

func writeCert(t *testing.T, dir, host string, notAfter time.Time) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, DNSNames: []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(dir, "caddy", "certificates", "acme-v02.api.letsencrypt.org-directory", host)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(d, host+".crt"), pemData, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAC_DOM_05_DomainStatus(t *testing.T) {
	dir := t.TempDir()
	exp := time.Now().Add(60 * 24 * time.Hour).Truncate(time.Second)
	writeCert(t, dir, "ok.example.com", exp)
	logs := `{"level":"info","logger":"tls.obtain","msg":"obtaining certificate","identifier":"bad.example.com"}
{"level":"error","ts":1,"logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":"bad.example.com","issuer":"acme","error":"HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: NXDOMAIN"}
not json
`
	m := &Manager{Config: Config{DataDir: dir}, Docker: &fakeDocker{logs: logs}}
	spec := &agentv1.IngressSpec{Routes: []*agentv1.Route{
		{Hosts: []string{"ok.example.com", "bad.example.com", "new.example.com"}, Https: true},
		{Hosts: []string{"plain.example.com"}},
	}}
	got := map[string]*agentv1.DomainStatus{}
	for _, s := range m.DomainStatus(context.Background(), spec) {
		got[s.Host] = s
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	if s := got["ok.example.com"]; s.TlsStatus != "issued" || s.ExpiresUnixMs != exp.UnixMilli() {
		t.Fatalf("ok %v", s)
	}
	if s := got["bad.example.com"]; s.TlsStatus != "failed" || !contains(s.Error, "NXDOMAIN") {
		t.Fatalf("bad %v", s)
	}
	if s := got["new.example.com"]; s.TlsStatus != "pending" {
		t.Fatalf("new %v", s)
	}
}

// AC-DEPLOY-09: a route with stream_close_delay_seconds keeps open streams for that long after a config load.
func TestBuildStreamCloseDelay(t *testing.T) {
	spec := &agentv1.IngressSpec{Routes: []*agentv1.Route{{RouteId: "r", Hosts: []string{"a.test"}, PathPrefix: "/", Https: false,
		Upstreams: []*agentv1.Upstream{{Address: "pp-x:80", Weight: 100}}, StreamCloseDelaySeconds: 30}}}
	b, err := BuildConfig(spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"stream_close_delay":"30s"`) {
		t.Fatalf("no stream_close_delay in %s", b)
	}
}

func TestBuildAccessLog(t *testing.T) {
	b, err := BuildConfig(spec1(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, b)
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["polipan"].(map[string]any)
	if srv["logs"].(map[string]any)["default_logger_name"] != AccessLogger {
		t.Fatalf("server logs %v", srv["logs"])
	}
	// The first route is not terminal and adds the upstream to the log line.
	first := srv["routes"].([]any)[0].(map[string]any)
	if first["terminal"] != nil || first["match"] != nil {
		t.Fatalf("first route %v", first)
	}
	la := first["handle"].([]any)[0].(map[string]any)
	if la["handler"] != "log_append" || la["key"] != "upstream" || la["value"] != "{http.reverse_proxy.upstream.hostport}" {
		t.Fatalf("log_append %v", la)
	}
	logs := cfg["logging"].(map[string]any)["logs"].(map[string]any)
	acc := logs[AccessLogger].(map[string]any)
	if acc["include"].([]any)[0] != "http.log.access" || acc["encoder"].(map[string]any)["format"] != "json" {
		t.Fatalf("access log %v", acc)
	}
	if logs["default"].(map[string]any)["exclude"].([]any)[0] != "http.log.access" {
		t.Fatal("the default log must not repeat access lines")
	}
}

// stepDocker records the calls of Start in order.
type stepDocker struct {
	docker.Client
	steps   []string
	pullErr error
}

func (f *stepDocker) Inspect(context.Context, string) (*docker.Container, error) {
	return nil, docker.ErrNotFound
}

func (f *stepDocker) PullImage(_ context.Context, ref, _ string) (string, error) {
	f.steps = append(f.steps, "pull "+ref)
	return ref, f.pullErr
}

func (f *stepDocker) CreateAndStart(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.steps = append(f.steps, "create "+spec.Image)
	return "id", nil
}

// A new host has no Caddy image: Start pulls it before it creates the container, and a failed pull is not fatal.
func TestStartPullsTheCaddyImageFirst(t *testing.T) {
	for _, pullErr := range []error{nil, errors.New("offline")} {
		f := &stepDocker{pullErr: pullErr}
		m := &Manager{Config: Config{DataDir: t.TempDir()}, Docker: f}
		if err := m.Start(context.Background()); err != nil {
			t.Fatalf("start with pull error %v: %v", pullErr, err)
		}
		want := []string{"pull " + DefaultImage, "create " + DefaultImage}
		if len(f.steps) != 2 || f.steps[0] != want[0] || f.steps[1] != want[1] {
			t.Fatalf("steps %v, want %v", f.steps, want)
		}
	}
}
