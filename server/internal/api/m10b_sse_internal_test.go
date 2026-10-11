// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/polipan/polipan/server/internal/store"
)

// Threat model row 21 (REQ-SEC-105): a user may have 10 open event streams. The 11th gets 429, and a stream
// that ends gives its place back.
func TestM10b_SSEStreamsPerUserLimit(t *testing.T) {
	open := func(user string) (*httptest.ResponseRecorder, func(), bool) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, &Principal{User: &store.User{ID: user}}))
		rec := httptest.NewRecorder()
		_, release, ok := openSSE(rec, req)
		return rec, release, ok
	}
	var releases []func()
	for i := 0; i < sseLimit; i++ {
		_, release, ok := open("usr_sse_test")
		if !ok {
			t.Fatalf("stream %d was refused", i+1)
		}
		releases = append(releases, release)
	}
	if rec, _, ok := open("usr_sse_test"); ok || rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 11th stream: ok=%v code=%d", ok, rec.Code)
	}
	if _, release, ok := open("usr_other"); !ok {
		t.Fatal("the limit is per user")
	} else {
		release()
	}
	releases[0]()
	if _, release, ok := open("usr_sse_test"); !ok {
		t.Fatal("a closed stream must give its place back")
	} else {
		release()
	}
	for _, r := range releases[1:] {
		r()
	}
}
