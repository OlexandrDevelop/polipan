// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func itoa(n int) string                 { return strconv.Itoa(n) }
func isNoRows(err error) bool           { return errors.Is(err, pgx.ErrNoRows) }

var store404 = errors.New("preview: not found")

// ErrNotFound is returned for a preview that does not exist.
var ErrNotFound = store404

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryStrings(ctx context.Context, q querier, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
