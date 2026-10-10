// SPDX-License-Identifier: Apache-2.0

// Package dbtest gives tests an empty PostgreSQL database. The tests read the
// server address from POLIPAN_TEST_DATABASE_URL. In CI (CI is set) a missing
// address is an error, elsewhere the test is skipped.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Admin connects to the test server.
func Admin(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("POLIPAN_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("POLIPAN_TEST_DATABASE_URL is not set")
		}
		t.Skip("POLIPAN_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// NewDatabase creates an empty database on the test server. It is dropped when the test ends.
func NewDatabase(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "pp_test_" + hex.EncodeToString(b)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cfg := admin.Config()
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool
}
