// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"

	"github.com/polipan/polipan/server/internal/audit"
)

// AuditEvent is one audit record (REQ-AUDIT-002). Diff must never hold a secret value:
// use mask.Replacement ("***") for values.
type AuditEvent = audit.Event

// WriteAudit appends an audit record to the hash chain in the transaction q.
func WriteAudit(ctx context.Context, q Querier, e AuditEvent) error {
	return audit.Write(ctx, q, e)
}
