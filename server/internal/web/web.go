// SPDX-License-Identifier: Apache-2.0

// Package web embeds the built web app (REQ-CORE-004: one binary).
// The folder dist is filled by "make web". A placeholder page is committed so the server always builds.
package web

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the app files. Paths that are not files get index.html,
// so the client router can handle them.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // the embedded folder always exists
	}
	return handler(sub)
}

// compressible are the file types that gzip makes smaller. Images and fonts are already compressed.
var compressible = map[string]bool{".js": true, ".css": true, ".html": true, ".svg": true, ".json": true, ".map": true, ".txt": true, ".xml": true}

// gzipMinBytes is the size under which a file is sent as it is.
const gzipMinBytes = 1024

// gzipped serves a file compressed when the browser accepts gzip. It returns false when the file is sent in the
// normal way. The compressed bytes are kept, so a file is compressed once (REQ-UI-029).
func gzipped(w http.ResponseWriter, r *http.Request, files fs.FS, name string, cache *sync.Map) bool {
	ext := path.Ext(name)
	if !compressible[ext] || r.Header.Get("Range") != "" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	var gz []byte
	if v, ok := cache.Load(name); ok {
		gz = v.([]byte)
	} else {
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return false
		}
		if len(raw) < gzipMinBytes {
			cache.Store(name, []byte(nil))
			return false
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		gz = buf.Bytes()
		cache.Store(name, gz)
	}
	if gz == nil {
		return false
	}
	w.Header().Add("Vary", "Accept-Encoding")
	w.Header().Set("Content-Encoding", "gzip")
	if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(gz))
	return true
}

func handler(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	cache := &sync.Map{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(files, "index.html"); err != nil {
			http.Error(w, "The web app is not built. Run make web.", http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(files, name); err != nil || st.IsDir() {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			if gzipped(w, r2, files, "index.html", cache) {
				return
			}
			fileServer.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if gzipped(w, r, files, name, cache) {
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
