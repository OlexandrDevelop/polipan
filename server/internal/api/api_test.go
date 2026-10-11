// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	specapi "github.com/polipan/polipan/api"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newRouter(t *testing.T, db api.Pinger, spec []byte, limit int) http.Handler {
	t.Helper()
	h, err := api.NewRouter(api.Deps{
		Log:       slog.New(slog.NewJSONHandler(io.Discard, nil)),
		DB:        db,
		Clock:     clock.NewFake(time.Unix(1_700_000_000, 0)),
		RateLimit: limit,
		Spec:      spec,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return h
}

// permissionMatrix reads the permission names from the matrix of docs/spec/10-security.md.
func permissionMatrix(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/spec/10-security.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(raw), "### Permission matrix")
	if !ok {
		t.Fatal("permission matrix not found in 10-security.md")
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z]+:[a-z]+)` \\|")
	allowed := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(after, -1) {
		allowed[m[1]] = true
	}
	if len(allowed) < 40 {
		t.Fatalf("matrix has only %d permissions, the parser is wrong", len(allowed))
	}
	return allowed
}

// REQ-API-101: the real OpenAPI file has x-permission on every operation, from the matrix or special.
func TestAC_API_01_REQ_API_101_SpecPermissionsAreValid(t *testing.T) {
	ops, err := api.ParseOperations(specapi.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 100 {
		t.Fatalf("only %d operations parsed", len(ops))
	}
	if err := api.CheckPermissions(ops, permissionMatrix(t)); err != nil {
		t.Fatal(err)
	}
}

// REQ-API-101: the router of the real server matches the OpenAPI file in both directions.
func TestREQ_API_101_RoutesMatchSpec(t *testing.T) {
	r := newRouter(t, fakeDB{}, nil, 600)
	ops, _ := api.ParseOperations(specapi.Spec)
	routes, err := api.ListRoutes(r.(chi.Routes))
	if err != nil {
		t.Fatal(err)
	}
	if err := api.CheckRoutes(ops, routes); err != nil {
		t.Fatal(err)
	}
}

// REQ-API-101: a test fails when an operation has no x-permission.
func TestREQ_API_101_MissingPermissionFails(t *testing.T) {
	ops := []api.Operation{
		{Method: "GET", Path: "/a", OperationID: "a", Permission: "org:read"},
		{Method: "GET", Path: "/b", OperationID: "b"},
		{Method: "GET", Path: "/c", OperationID: "c", Permission: "not:in:matrix"},
	}
	err := api.CheckPermissions(ops, permissionMatrix(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"GET /b: x-permission is missing", `x-permission "not:in:matrix" is not in the permission matrix`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "GET /a") {
		t.Errorf("a valid operation is reported: %v", err)
	}
}

// REQ-API-101: the server does not start when the file has an operation without x-permission.
func TestREQ_API_101_NewRouterRefusesSpecWithoutPermission(t *testing.T) {
	broken := bytes.Replace(specapi.Spec, []byte("      x-permission: public\n"), nil, 1)
	if bytes.Equal(broken, specapi.Spec) {
		t.Fatal("test setup: nothing was removed")
	}
	_, err := api.NewRouter(api.Deps{Log: slog.Default(), DB: fakeDB{}, Clock: clock.Real{}, RateLimit: 10, Spec: broken})
	if err == nil || !strings.Contains(err.Error(), "x-permission is missing") {
		t.Fatalf("want x-permission error, got %v", err)
	}
}

// REQ-API-101: a test fails when a route is not in the OpenAPI file, and when an operation has no route.
func TestREQ_API_101_RouteAndSpecMismatchFails(t *testing.T) {
	ops := []api.Operation{
		{Method: "GET", Path: "/services/{service_id}", OperationID: "getService", Permission: "service:read"},
		{Method: "POST", Path: "/only-in-spec", OperationID: "onlyInSpec", Permission: "org:read"},
	}
	routes := []api.Route{
		{Method: "GET", Pattern: "/api/v1/services/{id}"},  // same route, other parameter name: fine
		{Method: "GET", Pattern: "/api/v1/only-in-router"}, // not in the file
		{Method: "GET", Pattern: "/api/v1/openapi.yaml"},   // the file itself: allowed
		{Method: "GET", Pattern: "/healthz"},               // outside the base path: ignored
	}
	err := api.CheckRoutes(ops, routes)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"route GET /api/v1/only-in-router is not in the OpenAPI file",
		"operation POST /api/v1/only-in-spec (onlyInSpec) has no route",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "services") || strings.Contains(err.Error(), "openapi.yaml") || strings.Contains(err.Error(), "healthz") {
		t.Errorf("a valid route is reported: %v", err)
	}
}

// The server does not start when a route of the router is not in the file.
func TestREQ_API_101_NewRouterRefusesRouteNotInSpec(t *testing.T) {
	// Removing an operation from the file leaves its generated route without a spec entry.
	broken := regexp.MustCompile(`(?s)  /version:\n.*?\n  /setup/first-user:`).ReplaceAll(specapi.Spec, []byte("  /setup/first-user:"))
	_, err := api.NewRouter(api.Deps{Log: slog.Default(), DB: fakeDB{}, Clock: clock.Real{}, RateLimit: 10, Spec: broken})
	if err == nil || !strings.Contains(err.Error(), "GET /api/v1/version is not in the OpenAPI file") {
		t.Fatalf("want route error, got %v", err)
	}
}

// Done criterion 2 of M0: /healthz returns ok with the database up.
func TestM0_HealthzOkWhenDatabaseUp(t *testing.T) {
	r := newRouter(t, fakeDB{}, nil, 600)
	for _, path := range []string{"/healthz", "/api/v1/healthz"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		var body struct {
			Status   string `json:"status"`
			Database bool   `json:"database"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Status != "ok" || !body.Database {
			t.Fatalf("%s: body %s", path, rec.Body)
		}
		if rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s: no request id header", path)
		}
	}
}

func TestM0_HealthzDegradedWhenDatabaseDown(t *testing.T) {
	r := newRouter(t, fakeDB{err: errors.New("down")}, nil, 600)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"degraded"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

// REQ-CORE-033 and REQ-API-004: unimplemented operations and unknown paths use the error object with the request id.
func TestREQ_API_004_ErrorModel(t *testing.T) {
	r := newRouter(t, fakeDB{}, nil, 600)
	cases := []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/api/v1/auth/sso/providers", http.StatusOK, ""},
		{http.MethodGet, "/api/v1/nothing", http.StatusNotFound, "not_found"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("X-Request-ID", "client-id-12345")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var body struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if c.status == http.StatusOK {
			// A public handler answers without a store: an empty list, and the request id is echoed.
			if rec.Code != c.status || rec.Header().Get("X-Request-ID") != "client-id-12345" {
				t.Errorf("%s: status %d body %s", c.path, rec.Code, rec.Body)
			}
			continue
		}
		if rec.Code != c.status || body.Code != c.code || body.RequestID != "client-id-12345" {
			t.Errorf("%s: status %d body %s", c.path, rec.Code, rec.Body)
		}
	}
}

// REQ-API-100 and rate limit: openapi.yaml is served, and the limiter answers 429 with Retry-After.
func TestREQ_API_100_SpecServed(t *testing.T) {
	r := newRouter(t, fakeDB{}, nil, 600)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), specapi.Spec) {
		t.Fatalf("status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/docs", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/api/v1/healthz") {
		t.Fatalf("docs: status %d", rec.Code)
	}
}

func TestM0_RateLimit429(t *testing.T) {
	r := newRouter(t, fakeDB{}, nil, 2)
	var last *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		last = httptest.NewRecorder()
		r.ServeHTTP(last, httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))
		if i < 2 && last.Code != 200 {
			t.Fatalf("request %d: status %d", i, last.Code)
		}
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q", last.Code, last.Header().Get("Retry-After"))
	}
}
