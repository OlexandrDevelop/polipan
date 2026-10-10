// SPDX-License-Identifier: Apache-2.0

package api

import "time"

// SetExecIdleCheck changes how often an exec session checks its idle time. Only for tests.
func SetExecIdleCheck(d time.Duration) func() {
	old := execIdleCheck
	execIdleCheck = d
	return func() { execIdleCheck = old }
}
