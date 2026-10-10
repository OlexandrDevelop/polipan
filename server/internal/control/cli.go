// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/pki"
)

// openCLI opens the database, applies the migrations and loads the CA.
func openCLI(ctx context.Context, cfg config.Config) (*pgxpool.Pool, *pki.CA, error) {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}
	kek, err := pki.LoadKEK(cfg.MasterKeyFile, os.Getenv)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	ca, err := pki.Load(ctx, pool, kek)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, ca, nil
}

// CAFingerprintCommand prints the CA fingerprint (polipan-server ca-fingerprint).
func CAFingerprintCommand(ctx context.Context, cfg config.Config, out io.Writer) error {
	pool, ca, err := openCLI(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = fmt.Fprintln(out, ca.Fingerprint())
	return err
}

type labelFlags map[string]string

func (l labelFlags) String() string { return "" }
func (l labelFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return errors.New("a label must look like key=value")
	}
	l[k] = val
	return nil
}

// JoinTokenCommand makes a join token in the database (polipan-server join-token --name n [--label k=v]...).
// It uses the single org. If there is no org yet, it creates the org "Polipan" (slug "default").
// The first-user setup must reuse an existing org.
func JoinTokenCommand(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("join-token", flag.ContinueOnError)
	name := fs.String("name", "", "name of the new node")
	labels := labelFlags{}
	fs.Var(labels, "label", "node label key=value (can be repeated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	pool, ca, err := openCLI(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	orgID, err := singleOrg(ctx, pool)
	if err != nil {
		return err
	}
	c := New(Deps{DB: pool, Config: cfg, CA: ca})
	defer c.Close()
	tok, err := c.CreateJoinToken(ctx, orgID, *name, labels, "")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "token=%s\nca_fingerprint=%s\n", tok.Token, tok.CAFingerprint)
	return err
}

func singleOrg(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM orgs WHERE deleted_at IS NULL ORDER BY created_at, id LIMIT 1`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id = gateway.NewID("org")
	err = pool.QueryRow(ctx,
		`INSERT INTO orgs (id, name, slug) VALUES ($1, 'Polipan', 'default')
		 ON CONFLICT (slug) DO UPDATE SET name = orgs.name RETURNING id`, id).Scan(&id)
	return id, err
}
