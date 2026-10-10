// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/polipan/polipan/server/internal/registry"
)

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// ociTar writes a small OCI layout: one layer, one config, one manifest.
func ociTar(t *testing.T) (string, string) {
	t.Helper()
	layer, config := []byte("layer-bytes"), []byte(`{"architecture":"amd64","os":"linux"}`)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":%d}]}`,
		digestOf(config), len(config), digestOf(layer), len(layer)))
	index := []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d}]}`,
		digestOf(manifest), len(manifest)))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, b []byte) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(b)
	}
	add("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	add("index.json", index)
	for _, b := range [][]byte{layer, config, manifest} {
		add("blobs/sha256/"+strings.TrimPrefix(digestOf(b), "sha256:"), b)
	}
	_ = tw.Close()
	p := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, digestOf(manifest)
}

// AC-APP-01 (push part): the server pushes the OCI output by the registry API with its own
// user and returns the ref by digest at the pull host.
func TestAC_APP_01_PushOCI(t *testing.T) {
	var mu sync.Mutex
	blobs, manifests := map[string]bool{}, map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if u, _, ok := r.BasicAuth(); !ok || u != registry.ServerUser {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			w.Header().Set("Location", "/v2/svc-1/blobs/uploads/abc?state=x")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			b, _ := io.ReadAll(r.Body)
			if digestOf(b) != r.URL.Query().Get("digest") || r.URL.Query().Get("state") != "x" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			blobs[r.URL.Query().Get("digest")] = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			manifests[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]] = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	reg := registry.New(registry.Config{URL: srv.URL, PullHost: "10.0.0.1:5000"}, nil, nil, make([]byte, 32))
	tarPath, want := ociTar(t)
	ref, digest, err := reg.Push(context.Background(), tarPath, "svc-1", "abc1234")
	if err != nil {
		t.Fatal(err)
	}
	if digest != want || ref != "10.0.0.1:5000/svc-1@"+want {
		t.Fatalf("ref %s digest %s", ref, digest)
	}
	if len(blobs) != 3 || manifests[want] == "" || manifests["abc1234"] != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("blobs %v manifests %v", blobs, manifests)
	}
	// Per-node credentials differ and use the auth ref of the built-in registry.
	a, b := reg.Auth("nod_a"), reg.Auth("nod_b")
	if a.Ref != registry.AuthRef || a.Password == b.Password || a.Username == b.Username || !reg.IsOwnImage(ref) || reg.IsOwnImage("nginx:1") {
		t.Fatalf("auth %v %v", a, b)
	}
}
