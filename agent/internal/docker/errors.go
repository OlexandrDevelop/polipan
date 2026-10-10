// SPDX-License-Identifier: Apache-2.0

package docker

import "errors"

// ErrNotFound is returned when a container or image does not exist.
var ErrNotFound = errors.New("docker: not found")

// ErrUnavailable is returned when the Docker socket cannot be reached.
var ErrUnavailable = errors.New("docker: unavailable")
