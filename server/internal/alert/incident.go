// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/store"
)

// IncidentWindow is the correlation window: an open incident of a node closes when it has no firing alert and no
// new alert joined it for this long (12-observability section 5.1).
const IncidentWindow = 5 * time.Minute

// DefaultIncidentHold is how long the first message of a new incident waits, so that a cause that fires a little
// later still leads the one message.
const DefaultIncidentHold = 60 * time.Second

func (e *Engine) incidentHold() time.Duration {
	if e.IncidentHold != 0 {
		return max(e.IncidentHold, 0)
	}
	return DefaultIncidentHold
}

// CauseRank orders the alerts of an incident: a lower rank is a more probable cause. Node and disk problems come
// before the service and deployment failures they cause.
func CauseRank(rtype string, details map[string]any) int {
	switch rtype {
	case "node_down":
		return 0
	case "node_suspect":
		return 1
	case "disk_high":
		return 2
	case "memory_high":
		if detailStr(details, "service_id") == "" {
			return 3 // memory of the node
		}
		return 6
	case "cpu_high":
		return 4
	case "failover_blocked", "failover_started":
		return 5
	case "container_restart_loop":
		return 6
	case "service_unhealthy":
		return 7
	case "deployment_failed", "deployment_rolled_back":
		return 8
	case "log_match":
		return 9
	}
	return 10
}

// SortByCause puts the probable cause first: by rank, then by the first fire time.
func SortByCause(list []*Instance) {
	sort.SliceStable(list, func(a, b int) bool {
		ra, rb := CauseRank(list[a].RuleType, list[a].Details), CauseRank(list[b].RuleType, list[b].Details)
		if ra != rb {
			return ra < rb
		}
		if !list[a].FirstFiredAt.Equal(list[b].FirstFiredAt) {
			return list[a].FirstFiredAt.Before(list[b].FirstFiredAt)
		}
		return list[a].ID < list[b].ID
	})
}

// grouped tells if an alert of this type joins the incident of its node. A deployment that went live is good news.
func grouped(rtype string) bool { return rtype != "deployment_live" }

// nodeFor finds the node of an alert: the node of the event, else the node of the live container of the service.
func (e *Engine) nodeFor(ctx context.Context, q dbq, sc map[string]string, details map[string]any) (string, error) {
	if n := sc["node_id"]; n != "" {
		return n, nil
	}
	if n := detailStr(details, "node_id"); n != "" {
		return n, nil
	}
	sid := sc["service_id"]
	if sid == "" {
		sid = detailStr(details, "service_id")
	}
	if sid == "" {
		return "", nil
	}
	var node string
	err := q.QueryRow(ctx, `SELECT node_id FROM placements WHERE service_id = $1 ORDER BY (role = 'live') DESC, node_id LIMIT 1`, sid).Scan(&node)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return node, err
}

// attachIncident puts a firing instance into the open incident of its node, or opens one. An instance that is
// already in an open incident stays there: a new evaluation of a firing alert is not a new alert.
func (e *Engine) attachIncident(ctx context.Context, q dbq, r *Rule, fp string, details map[string]any, sc map[string]string) error {
	if !grouped(r.Type) {
		return nil
	}
	var instID string
	var cur *string
	var curOpen bool
	err := q.QueryRow(ctx, `SELECT i.id, i.incident_id, COALESCE(inc.state = 'open', false) FROM alert_instances i
		LEFT JOIN incidents inc ON inc.id = i.incident_id WHERE i.rule_id = $1 AND i.fingerprint = $2`, r.ID, fp).Scan(&instID, &cur, &curOpen)
	if err != nil {
		return err
	}
	if cur != nil && curOpen {
		return nil
	}
	node, err := e.nodeFor(ctx, q, sc, details)
	if err != nil || node == "" {
		return err
	}
	now := e.now()
	if _, err := q.Exec(ctx, `INSERT INTO incidents (id, org_id, node_id, state, opened_at, last_alert_at) VALUES ($1,$2,$3,'open',$4,$4)
		ON CONFLICT (org_id, node_id) WHERE state = 'open' DO NOTHING`, store.NewID("inc"), r.OrgID, node, now); err != nil {
		return err
	}
	var incID string
	if err := q.QueryRow(ctx, `SELECT id FROM incidents WHERE org_id = $1 AND node_id = $2 AND state = 'open' FOR UPDATE`, r.OrgID, node).Scan(&incID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE alert_instances SET incident_id = $2 WHERE id = $1`, instID, incID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE incidents SET last_alert_at = $2 WHERE id = $1`, incID, now); err != nil {
		return err
	}
	return e.rankIncident(ctx, q, incID)
}

// rankIncident chooses the probable cause of an incident and saves it with its one line text.
func (e *Engine) rankIncident(ctx context.Context, q dbq, incID string) error {
	list, err := incidentMembers(ctx, q, incID)
	if err != nil || len(list) == 0 {
		return err
	}
	SortByCause(list)
	root := list[0]
	text := e.describeInstance(ctx, q, root)
	_, err = q.Exec(ctx, `UPDATE incidents SET root_instance_id = $2, root_type = $3, root_cause = $4 WHERE id = $1`, incID, root.ID, root.RuleType, text)
	return err
}

// incidentMembers reads the alerts of an incident (no order).
func incidentMembers(ctx context.Context, q dbq, incID string) ([]*Instance, error) {
	rows, err := q.Query(ctx, `SELECT i.id, i.rule_id, r.name, r.type, r.severity, i.fingerprint, i.state, i.first_fired_at, i.last_notified_at, i.resolved_at,
		i.suppressed_by IS NOT NULL, COALESCE(i.details,'{}'::jsonb)
		FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE i.incident_id = $1`, incID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		var in Instance
		var det []byte
		if err := rows.Scan(&in.ID, &in.RuleID, &in.RuleName, &in.RuleType, &in.Severity, &in.Fingerprint, &in.State, &in.FirstFiredAt,
			&in.LastNotifiedAt, &in.ResolvedAt, &in.Suppressed, &det); err != nil {
			return nil, err
		}
		in.Details = map[string]any{}
		_ = json.Unmarshal(det, &in.Details)
		in.IncidentID = incID
		out = append(out, &in)
	}
	return out, rows.Err()
}

// describeInstance is the fixed one line text of an alert, with the names of its service and node.
func (e *Engine) describeInstance(ctx context.Context, q dbq, in *Instance) string {
	m := Message{RuleName: in.RuleName, RuleType: in.RuleType, Details: in.Details, Where: map[string]string{}}
	if sid := detailStr(in.Details, "service_id"); sid != "" {
		var name string
		if q.QueryRow(ctx, `SELECT name FROM services WHERE id = $1`, sid).Scan(&name) == nil {
			m.Where["service"] = name
		}
	}
	if nid := detailStr(in.Details, "node_id"); nid != "" {
		var name string
		if q.QueryRow(ctx, `SELECT name FROM nodes WHERE id = $1`, nid).Scan(&name) == nil {
			m.Where["node"] = name
		}
	}
	return describe(m)
}

// CloseIncidents closes the open incidents that have no firing alert and no new alert in the window.
func (e *Engine) CloseIncidents(ctx context.Context) error {
	now := e.now()
	_, err := e.DB.Exec(ctx, `UPDATE incidents SET state = 'resolved', resolved_at = $1
		WHERE state = 'open' AND last_alert_at <= $2
		AND NOT EXISTS (SELECT 1 FROM alert_instances i WHERE i.incident_id = incidents.id AND i.state = 'firing')`, now, now.Add(-IncidentWindow))
	return err
}

// incidentInfo is what the sender needs to know about the incident of an instance.
type incidentInfo struct {
	ID         string
	Open       bool
	OpenedAt   time.Time
	RootID     string
	RootFiring bool
	Members    int
}

// incidentOf reads the incident of an instance. Nil when the instance has none.
func incidentOf(ctx context.Context, q dbq, instanceID string) (*incidentInfo, error) {
	var inc incidentInfo
	var root *string
	err := q.QueryRow(ctx, `SELECT inc.id, inc.state = 'open', inc.opened_at, inc.root_instance_id,
		COALESCE((SELECT ri.state = 'firing' FROM alert_instances ri WHERE ri.id = inc.root_instance_id), false),
		(SELECT count(*) FROM alert_instances m WHERE m.incident_id = inc.id)
		FROM alert_instances i JOIN incidents inc ON inc.id = i.incident_id WHERE i.id = $1`, instanceID).
		Scan(&inc.ID, &inc.Open, &inc.OpenedAt, &root, &inc.RootFiring, &inc.Members)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if root != nil {
		inc.RootID = *root
	}
	return &inc, nil
}

// relatedOf gives the one line texts of the other alerts of the incident whose root is this instance, cause first.
func (e *Engine) relatedOf(ctx context.Context, q dbq, instanceID string) (string, []string, int, error) {
	inc, err := incidentOf(ctx, q, instanceID)
	if err != nil || inc == nil || inc.RootID != instanceID || inc.Members < 2 {
		return "", nil, 0, err
	}
	list, err := incidentMembers(ctx, q, inc.ID)
	if err != nil {
		return "", nil, 0, err
	}
	SortByCause(list)
	var out []string
	for _, in := range list {
		if in.ID == instanceID {
			continue
		}
		if len(out) == 10 {
			break
		}
		text := e.describeInstance(ctx, q, in)
		if in.State == "resolved" {
			text += " (resolved)"
		}
		out = append(out, text)
	}
	return inc.ID, out, len(list) - 1, nil
}

// relatedText is the block of the related alerts in a plain text message.
func (m *Message) relatedText() string {
	if m.RelatedCount == 0 {
		return ""
	}
	s := fmt.Sprintf("Probable cause of %d related alerts on this node:\n", m.RelatedCount)
	for _, r := range m.Related {
		s += "  - " + truncate(r, 300) + "\n"
	}
	if more := m.RelatedCount - len(m.Related); more > 0 {
		s += fmt.Sprintf("  and %d more\n", more)
	}
	return s
}
