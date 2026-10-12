// SPDX-License-Identifier: Apache-2.0

//go:build linux

package diskfs

import "syscall"

func usage(path string) (used, avail, size uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := uint64(st.Bsize) //nolint:unconvert // the type differs per architecture
	return (st.Blocks - st.Bfree) * bs, st.Bavail * bs, st.Blocks * bs, nil
}

func deviceOf(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil //nolint:unconvert // the type differs per architecture
}
