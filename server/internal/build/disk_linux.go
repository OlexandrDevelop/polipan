// SPDX-License-Identifier: Apache-2.0

//go:build linux

package build

import "syscall"

// Disk reads the size and the free space of the filesystem that holds path.
// Free bytes are the bytes a process without root can use.
func Disk(path string) DiskSpace {
	s := DiskSpace{Path: path}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		s.Error = err.Error()
		return s
	}
	unit := uint64(st.Frsize)
	if unit == 0 {
		unit = uint64(st.Bsize)
	}
	s.TotalBytes, s.FreeBytes = st.Blocks*unit, st.Bavail*unit
	s.TotalInodes, s.FreeInodes = st.Files, st.Ffree
	return s
}
