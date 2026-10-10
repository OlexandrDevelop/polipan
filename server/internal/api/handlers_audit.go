// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/audit"
)

func auditEventJSON(s audit.Stored) gen.AuditEvent {
	typ := gen.AuditEventActorType(s.ActorType)
	e := gen.AuditEvent{Seq: ptr(int(s.Seq)), Id: ptr(s.ID), ActorType: &typ, Action: ptr(s.Action), TargetType: ptr(s.TargetType),
		Hash: ptr(s.Hash), CreatedAt: ptr(s.CreatedAt)}
	for dst, src := range map[**string]string{&e.ActorId: s.ActorID, &e.TargetId: s.TargetID, &e.ProjectId: s.ProjectID,
		&e.Ip: s.IP, &e.UserAgent: s.UserAgent, &e.RequestId: s.RequestID} {
		if src != "" {
			*dst = ptr(src)
		}
	}
	var diff map[string]any
	if json.Unmarshal(s.Diff, &diff) == nil && diff != nil {
		e.Diff = &diff
	}
	return e
}

// ListAuditEvents searches the audit log, newest first.
func (h *Handlers) ListAuditEvents(w http.ResponseWriter, r *http.Request, params gen.ListAuditEventsParams) {
	p := PrincipalFrom(r.Context())
	cursor, err := audit.SeqCursor(derefStr(params.Cursor))
	if err != nil {
		invalid(w, r, "cursor", "cursor is not valid")
		return
	}
	f := audit.Filter{ActorID: derefStr(params.ActorId), Action: derefStr(params.Action), TargetType: derefStr(params.TargetType),
		TargetID: derefStr(params.TargetId), ProjectID: derefStr(params.ProjectId), From: params.From, To: params.To}
	items, next, err := audit.Search(r.Context(), h.Store.Pool, p.OrgID, f, cursor, pageLimit(params.Limit))
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := page[gen.AuditEvent]{Items: make([]gen.AuditEvent, 0, len(items))}
	for _, s := range items {
		out.Items = append(out.Items, auditEventJSON(s))
	}
	if next > 0 {
		out.NextCursor = ptr(strconv.FormatInt(next, 10))
	}
	WriteJSON(w, http.StatusOK, out)
}

// ExportAuditEvents streams the audit log as JSON lines. The export is audited first (REQ-AUDIT-001).
func (h *Handlers) ExportAuditEvents(w http.ResponseWriter, r *http.Request, params gen.ExportAuditEventsParams) {
	p := PrincipalFrom(r.Context())
	if err := audit.Write(r.Context(), h.Store.Pool, audit.Event{OrgID: p.OrgID, Action: "audit.export", TargetType: "audit",
		Diff: map[string]any{"from": params.From, "to": params.To}}); err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	err := audit.Export(r.Context(), h.Store.Pool, p.OrgID, params.From, params.To, func(s audit.Stored) error {
		return enc.Encode(auditEventJSON(s))
	})
	if err != nil {
		// The status is sent already. The client sees a cut stream.
		panic(http.ErrAbortHandler)
	}
}

// VerifyAuditChain walks the hash chain and reports the first broken seq.
func (h *Handlers) VerifyAuditChain(w http.ResponseWriter, r *http.Request) {
	res, err := audit.Verify(r.Context(), h.Store.Pool, PrincipalFrom(r.Context()).OrgID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := map[string]any{"ok": res.OK, "checked_rows": res.CheckedRows}
	if !res.OK {
		out["first_broken_seq"] = res.FirstBrokenSeq
	}
	WriteJSON(w, http.StatusOK, out)
}
