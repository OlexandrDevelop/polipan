// SPDX-License-Identifier: Apache-2.0

//go:build linux

package metrics

import "syscall"

// diskUsage returns the used and the total bytes of the file system of path.
func diskUsage(path string) (used, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize) //nolint:unconvert // the type differs per architecture
	total = st.Blocks * bs
	used = (st.Blocks - st.Bfree) * bs
	return used, total, nil
}
