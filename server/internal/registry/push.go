// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Pusher pushes a built image. Tests use a fake.
type Pusher interface {
	// Push uploads the OCI layout tar (output type=oci of BuildKit) as repo:tag and returns the
	// image ref by digest (pull host) and the digest.
	Push(ctx context.Context, ociTar, repo, tag string) (ref, digest string, err error)
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
}

type manifestDoc struct {
	MediaType string       `json:"mediaType"`
	Manifests []descriptor `json:"manifests"`
}

// Push implements Pusher with the registry HTTP API v2 and the server user.
func (r *Registry) Push(ctx context.Context, ociTar, repo, tag string) (string, string, error) {
	if !r.Enabled() {
		return "", "", errors.New("registry: no built-in registry is configured (POLIPAN_REGISTRY_URL)")
	}
	dir, err := os.MkdirTemp("", "pp-oci-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	if err := untar(ociTar, dir); err != nil {
		return "", "", err
	}
	var index manifestDoc
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return "", "", fmt.Errorf("registry: the build output has no index.json: %w", err)
	}
	if err := json.Unmarshal(raw, &index); err != nil || len(index.Manifests) == 0 {
		return "", "", errors.New("registry: the build output has no image")
	}
	top := index.Manifests[0]

	// 1. every blob (layers and configs; manifests too, which is harmless)
	blobs, err := os.ReadDir(filepath.Join(dir, "blobs", "sha256"))
	if err != nil {
		return "", "", fmt.Errorf("registry: the build output has no blobs: %w", err)
	}
	for _, b := range blobs {
		if err := r.pushBlob(ctx, repo, "sha256:"+b.Name(), filepath.Join(dir, "blobs", "sha256", b.Name())); err != nil {
			return "", "", err
		}
	}
	// 2. manifests, children first, the top one also with the tag
	if err := r.pushManifest(ctx, dir, repo, top, tag, 0); err != nil {
		return "", "", err
	}
	return r.Ref(repo, top.Digest), top.Digest, nil
}

func (r *Registry) pushManifest(ctx context.Context, dir, repo string, d descriptor, tag string, depth int) error {
	if depth > 3 {
		return errors.New("registry: manifests are nested too deep")
	}
	body, err := os.ReadFile(blobPath(dir, d.Digest))
	if err != nil {
		return fmt.Errorf("registry: manifest %s: %w", d.Digest, err)
	}
	var doc manifestDoc
	_ = json.Unmarshal(body, &doc)
	for _, child := range doc.Manifests {
		if err := r.pushManifest(ctx, dir, repo, child, "", depth+1); err != nil {
			return err
		}
	}
	mt := d.MediaType
	if mt == "" {
		mt = doc.MediaType
	}
	refs := []string{d.Digest}
	if tag != "" {
		refs = append(refs, tag)
	}
	for _, ref := range refs {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.Cfg.URL+"/v2/"+repo+"/manifests/"+ref, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mt)
		if err := r.do(req, http.StatusCreated); err != nil {
			return fmt.Errorf("registry: put manifest: %w", err)
		}
	}
	return nil
}

func blobPath(dir, digest string) string {
	algo, hexPart, _ := strings.Cut(digest, ":")
	return filepath.Join(dir, "blobs", algo, hexPart)
}

func (r *Registry) pushBlob(ctx context.Context, repo, digest, path string) error {
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, r.Cfg.URL+"/v2/"+repo+"/blobs/"+digest, nil)
	if err != nil {
		return err
	}
	if r.do(head, http.StatusOK) == nil {
		return nil // already there
	}
	start, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Cfg.URL+"/v2/"+repo+"/blobs/uploads/", nil)
	if err != nil {
		return err
	}
	start.SetBasicAuth(ServerUser, r.password(ServerUser))
	resp, err := r.client.Do(start)
	if err != nil {
		return fmt.Errorf("registry: start upload: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("registry: start upload: status %d", resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		return fmt.Errorf("registry: upload location: %w", err)
	}
	q := loc.Query()
	q.Set("digest", digest)
	loc.RawQuery = q.Encode()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, loc.String(), f)
	if err != nil {
		return err
	}
	put.ContentLength = st.Size()
	put.Header.Set("Content-Type", "application/octet-stream")
	if err := r.do(put, http.StatusCreated); err != nil {
		return fmt.Errorf("registry: upload %s: %w", digest, err)
	}
	return nil
}

func (r *Registry) do(req *http.Request, want int) error {
	req.SetBasicAuth(ServerUser, r.password(ServerUser))
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode != want {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// untar extracts a tar file. Only regular files and folders inside dir are written.
func untar(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("registry: open build output: %w", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("registry: read build output: %w", err)
		}
		name := filepath.Clean("/" + h.Name)
		target := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			sum := sha256.New()
			_, err = io.Copy(io.MultiWriter(out, sum), tr)
			out.Close()
			if err != nil {
				return err
			}
			// A blob must match its name.
			if strings.HasPrefix(name, "/blobs/sha256/") && hex.EncodeToString(sum.Sum(nil)) != filepath.Base(name) {
				return fmt.Errorf("registry: blob %s has a wrong digest", filepath.Base(name))
			}
		}
	}
}

// RepoName makes a registry repository name from a service id ("svc_01ab" -> "svc-01ab").
func RepoName(serviceID string) string {
	return url.PathEscape(strings.ToLower(strings.ReplaceAll(serviceID, "_", "-")))
}
