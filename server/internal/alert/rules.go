// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/obs"
)

// finding is one target that a rule found now.
type finding struct {
	target  string // goes into the fingerprint
	scope   map[string]string
	details map[string]any
}

// EvalRules evaluates the rules that are not event based: the thresholds, the restart loop and the log match from
// VictoriaMetrics and VictoriaLogs, and the certificate, backup and audit rules from the database.
// When VictoriaMetrics does not answer, its rules are skipped and monitoring_unavailable is raised (F09 edge case).
func (e *Engine) EvalRules(ctx context.Context) {
	metricsUp := e.Backends.MetricsEnabled() && e.Backends.MetricsUp(ctx)
	e.monitoringDown.Store(e.Backends.MetricsEnabled() && !metricsUp)
	logsUp := e.Backends.LogsEnabled() && e.Backends.LogsUp(ctx)
	rows, err := e.DB.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE enabled AND type = ANY($1)`,
		[]string{"cpu_high", "memory_high", "disk_high", "container_restart_loop", "log_match", "cert_expiring", "cert_failed", "backup_missing", "audit_chain_broken"})
	if err != nil {
		e.log().Warn("alert: rules", "err", err)
		return
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return
		}
		rules = append(rules, r)
	}
	rows.Close()
	for _, r := range rules {
		var found []finding
		var err error
		ok := true
		switch r.Type {
		case "cpu_high", "memory_high", "disk_high":
			if !metricsUp {
				continue
			}
			found, err = e.evalThreshold(ctx, r)
		case "container_restart_loop":
			if !metricsUp {
				continue
			}
			found, err = e.evalRestartLoop(ctx, r)
		case "log_match":
			if !logsUp {
				continue
			}
			found, err = e.evalLogMatch(ctx, r)
		case "cert_expiring", "cert_failed":
			found, err = e.evalCerts(ctx, r)
		case "backup_missing":
			found, err = e.evalBackupMissing(ctx, r)
		case "audit_chain_broken":
			found, ok, err = e.evalAudit(ctx, r)
		}
		if err != nil {
			if ctx.Err() == nil {
				e.log().Warn("alert: evaluate rule", "rule", r.Name, "type", r.Type, "err", err)
			}
			continue
		}
		if !ok {
			continue
		}
		if err := e.reconcile(ctx, r, found); err != nil && ctx.Err() == nil {
			e.log().Warn("alert: save rule result", "rule", r.Name, "err", err)
		}
	}
}

// reconcile makes the instances of a rule match what the rule found: new targets fire, targets that are gone resolve.
func (e *Engine) reconcile(ctx context.Context, r *Rule, found []finding) error {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	want := map[string]bool{}
	for _, f := range found {
		fp := r.Type + ":" + f.target
		want[fp] = true
		if err := e.fire(ctx, tx, r, fp, f.details, f.scope); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT fingerprint FROM alert_instances WHERE rule_id = $1 AND state = 'firing'`, r.ID)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			rows.Close()
			return err
		}
		if !want[fp] {
			gone = append(gone, fp)
		}
	}
	rows.Close()
	for _, fp := range gone {
		if err := e.resolve(ctx, tx, r.ID, fp, nil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- thresholds ----

type nodeRow struct {
	id, name            string
	cpuMillis, memBytes float64
	scope               map[string]string
}

// targetNodes are the nodes a rule looks at: the node of the scope, the nodes under a service, environment or
// project of the scope, or every node of the org that is not down.
func (e *Engine) targetNodes(ctx context.Context, r *Rule) ([]nodeRow, error) {
	q := `SELECT n.id, n.name, COALESCE((n.capacity->>'cpu_millis')::float8, 0), COALESCE((n.capacity->>'memory_bytes')::float8, 0)
		FROM nodes n WHERE n.org_id = $1 AND n.deleted_at IS NULL AND n.state IN ('ready','cordoned','draining','suspect')`
	args := []any{r.OrgID}
	if id := r.Scope["node_id"]; id != "" {
		args = append(args, id)
		q += fmt.Sprintf(` AND n.id = $%d`, len(args))
	}
	for _, kv := range [][2]string{{"service_id", "s.id"}, {"environment_id", "s.environment_id"}, {"project_id", "en.project_id"}} {
		if id := r.Scope[kv[0]]; id != "" {
			args = append(args, id)
			q += fmt.Sprintf(` AND n.id IN (SELECT p.node_id FROM placements p JOIN services s ON s.id = p.service_id JOIN environments en ON en.id = s.environment_id WHERE %s = $%d)`, kv[1], len(args))
		}
	}
	rows, err := e.DB.Query(ctx, q+` ORDER BY n.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nodeRow
	for rows.Next() {
		var n nodeRow
		if err := rows.Scan(&n.id, &n.name, &n.cpuMillis, &n.memBytes); err != nil {
			return nil, err
		}
		n.scope = map[string]string{"node_id": n.id}
		out = append(out, n)
	}
	return out, rows.Err()
}

func paramNum(p map[string]any, k string, def float64) float64 {
	if v, ok := p[k].(float64); ok {
		return v
	}
	return def
}

func (e *Engine) evalThreshold(ctx context.Context, r *Rule) ([]finding, error) {
	threshold := paramNum(r.Params, "threshold", 90)
	forS := int(paramNum(r.Params, "for_seconds", 300))
	win := fmt.Sprintf("%ds", forS)
	var out []finding
	if r.Type == "memory_high" && (r.Scope["service_id"] != "" || r.Scope["environment_id"] != "" || r.Scope["project_id"] != "") {
		return e.evalServiceMemory(ctx, r, threshold, win, forS)
	}
	nodes, err := e.targetNodes(ctx, r)
	if err != nil {
		return nil, err
	}
	minSamples := float64(forS) / obs.ScrapeInterval.Seconds() * 0.6 // the series must cover most of the window
	for _, n := range nodes {
		sel := "{node=" + strconv.Quote(n.id) + "}"
		var expr, count string
		switch r.Type {
		case "cpu_high":
			if n.cpuMillis <= 0 {
				continue
			}
			expr = fmt.Sprintf("min_over_time((polipan_node_cpu_usage_millis%s / %g * 100)[%s:15s])", sel, n.cpuMillis, win)
			count = fmt.Sprintf("count_over_time(polipan_node_cpu_usage_millis%s[%s])", sel, win)
		case "memory_high":
			if n.memBytes <= 0 {
				continue
			}
			expr = fmt.Sprintf("min_over_time((polipan_node_memory_bytes%s / %g * 100)[%s:15s])", sel, n.memBytes, win)
			count = fmt.Sprintf("count_over_time(polipan_node_memory_bytes%s[%s])", sel, win)
		case "disk_high":
			expr = fmt.Sprintf("min_over_time((polipan_node_disk_used_bytes%s / polipan_node_disk_total_bytes%s * 100)[%s:15s])", sel, sel, win)
			count = fmt.Sprintf("count_over_time(polipan_node_disk_used_bytes%s[%s])", sel, win)
		}
		cs, err := e.Backends.Query(ctx, count, e.now())
		if err != nil {
			return nil, err
		}
		if len(cs) == 0 || cs[0].Value < minSamples {
			continue // no data or not enough of it for the "for" time
		}
		vs, err := e.Backends.Query(ctx, expr, e.now())
		if err != nil {
			return nil, err
		}
		if len(vs) == 0 || vs[0].Value <= threshold {
			continue
		}
		out = append(out, finding{target: n.id, scope: n.scope,
			details: map[string]any{"node_id": n.id, "value": round1(vs[0].Value), "threshold": threshold, "for_seconds": forS}})
	}
	return out, nil
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

type svcRow struct{ id, project, env string }

// targetServices are the live services of the org inside the scope.
func (e *Engine) targetServices(ctx context.Context, r *Rule) ([]svcRow, error) {
	q := `SELECT s.id, en.project_id, en.id FROM services s JOIN environments en ON en.id = s.environment_id JOIN projects p ON p.id = en.project_id
		WHERE p.org_id = $1 AND s.deleted_at IS NULL`
	args := []any{r.OrgID}
	for _, kv := range [][2]string{{"service_id", "s.id"}, {"environment_id", "en.id"}, {"project_id", "p.id"}} {
		if id := r.Scope[kv[0]]; id != "" {
			args = append(args, id)
			q += fmt.Sprintf(` AND %s = $%d`, kv[1], len(args))
		}
	}
	rows, err := e.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []svcRow
	for rows.Next() {
		var s svcRow
		if err := rows.Scan(&s.id, &s.project, &s.env); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (e *Engine) evalServiceMemory(ctx context.Context, r *Rule, threshold float64, win string, forS int) ([]finding, error) {
	svcs, err := e.targetServices(ctx, r)
	if err != nil {
		return nil, err
	}
	var out []finding
	for _, s := range svcs {
		sel := "{service=" + strconv.Quote(s.id) + "}"
		expr := fmt.Sprintf("min_over_time((sum(polipan_container_memory_bytes%s) / (sum(polipan_container_memory_limit_bytes%s) > 0) * 100)[%s:15s])", sel, sel, win)
		vs, err := e.Backends.Query(ctx, expr, e.now())
		if err != nil {
			return nil, err
		}
		if len(vs) == 0 || vs[0].Value <= threshold {
			continue
		}
		out = append(out, finding{target: s.id, scope: map[string]string{"service_id": s.id, "project_id": s.project, "environment_id": s.env},
			details: map[string]any{"service_id": s.id, "value": round1(vs[0].Value), "threshold": threshold, "for_seconds": forS}})
	}
	return out, nil
}

// evalRestartLoop finds the services with 5 or more restarts in 10 minutes (REQ-ALERT-002).
func (e *Engine) evalRestartLoop(ctx context.Context, r *Rule) ([]finding, error) {
	vs, err := e.Backends.Query(ctx, `sum by (service) (increase(polipan_container_restarts_total{org=`+strconv.Quote(r.OrgID)+`}[10m])) >= 5`, e.now())
	if err != nil {
		return nil, err
	}
	svcs, err := e.targetServices(ctx, r)
	if err != nil {
		return nil, err
	}
	inScope := map[string]svcRow{}
	for _, s := range svcs {
		inScope[s.id] = s
	}
	var out []finding
	for _, v := range vs {
		s, ok := inScope[v.Labels["service"]]
		if !ok {
			continue
		}
		out = append(out, finding{target: s.id, scope: map[string]string{"service_id": s.id, "project_id": s.project, "environment_id": s.env},
			details: map[string]any{"service_id": s.id, "restarts": int(v.Value + 0.5)}})
	}
	return out, nil
}

// evalLogMatch counts the log lines that match the query of the rule in its window (REQ-OBS-015).
func (e *Engine) evalLogMatch(ctx context.Context, r *Rule) ([]finding, error) {
	query, _ := r.Params["query"].(string)
	forS := int(paramNum(r.Params, "for_seconds", 300))
	need := int(paramNum(r.Params, "count", 1))
	f := obs.LogFilter{OrgID: r.OrgID, ProjectID: r.Scope["project_id"], EnvironmentID: r.Scope["environment_id"], ServiceID: r.Scope["service_id"],
		NodeID: r.Scope["node_id"], Text: query}
	now := e.now()
	n, err := e.Backends.CountLogs(ctx, f, now.Add(-time.Duration(forS)*time.Second), now)
	if err != nil {
		return nil, err
	}
	if n < need {
		return nil, nil
	}
	sc := map[string]string{}
	for k, v := range r.Scope {
		sc[k] = v
	}
	return []finding{{target: r.ID, scope: sc, details: map[string]any{"query": query, "count": n, "service_id": r.Scope["service_id"], "for_seconds": forS}}}, nil
}

// ---- database rules ----

func (e *Engine) evalCerts(ctx context.Context, r *Rule) ([]finding, error) {
	q := `SELECT d.id, d.host, d.tls_status, d.tls_expires_at, COALESCE(d.tls_error,''), s.id, en.id, en.project_id FROM domains d
		JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL JOIN environments en ON en.id = s.environment_id JOIN projects p ON p.id = en.project_id
		WHERE p.org_id = $1 AND d.https`
	rows, err := e.DB.Query(ctx, q, r.OrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := paramNum(r.Params, "days", 14)
	now := e.now()
	var out []finding
	for rows.Next() {
		var id, host, status, tlsErr, sid, eid, pid string
		var exp *time.Time
		if err := rows.Scan(&id, &host, &status, &exp, &tlsErr, &sid, &eid, &pid); err != nil {
			return nil, err
		}
		sc := map[string]string{"service_id": sid, "environment_id": eid, "project_id": pid}
		if !scopeMatches(r.Scope, sc) {
			continue
		}
		switch r.Type {
		case "cert_expiring":
			if status == "issued" && exp != nil && exp.Sub(now) < time.Duration(days*24)*time.Hour {
				out = append(out, finding{target: id, scope: sc, details: map[string]any{"service_id": sid, "host": host,
					"days_left": int(exp.Sub(now).Hours() / 24), "expires_at": exp.Format(time.RFC3339)}})
			}
		case "cert_failed":
			if status == "failed" {
				out = append(out, finding{target: id, scope: sc, details: map[string]any{"service_id": sid, "host": host, "error": tlsErr}})
			}
		}
	}
	return out, rows.Err()
}

// evalBackupMissing finds the policies without a successful backup in twice their schedule interval (REQ-ALERT-002).
func (e *Engine) evalBackupMissing(ctx context.Context, r *Rule) ([]finding, error) {
	rows, err := e.DB.Query(ctx, `SELECT bp.id, bp.schedule, bp.created_at, COALESCE(bp.service_id,''), COALESCE(en.id,''), COALESCE(en.project_id,''),
		(SELECT max(finished_at) FROM backup_runs br WHERE br.policy_id = bp.id AND br.state = 'succeeded')
		FROM backup_policies bp LEFT JOIN services s ON s.id = bp.service_id LEFT JOIN environments en ON en.id = s.environment_id
		WHERE bp.org_id = $1 AND bp.enabled AND (bp.service_id IS NULL OR s.deleted_at IS NULL)`, r.OrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := e.now()
	var out []finding
	for rows.Next() {
		var id, schedule, sid, eid, pid string
		var created time.Time
		var last *time.Time
		if err := rows.Scan(&id, &schedule, &created, &sid, &eid, &pid, &last); err != nil {
			return nil, err
		}
		cron, err := backup.ParseCron(schedule)
		if err != nil {
			continue
		}
		next := cron.Next(now)
		interval := cron.Next(next).Sub(next)
		if interval <= 0 {
			continue
		}
		since := created
		if last != nil {
			since = *last
		}
		if now.Sub(since) <= 2*interval {
			continue
		}
		sc := map[string]string{}
		if sid != "" {
			sc = map[string]string{"service_id": sid, "environment_id": eid, "project_id": pid}
		}
		if !scopeMatches(r.Scope, sc) {
			continue
		}
		out = append(out, finding{target: id, scope: sc, details: map[string]any{"service_id": sid, "policy_id": id, "since": since.Format(time.RFC3339)}})
	}
	return out, rows.Err()
}

// evalAudit checks the audit chain of the org about every 10 minutes. ok is false when it did not run now.
func (e *Engine) evalAudit(ctx context.Context, r *Rule) ([]finding, bool, error) {
	if e.lastAuditCheck == nil {
		e.lastAuditCheck = map[string]time.Time{}
	}
	if t, ok := e.lastAuditCheck[r.OrgID]; ok && e.now().Sub(t) < 10*time.Minute {
		return nil, false, nil
	}
	e.lastAuditCheck[r.OrgID] = e.now()
	res, err := audit.Verify(ctx, e.DB, r.OrgID)
	if err != nil {
		return nil, false, err
	}
	if res.OK {
		return nil, true, nil
	}
	return []finding{{target: r.OrgID, scope: map[string]string{}, details: map[string]any{"first_broken_seq": res.FirstBrokenSeq}}}, true, nil
}
