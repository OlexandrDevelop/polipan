// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"

	"github.com/polipan/polipan/api/gen"
)

// Stable error codes of docs/spec/05-api.md, section 2. The list is closed (REQ-API-104).
const (
	CodeValidationFailed = "validation_failed" // 422
	CodeUnauthenticated  = "unauthenticated"   // 401
	CodeForbidden        = "forbidden"         // 403
	CodeNotFound         = "not_found"         // 404
	CodeConflict         = "conflict"          // 409
	CodeRateLimited      = "rate_limited"      // 429
	CodeInternal         = "internal_error"    // 500
)

// WriteError writes the error object {code, message, details, request_id}.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	body := gen.Error{Code: code, Message: message, RequestId: RequestIDFrom(r.Context())}
	if len(details) > 0 {
		body.Details = &details
	}
	WriteJSON(w, status, body)
}

// WriteJSON writes a JSON body with the status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
