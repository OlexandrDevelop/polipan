// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/store"
)

// diskLevels are the built-in disk rules: two warnings and one critical level (12-observability section 7.1).
// Each one looks at every file system of the node: the root, the Docker data-root and the BuildKit state.
var diskLevels = []defaultRule{
	{"Disk 70%", "disk_high", "warning", map[string]any{"threshold": 70, "for_seconds": 300}},
	{"Disk 85%", "disk_high", "warning", map[string]any{"threshold": 85, "for_seconds": 300}},
	{"Disk 95%", "disk_high", "critical", map[string]any{"threshold": 95, "for_seconds": 120}},
}

// oldDiskRule is the name of the one built-in disk rule before the levels.
const oldDiskRule = "Disk high"

// upgradeDiskRules gives an org that still has the old built-in "Disk high" rule the three disk levels, with the
// channels, repeat time and on/off state of the old rule. The old rule is deleted when it still has its default
// values. When a person changed it, it stays as a normal (not built-in) rule. So the upgrade runs once per org.
func (e *Engine) upgradeDiskRules(ctx context.Context, org string) error {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	var raw []byte
	var chans []string
	var repeat int
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT id, params, channel_ids, repeat_interval_s, enabled FROM alert_rules
		WHERE org_id = $1 AND builtin AND type = 'disk_high' AND name = $2 FOR UPDATE`, org, oldDiskRule).
		Scan(&id, &raw, &chans, &repeat, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range diskLevels {
		pb, _ := json.Marshal(d.params)
		if _, err := tx.Exec(ctx, `INSERT INTO alert_rules (id, org_id, name, type, scope, params, severity, channel_ids, repeat_interval_s, enabled, builtin)
			VALUES ($1,$2,$3,$4,'{}',$5,$6,$7,$8,$9,true) ON CONFLICT (org_id, name) DO NOTHING`,
			store.NewID("alr"), org, d.name, d.typ, pb, d.severity, chans, repeat, enabled); err != nil {
			return err
		}
	}
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if paramNum(p, "threshold", 90) == 90 && paramNum(p, "for_seconds", 300) == 300 {
		_, err = tx.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE alert_rules SET builtin = false WHERE id = $1`, id)
	}
	if err != nil {
		return err
	}
	e.log().Info("alert: disk rule changed to the levels 70/85/95", "org", org)
	return tx.Commit(ctx)
}

// evalDisk checks every file system of a node (polipan_node_fs_*). ok is false when the node sends no such series
// (an older agent) or none of them covers the window yet: the caller then uses polipan_node_disk_* of the root.
func (e *Engine) evalDisk(ctx context.Context, n nodeRow, threshold float64, forS int, win string, minSamples float64) (out []finding, ok bool, err error) {
	sel := "{node=" + strconv.Quote(n.id) + "}"
	used, avail, size := "polipan_node_fs_used_bytes"+sel, "polipan_node_fs_avail_bytes"+sel, "polipan_node_fs_size_bytes"+sel
	counts, err := e.Backends.Query(ctx, fmt.Sprintf("count_over_time(%s[%s])", used, win), e.now())
	if err != nil || len(counts) == 0 {
		return nil, false, err
	}
	enough := map[string]bool{}
	for _, c := range counts {
		if c.Value >= minSamples {
			enough[c.Labels["mountpoint"]] = true
		}
	}
	if len(enough) == 0 {
		// The series are new (the agent was just updated): the root disk series covers the window, so a firing
		// alert does not resolve and fire again.
		return nil, false, nil
	}
	// The used share like df shows it: the root reserve is not free space for the services.
	pct, err := e.Backends.Query(ctx, fmt.Sprintf("min_over_time((%s / (%s + %s) * 100)[%s:15s])", used, used, avail, win), e.now())
	if err != nil {
		return nil, true, err
	}
	now := map[string]map[string]float64{} // mountpoint -> used/avail/size bytes now
	roles := map[string]string{}
	for k, q := range map[string]string{"used": used, "avail": avail, "size": size} {
		vs, err := e.Backends.Query(ctx, q, e.now())
		if err != nil {
			return nil, true, err
		}
		for _, v := range vs {
			m := v.Labels["mountpoint"]
			if now[m] == nil {
				now[m] = map[string]float64{}
			}
			now[m][k] = v.Value
			roles[m] = v.Labels["roles"]
		}
	}
	seen := map[string]bool{}
	for _, v := range pct {
		m := v.Labels["mountpoint"]
		if m == "" || seen[m] || !enough[m] || v.Value <= threshold || now[m] == nil {
			continue
		}
		seen[m] = true
		// The root keeps the old fingerprint (the node id), so an alert that fires already goes on.
		target := n.id
		if m != "/" {
			target += ":" + m
		}
		out = append(out, finding{target: target, scope: n.scope, details: map[string]any{
			"node_id": n.id, "value": round1(v.Value), "threshold": threshold, "for_seconds": forS,
			"filesystem": m, "roles": roles[m],
			"used_bytes": now[m]["used"], "avail_bytes": now[m]["avail"], "size_bytes": now[m]["size"]}})
	}
	return out, true, nil
}

// describeDisk is the text of a disk alert with the file system and the bytes.
func describeDisk(node string, d map[string]any, num func(string) string) string {
	fs := detailStr(d, "filesystem")
	if fs == "" {
		return fmt.Sprintf("Disk of node %s is at %s%% (limit %s%%)", orStr(node, "?"), num("value"), num("threshold"))
	}
	roles := strings.ReplaceAll(detailStr(d, "roles"), ",", ", ")
	b := func(k string) string { v, _ := d[k].(float64); return fmtBytes(v) }
	return fmt.Sprintf("Disk %s (%s) of node %s is at %s%% (limit %s%%): %s used, %s free of %s",
		fs, orStr(roles, "?"), orStr(node, "?"), num("value"), num("threshold"), b("used_bytes"), b("avail_bytes"), b("size_bytes"))
}

// fmtBytes writes bytes in binary units, for example 6.2 GiB.
func fmtBytes(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", v)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
