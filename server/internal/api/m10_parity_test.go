// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	specapi "github.com/polipan/polipan/api"
	"github.com/polipan/polipan/server/internal/api"
)

// operationsOfScreens reads the operationIds in the Data and Actions lines of docs/spec/ui/screens.md.
func operationsOfScreens(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/spec/ui/screens.md")
	if err != nil {
		t.Fatal(err)
	}
	tick := regexp.MustCompile("`([a-z][A-Za-z0-9]*)`")
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "- **Data:**") && !strings.HasPrefix(line, "- **Actions:**") {
			continue
		}
		for _, m := range tick.FindAllStringSubmatch(line, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// AC-API-03 (REQ-API-003): every action of a screen is an API operation that the server does. The test calls each operation
// named in a Data or Actions line of the screens with an owner session, and fails when the server answers 501 (not
// implemented) or when the operation does not exist. Words in those lines that are not operations are skipped.
func TestAC_API_03_EveryScreenActionIsAnImplementedOperation(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	ops, err := api.ParseOperations(specapi.Spec)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]api.Operation{}
	for _, o := range ops {
		byID[o.OperationID] = o
	}
	notOperations := map[string]bool{"cron": true, "inputs": true, "localStorage": true, "preview": true}
	// Known gaps: none. Every operation of a screen has a handler. Keep the list empty.
	knownGaps := map[string]bool{}
	var missing, unimplemented []string
	checked := 0
	for id := range operationsOfScreens(t) {
		if notOperations[id] {
			continue
		}
		op, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if id == "logout" {
			continue // it would end the session of this test
		}
		code := owner.call(op.Method, fillPath(op.FullPath(), nil))
		checked++
		if code == 501 && !knownGaps[id] {
			unimplemented = append(unimplemented, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(unimplemented)
	if checked < 100 {
		t.Fatalf("only %d operations were checked: the parser of screens.md is wrong", checked)
	}
	if len(missing) > 0 || len(unimplemented) > 0 {
		t.Fatalf("screen operations that are not in the API: %v; that answer 501: %v", missing, unimplemented)
	}
}
