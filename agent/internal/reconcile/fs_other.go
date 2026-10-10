// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package reconcile

import "errors"

// isTmpfs: secret files need a Linux tmpfs.
func isTmpfs(string) error { return errors.New("secret files need a Linux tmpfs") }

// xfsQuota has no quota support outside Linux.
type xfsQuota struct{}

func (xfsQuota) Apply(string, uint64) (bool, error) { return false, nil }
