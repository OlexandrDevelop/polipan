// SPDX-License-Identifier: Apache-2.0

package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, path, acceptEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// REQ-UI-029: text assets are compressed by the server when the browser accepts gzip, and the headers of the cache stay.
func TestAssetsAreGzipped(t *testing.T) {
	js := strings.Repeat("export const answer = 42; // padding\n", 200)
	h := handler(fstest.MapFS{
		"index.html":     {Data: []byte("<!doctype html>" + strings.Repeat("<p>hello</p>", 200))},
		"assets/app.js":  {Data: []byte(js)},
		"assets/tiny.js": {Data: []byte("x=1")},
		"assets/a.png":   {Data: []byte(strings.Repeat("png", 1000))},
	})

	rec := get(h, "/assets/app.js", "gzip, br")
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") ||
		!strings.Contains(rec.Header().Get("Content-Type"), "javascript") || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("headers: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); string(body) != js {
		t.Fatal("the unpacked body is not the file")
	}
	if rec.Body.Len() >= len(js)/4 {
		t.Fatalf("compressed size %d of %d", rec.Body.Len(), len(js))
	}

	// No Accept-Encoding: the file as it is. Small files and images are never compressed.
	if rec := get(h, "/assets/app.js", ""); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != js {
		t.Fatal("a client without gzip got a compressed file")
	}
	for _, p := range []string{"/assets/tiny.js", "/assets/a.png"} {
		if rec := get(h, p, "gzip"); rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s was compressed", p)
		}
	}
	// The page for a client route is index.html: compressed, not cached.
	rec = get(h, "/projects/prj_1", "gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index fallback: %v", rec.Header())
	}
}
