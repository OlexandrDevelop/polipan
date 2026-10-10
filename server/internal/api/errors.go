// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
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

	// Specific codes of the list in 05-api.md section 2.
	CodeDomainInUse  = "domain_in_use" // 409
	CodeNameConflict = "name_conflict" // 409

	// M3 codes.
	CodeAlreadyLive         = "already_live"               // 409
	CodeReleaseImagePruned  = "release_image_pruned"       // 409
	CodeNotCancellable      = "deployment_not_cancellable" // 409
	CodeDomainPortMismatch  = "domain_port_mismatch"       // 409
	CodeIdempotencyKeyReuse = "idempotency_key_reuse"      // 409
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

// decodeJSON reads the request body into dst. It writes the error answer and returns false when the
// body is too large (413), empty or not valid (422). Unknown fields are refused (05-api, section 6).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		switch {
		case isTooLarge(err):
			writeTooLarge(w, r, DefaultMaxBodyBytes)
		case errors.Is(err, io.EOF):
			WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, "request body is required", nil)
		default:
			WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, "request body is not valid: "+err.Error(), nil)
		}
		return false
	}
	return true
}

// invalid writes a 422 answer for one field.
func invalid(w http.ResponseWriter, r *http.Request, field, message string) {
	WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, message, map[string]any{"field": field})
}

func notFoundError(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusNotFound, CodeNotFound, "not found", nil)
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	logging.From(r.Context()).Error("request failed", "error", err)
	WriteError(w, r, http.StatusInternalServerError, CodeInternal, "internal error", nil)
}

func ptr[T any](v T) *T { return &v }

var errBadExternalURL = errors.New("external_url is not valid and the Host header is not safe")
