// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package diskfs

import "errors"

var errNotLinux = errors.New("diskfs: needs Linux")

func usage(string) (used, avail, size uint64, err error) { return 0, 0, 0, errNotLinux }

func deviceOf(string) (uint64, error) { return 0, errNotLinux }
