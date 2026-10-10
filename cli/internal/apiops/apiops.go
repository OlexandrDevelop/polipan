// SPDX-License-Identifier: Apache-2.0

// Package apiops calls the operations of the public API through the generated client (REQ-CORE-009).
// The file ops_gen.go is made by "go generate" from api/gen/api.gen.go.
package apiops

//go:generate go run ./gen

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/polipan/polipan/api/gen"
)

// Input holds what one call needs. Path values are in the order of the path template.
type Input struct {
	Path        []string
	Query       url.Values
	Header      http.Header
	ContentType string
	Body        []byte
}

// Str returns path value i.
func (in Input) Str(i int) string {
	if i < len(in.Path) {
		return in.Path[i]
	}
	return ""
}

// Int returns path value i as a number.
func (in Input) Int(i int) int {
	n, _ := strconv.Atoi(in.Str(i))
	return n
}

// Has tells if the operation is known.
func Has(opID string) bool {
	_, ok := calls[opID]
	return ok
}

// IDs lists all known operation ids, sorted.
func IDs() []string {
	ids := make([]string, 0, len(calls))
	for id := range calls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Do runs one operation. The caller closes the response body.
func Do(ctx context.Context, c *gen.Client, opID string, in Input) (*http.Response, error) {
	call, ok := calls[opID]
	if !ok {
		return nil, fmt.Errorf("unknown operation %s", opID)
	}
	if in.ContentType == "" {
		in.ContentType = "application/json"
	}
	editor := func(_ context.Context, req *http.Request) error {
		if len(in.Query) > 0 {
			q := req.URL.Query()
			for k, v := range in.Query {
				q[k] = v
			}
			req.URL.RawQuery = q.Encode()
		}
		for k, v := range in.Header {
			req.Header[k] = v
		}
		return nil
	}
	return call(ctx, c, in, editor)
}
