// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/store"
)

const totpIssuer = "Polipan"

func (h *Handlers) needKEK(w http.ResponseWriter, r *http.Request) bool {
	if len(h.KEK) == 0 {
		internalError(w, r, errors.New("no master key for two-factor login"))
		return false
	}
	return true
}

// EnrollTotp makes a new TOTP secret. The otpauth URI is shown once. TOTP is not on until VerifyTotp.
func (h *Handlers) EnrollTotp(w http.ResponseWriter, r *http.Request) {
	if !h.needKEK(w, r) {
		return
	}
	p := PrincipalFrom(r.Context())
	if p.User.TotpEnabled {
		WriteError(w, r, http.StatusConflict, CodeConflict, "two-factor login is on already. Turn it off first", map[string]any{"reason": "totp_enabled"})
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		internalError(w, r, err)
		return
	}
	enc, err := auth.Seal(h.KEK, secret, "totp:"+p.User.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if err := h.Store.SetTOTPPending(r.Context(), p.User.ID, enc); err != nil {
		internalError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"otpauth_uri": auth.TOTPURI(totpIssuer, p.User.Email, secret)})
}

// VerifyTotp confirms the enrollment with a code, turns TOTP on and returns the 10 recovery codes once.
func (h *Handlers) VerifyTotp(w http.ResponseWriter, r *http.Request) {
	var body gen.VerifyTotpJSONBody
	if !decodeJSON(w, r, &body) || !h.needKEK(w, r) {
		return
	}
	p := PrincipalFrom(r.Context())
	key := "totp:" + p.User.ID
	if locked, wait := h.Throttle.Locked(key); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	t, err := h.Store.GetTOTP(r.Context(), p.User.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if t.Enabled || len(t.SecretEnc) == 0 {
		WriteError(w, r, http.StatusConflict, CodeConflict, "start the enrollment first", map[string]any{"reason": "no_enrollment"})
		return
	}
	secret, err := auth.Open(h.KEK, t.SecretEnc, "totp:"+p.User.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	step, ok := auth.CheckTOTP(secret, body.TotpCode, h.Clock.Now())
	if !ok {
		h.Throttle.Fail(key)
		invalid(w, r, "totp_code", "the code is wrong")
		return
	}
	plain, hashes, err := auth.NewRecoveryCodes()
	if err != nil {
		internalError(w, r, err)
		return
	}
	if err := h.Store.EnableTOTP(r.Context(), p.OrgID, p.User.ID, step, hashes); err != nil {
		internalError(w, r, err)
		return
	}
	h.Throttle.Reset(key)
	WriteJSON(w, http.StatusOK, map[string]any{"recovery_codes": plain})
}

// DisableTotp turns TOTP off. It needs the password.
func (h *Handlers) DisableTotp(w http.ResponseWriter, r *http.Request) {
	var body gen.DisableTotpJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p := PrincipalFrom(r.Context())
	key := "pw:" + p.User.ID
	if locked, wait := h.Throttle.Locked(key); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	good := false
	if p.User.PasswordHash != nil {
		good, _ = auth.VerifyPassword(body.Password, *p.User.PasswordHash)
	}
	if !good {
		h.Throttle.Fail(key)
		invalid(w, r, "password", "the password is wrong")
		return
	}
	if err := h.Store.DisableTOTP(r.Context(), p.OrgID, p.User.ID); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RequestPasswordReset always answers 202, so nobody can find out which emails exist. The server has no mail
// sender yet, so no link is sent: an admin makes one with "polipan-server admin reset-password" (REQ-AUTH-007).
func (h *Handlers) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body gen.RequestPasswordResetJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	key := "reset:" + clientKey(r)
	if locked, wait := h.Throttle.Locked(key); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	h.Throttle.Fail(key) // counts every request: at most 5 per 15 minutes per address
	logging.From(r.Context()).Info("password reset requested, no mail sender is set up")
	w.WriteHeader(http.StatusAccepted)
}

// ConfirmPasswordReset sets a new password with the token from the reset link.
func (h *Handlers) ConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body gen.ConfirmPasswordResetJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := auth.CheckPasswordPolicy(body.Password); err != nil {
		invalid(w, r, "password", err.Error())
		return
	}
	key := "resetc:" + clientKey(r)
	if locked, wait := h.Throttle.Locked(key); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		internalError(w, r, err)
		return
	}
	_, err = h.Store.ConfirmPasswordReset(r.Context(), auth.HashToken(strings.TrimSpace(body.Token)), hash, h.Clock.Now())
	if errors.Is(err, store.ErrNotFound) {
		h.Throttle.Fail(key)
		invalid(w, r, "token", "the reset link is not valid or has expired")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
