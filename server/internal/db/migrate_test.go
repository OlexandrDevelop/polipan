// SPDX-License-Identifier: Apache-2.0

package db_test

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
)

// The tables of the baseline migration (M0).
var baselineTables = []string{
	"orgs", "users", "memberships", "invitations", "password_resets", "teams", "team_members",
	"projects", "project_roles", "sessions", "api_tokens", "sso_providers", "sso_group_mappings",
	"sso_identities", "jobs",
}

// The tables of the M2 migration, and the tables they need, for the second comparison.
var (
	m2Tables     = []string{"volumes", "variables", "data_keys", "secrets", "secret_versions", "git_connections"}
	m2BaseTables = []string{"orgs", "users", "projects", "environments", "template_instances", "services", "nodes"}
)

// ddlStatements returns the statements of docs/spec/03-data-model.md that belong to the given tables.
func ddlStatements(t *testing.T, tables []string) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/spec/03-data-model.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```sql\n(.*?)```").FindAllStringSubmatch(string(raw), -1)
	comment := regexp.MustCompile(`--[^\n]*`)
	want := map[string]bool{}
	for _, tbl := range tables {
		want[tbl] = true
	}
	create := regexp.MustCompile(`(?s)^CREATE TABLE (\w+)`)
	index := regexp.MustCompile(`(?s)^CREATE (?:UNIQUE )?INDEX \w+\s+ON (\w+)`)
	var out []string
	for _, b := range blocks {
		for _, stmt := range strings.Split(comment.ReplaceAllString(b[1], ""), ";") {
			stmt = strings.TrimSpace(stmt)
			if m := create.FindStringSubmatch(stmt); m != nil && want[m[1]] {
				out = append(out, stmt)
			} else if m := index.FindStringSubmatch(stmt); m != nil && want[m[1]] {
				out = append(out, stmt)
			}
		}
	}
	return out
}

// describe returns a readable description of the schema of the tables:
// columns, constraints and indexes, as PostgreSQL reports them.
func describe(t *testing.T, pool *pgxpool.Pool, tables []string) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	collect := func(q string, args ...any) {
		rows, err := pool.Query(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	collect(`SELECT 'column ' || table_name || '.' || column_name || ' ' || format_type(a.atttypid, a.atttypmod)
			|| ' null=' || is_nullable || ' default=' || coalesce(column_default, '')
		FROM information_schema.columns c
		JOIN pg_attribute a ON a.attrelid = (quote_ident(c.table_schema) || '.' || quote_ident(c.table_name))::regclass AND a.attname = c.column_name
		WHERE c.table_schema = 'public' AND c.table_name = ANY($1)`, tables)
	collect(`SELECT 'constraint ' || conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid)
		FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND conrelid::regclass::text = ANY($1)`, tables)
	collect(`SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = ANY($1)`, tables)
	sort.Strings(out)
	return out
}

// specDatabase builds a database straight from the spec text. The statements run in rounds
// because the spec lists some tables after the tables that point to them.
func specDatabase(t *testing.T, admin *pgxpool.Pool, tables []string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	reference := dbtest.NewDatabase(t, admin)
	if _, err := reference.Exec(ctx, "CREATE EXTENSION citext"); err != nil {
		t.Fatal(err)
	}
	pending := ddlStatements(t, tables)
	created := 0
	for len(pending) > 0 {
		var failed []string
		var lastErr error
		for _, stmt := range pending {
			if _, err := reference.Exec(ctx, stmt); err != nil {
				failed = append(failed, stmt)
				lastErr = err
			} else {
				created++
			}
		}
		if len(failed) == len(pending) {
			t.Fatalf("cannot create the spec DDL: %v", lastErr)
		}
		pending = failed
	}
	if created < len(tables) {
		t.Fatalf("only %d statements found in the spec", created)
	}
	return reference
}

// The M2 migration makes the tables of 03-data-model section 3.5 exactly as the spec DDL.
func TestM2_MigrationMatchesSpecDDL(t *testing.T) {
	admin := dbtest.Admin(t)
	migrated := dbtest.NewDatabase(t, admin)
	if _, err := db.Migrate(context.Background(), migrated); err != nil {
		t.Fatal(err)
	}
	reference := specDatabase(t, admin, append(append([]string{}, m2BaseTables...), m2Tables...))
	got, want := describe(t, migrated, m2Tables), describe(t, reference, m2Tables)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("schema differs from the spec DDL\nmigration only:\n%s\nspec only:\n%s", diff(got, want), diff(want, got))
	}
	if len(want) < 40 {
		t.Fatalf("schema description has only %d lines, the comparison is too weak", len(want))
	}
}

// Done criterion 5 of M0 (REQ-CORE-020): the first migration applies on an empty database and
// the schema equals the DDL of docs/spec/03-data-model.md for the included tables.
func TestM0_FirstMigrationMatchesSpecDDL(t *testing.T) {
	ctx := context.Background()
	admin := dbtest.Admin(t)

	migrated := dbtest.NewDatabase(t, admin)
	applied, err := db.Migrate(ctx, migrated)
	if err != nil {
		t.Fatal(err)
	}
	all, err := db.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(all) || len(applied) < 3 || applied[0] != 1 || applied[1] != 2 || applied[2] != 3 {
		t.Fatalf("applied %v, want all %d migrations from 1", applied, len(all))
	}
	again, err := db.Migrate(ctx, migrated)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run: applied %v, err %v", again, err)
	}

	// Build a second database straight from the spec text.
	reference := specDatabase(t, admin, baselineTables)

	got, want := describe(t, migrated, baselineTables), describe(t, reference, baselineTables)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("schema differs from the spec DDL\nmigration only:\n%s\nspec only:\n%s",
			diff(got, want), diff(want, got))
	}
	if len(want) < 100 {
		t.Fatalf("schema description has only %d lines, the comparison is too weak", len(want))
	}

	// The queue index from the spec must exist and work as a unique key for waiting jobs.
	for i := 0; i < 2; i++ {
		_, err := migrated.Exec(ctx, `INSERT INTO jobs (kind, payload, unique_key) VALUES ('x', '{}', 'k')`)
		if (i == 0) != (err == nil) {
			t.Fatalf("insert %d: err %v", i, err)
		}
	}
}

func diff(a, b []string) string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, "  "+s)
		}
	}
	return strings.Join(out, "\n")
}
