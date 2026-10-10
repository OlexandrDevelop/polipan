// SPDX-License-Identifier: Apache-2.0

// Package version holds the build version. The linker sets the values.
package version

var (
	// Version is the release version, for example 0.1.0.
	Version = "dev"
	// Commit is the git commit of the build.
	Commit = "unknown"
)
