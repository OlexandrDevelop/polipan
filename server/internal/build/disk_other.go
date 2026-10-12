// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package build

// Disk is only on Linux. Elsewhere the size is unknown, so a new release is refused (disk_low).
func Disk(path string) DiskSpace {
	return DiskSpace{Path: path, Error: "disk space is read only on Linux"}
}
