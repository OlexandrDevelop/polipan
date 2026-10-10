// SPDX-License-Identifier: Apache-2.0

package build

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveInside joins rel to root, follows all symlinks and returns the real path.
// It fails if the real path is outside the real root (for example a symlink to "/").
func ResolveInside(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(realRoot, filepath.Clean("/"+rel)))
	if err != nil {
		return "", err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves the repository", rel)
	}
	return real, nil
}
