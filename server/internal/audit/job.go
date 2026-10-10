// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultRetentionDays is audit_retention_days when the org has no setting (OrgSettings in the API file).
const DefaultRetentionDays = 365

// ArchiveKey derives the key that signs archive segments from the master key.
func ArchiveKey(kek []byte) []byte {
	sum := sha256.Sum256(append([]byte("polipan-audit-archive:"), kek...))
	return sum[:]
}

// RunOnce is the daily job: it verifies the chain of every org, raises the alert when it is broken, and
// archives the rows that are older than the retention of the org.
func RunOnce(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, key []byte, now time.Time) {
	orgs, err := OrgIDs(ctx, pool)
	if err != nil {
		log.Error("audit job: cannot list orgs", "error", err)
		return
	}
	for _, org := range orgs {
		res, err := Verify(ctx, pool, org)
		switch {
		case err != nil:
			log.Error("audit job: verify failed", "org_id", org, "error", err)
			continue
		case !res.OK:
			// The alert audit_chain_broken (critical). The alert channels come with M8: until then the log and an event.
			log.Error("ALERT audit_chain_broken", "org_id", org, "first_broken_seq", res.FirstBrokenSeq)
			if err := Write(ctx, pool, Event{OrgID: org, ActorType: ActorSystem, ActorID: "audit-job", Action: "audit.chain_broken",
				TargetType: "audit", Diff: map[string]any{"first_broken_seq": res.FirstBrokenSeq}}); err != nil {
				log.Error("audit job: event not written", "error", err)
			}
			continue // never archive (and so hide) a broken chain
		}
		days := DefaultRetentionDays
		var setting *int
		if err := pool.QueryRow(ctx, `SELECT (settings->>'audit_retention_days')::int FROM orgs WHERE id = $1`, org).Scan(&setting); err == nil && setting != nil && *setting > 0 {
			days = *setting
		}
		out, err := Archive(ctx, pool, org, now.AddDate(0, 0, -days), key)
		if err != nil {
			log.Error("audit job: archive failed", "org_id", org, "error", err)
			continue
		}
		if out.Rows > 0 {
			log.Info("audit job: rows archived", "org_id", org, "archive_id", out.ArchiveID, "rows", out.Rows)
		}
	}
}

// Run calls RunOnce once a day until ctx ends. The first run is after one minute.
func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, key []byte) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			RunOnce(ctx, pool, log, key, time.Now())
			timer.Reset(24 * time.Hour)
		}
	}
}
