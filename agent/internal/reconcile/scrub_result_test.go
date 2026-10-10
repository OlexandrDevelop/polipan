// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"strings"
	"testing"
)

// R11: the result of a run or a check goes to the server, so the secrets of the job are removed from it.
func TestScrubResultRemovesJobSecrets(t *testing.T) {
	e := &Engine{}
	res := map[string]any{"snapshots": []map[string]any{{"error": "restic failed: password hunter2-secret rejected by AKIASECRETKEY1"}}}
	got := string(e.scrubResult(res, []string{"hunter2-secret", "AKIASECRETKEY1"}))
	if strings.Contains(got, "hunter2-secret") || strings.Contains(got, "AKIASECRETKEY1") || !strings.Contains(got, "***") {
		t.Fatalf("result: %s", got)
	}
}
