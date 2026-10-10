// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Message is what a channel gets. The text is fixed per rule type and channel kind (12-observability section 5).
type Message struct {
	Kind        string // firing, repeat or resolved
	Test        bool
	InstanceID  string
	RuleID      string
	RuleName    string
	RuleType    string
	Fingerprint string
	Severity    string
	What        string
	Where       map[string]string // org, project, environment, service, node
	Since       time.Time
	Link        string
	Reason      string   // failure reason code of a deployment
	LastLines   []string // last log lines of a failed deployment
	Count       int      // messages sent for this alert, with this one
	Details     map[string]any
}

// buildMessage reads the instance and the names around it.
func (e *Engine) buildMessage(ctx context.Context, instanceID, kind string) (*Message, error) {
	var m Message
	var raw []byte
	var orgID string
	err := e.DB.QueryRow(ctx, `SELECT i.id, i.rule_id, i.fingerprint, i.first_fired_at, i.notify_count, COALESCE(i.details,'{}'::jsonb),
		r.name, r.type, r.severity, r.org_id FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE i.id = $1`, instanceID).
		Scan(&m.InstanceID, &m.RuleID, &m.Fingerprint, &m.Since, &m.Count, &raw, &m.RuleName, &m.RuleType, &m.Severity, &orgID)
	if err != nil {
		return nil, err
	}
	m.Kind = kind
	m.Details = map[string]any{}
	_ = json.Unmarshal(raw, &m.Details)
	m.Where = map[string]string{}
	var orgSlug string
	_ = e.DB.QueryRow(ctx, `SELECT slug FROM orgs WHERE id = $1`, orgID).Scan(&orgSlug)
	m.Where["org"] = orgSlug
	base := strings.TrimRight(e.ExternalURL, "/")
	m.Link = base + "/o/" + orgSlug
	if sid := detailStr(m.Details, "service_id"); sid != "" {
		var sname, ename, eslug, pname, pslug, sslug string
		err := e.DB.QueryRow(ctx, `SELECT s.name, s.slug, en.name, en.slug, p.name, p.slug FROM services s
			JOIN environments en ON en.id = s.environment_id JOIN projects p ON p.id = en.project_id WHERE s.id = $1`, sid).
			Scan(&sname, &sslug, &ename, &eslug, &pname, &pslug)
		if err == nil {
			m.Where["service"], m.Where["environment"], m.Where["project"] = sname, ename, pname
			m.Link = fmt.Sprintf("%s/o/%s/p/%s/e/%s/s/%s", base, orgSlug, pslug, eslug, sslug)
			if dep := detailStr(m.Details, "deployment_id"); dep != "" {
				m.Link += "/deployments/" + dep
			}
		} else if err != pgx.ErrNoRows {
			return nil, err
		}
	}
	if nid := detailStr(m.Details, "node_id"); nid != "" {
		var nname string
		if err := e.DB.QueryRow(ctx, `SELECT name FROM nodes WHERE id = $1`, nid).Scan(&nname); err == nil {
			m.Where["node"] = nname
			if m.Where["service"] == "" {
				m.Link = base + "/o/" + orgSlug + "/nodes/" + nid
			}
		}
	}
	m.Reason = detailStr(m.Details, "reason")
	if lines, ok := m.Details["last_lines"].([]any); ok {
		for _, l := range lines {
			if s, ok := l.(string); ok {
				m.LastLines = append(m.LastLines, s)
			}
		}
	}
	m.What = describe(m)
	return &m, nil
}

// describe is the fixed one line text of a rule type.
func describe(m Message) string {
	svc, node := m.Where["service"], m.Where["node"]
	d := m.Details
	num := func(k string) string {
		switch v := d[k].(type) {
		case float64:
			if v == float64(int64(v)) {
				return fmt.Sprintf("%d", int64(v))
			}
			return fmt.Sprintf("%.1f", v)
		case string:
			return v
		}
		return ""
	}
	switch m.RuleType {
	case "node_suspect":
		return fmt.Sprintf("Node %s does not answer", orStr(node, "?"))
	case "node_down":
		return fmt.Sprintf("Node %s is down", orStr(node, "?"))
	case "service_unhealthy":
		return fmt.Sprintf("Service %s is unhealthy", orStr(svc, "?"))
	case "deployment_failed":
		return fmt.Sprintf("Deployment of %s failed", orStr(svc, "?"))
	case "deployment_rolled_back":
		return fmt.Sprintf("Deployment of %s was rolled back", orStr(svc, "?"))
	case "deployment_live":
		return fmt.Sprintf("Deployment of %s is live", orStr(svc, "?"))
	case "cpu_high":
		return fmt.Sprintf("CPU of node %s is at %s%% (limit %s%%)", orStr(node, "?"), num("value"), num("threshold"))
	case "memory_high":
		if node != "" {
			return fmt.Sprintf("Memory of node %s is at %s%% (limit %s%%)", node, num("value"), num("threshold"))
		}
		return fmt.Sprintf("Memory of %s is at %s%% of its limit (limit %s%%)", orStr(svc, "?"), num("value"), num("threshold"))
	case "disk_high":
		return fmt.Sprintf("Disk of node %s is at %s%% (limit %s%%)", orStr(node, "?"), num("value"), num("threshold"))
	case "cert_expiring":
		return fmt.Sprintf("The certificate of %s ends in %s days", orStr(detailStr(d, "host"), "?"), num("days_left"))
	case "cert_failed":
		return fmt.Sprintf("The certificate of %s could not be issued", orStr(detailStr(d, "host"), "?"))
	case "backup_failed":
		return fmt.Sprintf("A backup of %s failed", orStr(svc, "the control plane"))
	case "restore_check_failed":
		return fmt.Sprintf("A restore check of %s failed", orStr(svc, "the control plane"))
	case "restore_check_overdue":
		return fmt.Sprintf("No restore check of %s passed in time", orStr(svc, "the control plane"))
	case "backup_missing":
		return fmt.Sprintf("No backup of %s succeeded in twice the schedule interval", orStr(svc, "the control plane"))
	case "audit_chain_broken":
		return "The audit log chain is broken"
	case "failover_started":
		return fmt.Sprintf("Failover of %s started", orStr(svc, "?"))
	case "failover_blocked":
		return fmt.Sprintf("Failover of %s is blocked", orStr(svc, "?"))
	case "container_restart_loop":
		return fmt.Sprintf("%s restarted %s times in 10 minutes", orStr(svc, "?"), num("restarts"))
	case "log_match":
		return fmt.Sprintf("%s log lines match %q in %s", num("count"), detailStr(d, "query"), orStr(svc, "the logs"))
	}
	return m.RuleName
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (m *Message) subject() string {
	prefix := strings.ToUpper(m.Severity)
	switch m.Kind {
	case KindResolved:
		prefix = "RESOLVED"
	case KindRepeat:
		prefix = strings.ToUpper(m.Severity) + " (still firing)"
	}
	if m.Test {
		prefix = "TEST"
	}
	return fmt.Sprintf("[%s] %s: %s", prefix, m.RuleName, m.What)
}

func (m *Message) whereLine() string {
	var parts []string
	for _, k := range []string{"project", "environment", "service", "node"} {
		if v := m.Where[k]; v != "" {
			parts = append(parts, k+" "+v)
		}
	}
	return strings.Join(parts, ", ")
}

// text is the plain text body (Telegram, email).
func (m *Message) text() string {
	var sb strings.Builder
	sb.WriteString(m.subject() + "\n")
	if w := m.whereLine(); w != "" {
		sb.WriteString("Where: " + w + "\n")
	}
	sb.WriteString("Since: " + m.Since.UTC().Format("2006-01-02 15:04:05 UTC") + "\n")
	if m.Reason != "" {
		sb.WriteString("Reason: " + m.Reason + "\n")
	}
	if d := detailStr(m.Details, "detail"); d != "" {
		sb.WriteString("Detail: " + truncate(d, 500) + "\n")
	}
	if d := detailStr(m.Details, "error"); d != "" && m.Reason == "" {
		sb.WriteString("Error: " + truncate(d, 500) + "\n")
	}
	if len(m.LastLines) > 0 {
		sb.WriteString("Last lines:\n")
		n := len(m.LastLines)
		for _, l := range m.LastLines[max(0, n-10):] {
			sb.WriteString("  " + truncate(l, 300) + "\n")
		}
	}
	if m.Link != "" {
		sb.WriteString("Link: " + m.Link + "\n")
	}
	return sb.String()
}

// webhookBody is the JSON body of a webhook channel.
func (m *Message) webhookBody(now time.Time) map[string]any {
	where := map[string]string{}
	for k, v := range m.Where {
		where[k] = v
	}
	keys := make([]string, 0, len(m.Details))
	for k := range m.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	details := map[string]any{}
	for _, k := range keys {
		details[k] = m.Details[k]
	}
	return map[string]any{
		"event": "alert." + m.Kind, "test": m.Test, "severity": m.Severity, "state": map[bool]string{true: "resolved", false: "firing"}[m.Kind == KindResolved],
		"rule": map[string]string{"id": m.RuleID, "name": m.RuleName, "type": m.RuleType}, "what": m.What, "where": where,
		"since": m.Since.UTC().Format(time.RFC3339), "link": m.Link, "reason": m.Reason, "instance_id": m.InstanceID,
		"fingerprint": m.Fingerprint, "details": details, "sent_at": now.UTC().Format(time.RFC3339),
	}
}
