// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// Fake is a Builder for tests. It checks the Dockerfile like the real one, prints Lines to
// the log and writes a small file as the output.
type Fake struct {
	mu        sync.Mutex
	Lines     []string
	DetectErr string // railpack: fail detection with this message
	Err       error
	Calls     []Request
}

// Build implements Builder.
func (f *Fake) Build(_ context.Context, req Request) error {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.mu.Unlock()
	if req.Builder == Dockerfile {
		name := req.Dockerfile
		if name == "" {
			name = "Dockerfile"
		}
		if _, err := os.Stat(filepath.Join(req.ContextDir, name)); err != nil {
			return &DetectError{Msg: "no " + name + " in the build path"}
		}
	}
	if req.Builder == Railpack && f.DetectErr != "" {
		return &DetectError{Msg: f.DetectErr}
	}
	for _, l := range f.Lines {
		if req.Log != nil {
			req.Log(l)
		}
	}
	if f.Err != nil {
		return f.Err
	}
	return os.WriteFile(req.Output, []byte("oci"), 0o600)
}
