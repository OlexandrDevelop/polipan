// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/alert"
)

// alertFailed maps the errors of the alert store to answers. It returns true when it wrote one.
func (h *Handlers) alertFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, alert.ErrNotFound):
		notFoundError(w, r)
	case errors.Is(err, alert.ErrInvalid):
		if field := alert.InvalidField(err); field != "" {
			invalid(w, r, field, err.Error())
		} else {
			WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		}
	case errors.Is(err, alert.ErrConflict):
		WriteError(w, r, http.StatusConflict, CodeNameConflict, err.Error(), nil)
	default:
		internalError(w, r, err)
	}
	return true
}

func (h *Handlers) needAlerts(w http.ResponseWriter, r *http.Request) bool {
	if h.Alerts == nil || h.Alerts.Store == nil {
		internalError(w, r, errors.New("the alert engine is not set up"))
		return false
	}
	return true
}

func channelJSON(c *alert.Channel) gen.AlertChannel {
	kind, state := gen.AlertChannelKind(c.Kind), gen.AlertChannelState(c.State)
	cfg := c.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	out := gen.AlertChannel{Id: &c.ID, Kind: &kind, Name: &c.Name, Config: &cfg, Enabled: &c.Enabled, State: &state, LastOkAt: c.LastOKAt}
	if c.LastError != "" {
		out.LastError = &c.LastError
	}
	return out
}

// ListAlertChannels lists the channels. Secrets are never part of the answer.
func (h *Handlers) ListAlertChannels(w http.ResponseWriter, r *http.Request) {
	if !h.needAlerts(w, r) {
		return
	}
	list, err := h.Alerts.Store.ListChannels(r.Context(), h.orgID(r))
	if h.alertFailed(w, r, err) {
		return
	}
	out := make([]gen.AlertChannel, 0, len(list))
	for _, c := range list {
		out = append(out, channelJSON(c))
	}
	WriteJSON(w, http.StatusOK, out)
}

// channelPatch is the body of a change: every field is optional.
type channelPatch struct {
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Config  map[string]any `json:"config"`
	Secret  *string        `json:"secret"`
	Enabled *bool          `json:"enabled"`
}

func (b channelPatch) input() alert.ChannelInput {
	return alert.ChannelInput{Kind: b.Kind, Name: b.Name, Config: b.Config, Secret: b.Secret, Enabled: b.Enabled}
}

// CreateAlertChannel makes a channel. The secret is stored encrypted (REQ-ALERT-001).
func (h *Handlers) CreateAlertChannel(w http.ResponseWriter, r *http.Request) {
	if !h.needAlerts(w, r) {
		return
	}
	var body channelPatch
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := h.Alerts.Store.CreateChannel(r.Context(), h.orgID(r), body.input())
	if h.alertFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_channel.create", "alert_channel", c.ID, map[string]any{"kind": c.Kind, "name": c.Name})
	WriteJSON(w, http.StatusCreated, channelJSON(c))
}

// UpdateAlertChannel changes a channel. A missing secret keeps the old one.
func (h *Handlers) UpdateAlertChannel(w http.ResponseWriter, r *http.Request, channelID gen.ChannelId) {
	if !h.needAlerts(w, r) {
		return
	}
	var body channelPatch
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := h.Alerts.Store.UpdateChannel(r.Context(), h.orgID(r), channelID, body.input())
	if h.alertFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_channel.update", "alert_channel", c.ID, map[string]any{"name": c.Name, "secret_changed": body.Secret != nil})
	WriteJSON(w, http.StatusOK, channelJSON(c))
}

// DeleteAlertChannel removes a channel.
func (h *Handlers) DeleteAlertChannel(w http.ResponseWriter, r *http.Request, channelID gen.ChannelId) {
	if !h.needAlerts(w, r) {
		return
	}
	if h.alertFailed(w, r, h.Alerts.Store.DeleteChannel(r.Context(), h.orgID(r), channelID)) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_channel.delete", "alert_channel", channelID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// TestAlertChannel sends a test message and tells the result (REQ-ALERT-001).
func (h *Handlers) TestAlertChannel(w http.ResponseWriter, r *http.Request, channelID gen.ChannelId) {
	if !h.needAlerts(w, r) {
		return
	}
	err := h.Alerts.SendTest(r.Context(), h.orgID(r), channelID)
	if errors.Is(err, alert.ErrNotFound) {
		notFoundError(w, r)
		return
	}
	if err != nil {
		WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ruleJSON is a map: the scope and params of a rule are open objects.
func ruleJSON(rl *alert.Rule) map[string]any {
	scope := map[string]string{}
	for k, v := range rl.Scope {
		scope[k] = v
	}
	params := map[string]any{}
	for k, v := range rl.Params {
		params[k] = v
	}
	ids := rl.ChannelIDs
	if ids == nil {
		ids = []string{}
	}
	out := map[string]any{"id": rl.ID, "name": rl.Name, "type": rl.Type, "scope": scope, "params": params, "severity": rl.Severity,
		"channel_ids": ids, "repeat_interval_seconds": rl.RepeatS, "enabled": rl.Enabled, "builtin": rl.Builtin}
	if rl.MutedUntil != nil {
		out["muted_until"] = rl.MutedUntil
		out["mute_reason"] = rl.MuteReason
	}
	return out
}

// ListAlertRules lists the rules, the default ones too.
func (h *Handlers) ListAlertRules(w http.ResponseWriter, r *http.Request) {
	if !h.needAlerts(w, r) {
		return
	}
	list, err := h.Alerts.Store.ListRules(r.Context(), h.orgID(r))
	if h.alertFailed(w, r, err) {
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, rl := range list {
		out = append(out, ruleJSON(rl))
	}
	WriteJSON(w, http.StatusOK, out)
}

// rulePatch is the body of a create or a change of a rule.
type rulePatch struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Scope      map[string]string `json:"scope"`
	Params     map[string]any    `json:"params"`
	Severity   string            `json:"severity"`
	ChannelIDs []string          `json:"channel_ids"`
	RepeatS    int               `json:"repeat_interval_seconds"`
	Enabled    *bool             `json:"enabled"`
}

func (b rulePatch) input() alert.RuleInput {
	return alert.RuleInput{Name: b.Name, Type: b.Type, Scope: b.Scope, Params: b.Params, Severity: b.Severity,
		ChannelIDs: b.ChannelIDs, RepeatS: b.RepeatS, Enabled: b.Enabled}
}

// CreateAlertRule makes a rule.
func (h *Handlers) CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	if !h.needAlerts(w, r) {
		return
	}
	var body rulePatch
	if !decodeJSON(w, r, &body) {
		return
	}
	rl, err := h.Alerts.Store.CreateRule(r.Context(), h.orgID(r), body.input(), h.Alerts.DefaultRepeatSeconds())
	if h.alertFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_rule.create", "alert_rule", rl.ID, map[string]any{"name": rl.Name, "type": rl.Type})
	WriteJSON(w, http.StatusCreated, ruleJSON(rl))
}

// UpdateAlertRule changes a rule.
func (h *Handlers) UpdateAlertRule(w http.ResponseWriter, r *http.Request, ruleID gen.RuleId) {
	if !h.needAlerts(w, r) {
		return
	}
	var body rulePatch
	if !decodeJSON(w, r, &body) {
		return
	}
	rl, err := h.Alerts.Store.UpdateRule(r.Context(), h.orgID(r), ruleID, body.input())
	if h.alertFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_rule.update", "alert_rule", rl.ID, map[string]any{"name": rl.Name})
	WriteJSON(w, http.StatusOK, ruleJSON(rl))
}

// DeleteAlertRule removes a rule.
func (h *Handlers) DeleteAlertRule(w http.ResponseWriter, r *http.Request, ruleID gen.RuleId) {
	if !h.needAlerts(w, r) {
		return
	}
	if h.alertFailed(w, r, h.Alerts.Store.DeleteRule(r.Context(), h.orgID(r), ruleID)) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_rule.delete", "alert_rule", ruleID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// MuteAlertRule mutes a rule until a time. The reason is audited (REQ-ALERT-005).
func (h *Handlers) MuteAlertRule(w http.ResponseWriter, r *http.Request, ruleID gen.RuleId) {
	if !h.needAlerts(w, r) {
		return
	}
	var body gen.MuteAlertRuleJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	now := h.Clock.Now()
	if !body.Until.After(now) {
		invalid(w, r, "until", "until must be in the future")
		return
	}
	if body.Reason == "" || len(body.Reason) > 500 {
		invalid(w, r, "reason", "the reason has 1 to 500 characters")
		return
	}
	rl, err := h.Alerts.Store.MuteRule(r.Context(), h.orgID(r), ruleID, body.Until, body.Reason, now)
	if h.alertFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "alert_rule.mute", "alert_rule", rl.ID, map[string]any{"until": body.Until.UTC().Format(time.RFC3339), "reason": body.Reason})
	WriteJSON(w, http.StatusOK, ruleJSON(rl))
}

// ListAlertInstances lists firing and recently resolved alerts.
func (h *Handlers) ListAlertInstances(w http.ResponseWriter, r *http.Request, params gen.ListAlertInstancesParams) {
	if !h.needAlerts(w, r) {
		return
	}
	state, cursor := "", ""
	if params.State != nil {
		state = string(*params.State)
	}
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	list, next, err := h.Alerts.Store.ListInstances(r.Context(), h.orgID(r), state, pageLimit(params.Limit), cursor, h.Clock.Now())
	if h.alertFailed(w, r, err) {
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, in := range list {
		m := map[string]any{"id": in.ID, "rule_id": in.RuleID, "rule_name": in.RuleName, "rule_type": in.RuleType, "severity": in.Severity,
			"fingerprint": in.Fingerprint, "state": in.State, "first_fired_at": in.FirstFiredAt, "muted": in.Muted, "suppressed": in.Suppressed,
			"details": in.Details}
		if in.LastNotifiedAt != nil {
			m["last_notified_at"] = in.LastNotifiedAt
		}
		if in.ResolvedAt != nil {
			m["resolved_at"] = in.ResolvedAt
		}
		items = append(items, m)
	}
	var nc *string
	if next != "" {
		nc = &next
	}
	WriteJSON(w, http.StatusOK, page[map[string]any]{Items: items, NextCursor: nc})
}
