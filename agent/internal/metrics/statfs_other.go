// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package metrics

import "errors"

func diskUsage(string) (used, total uint64, err error) {
	return 0, 0, errors.New("metrics: disk usage needs Linux")
}
