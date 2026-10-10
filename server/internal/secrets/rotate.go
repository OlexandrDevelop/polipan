// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/pki"
)

// RotateKEK re-wraps every project DEK and the server CA key with newKEK (REQ-SEC-008).
// Secret values are not re-encrypted, so no service restarts. It returns the number of DEKs.
func RotateKEK(ctx context.Context, pool *pgxpool.Pool, oldKEK, newKEK []byte) (int, error) {
	if len(oldKEK) != KeySize || len(newKEK) != KeySize {
		return 0, ErrBadKey
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	type row struct {
		project, kekID string
		wrapped        []byte
	}
	rows, err := tx.Query(ctx, `SELECT project_id, kek_id, dek_enc FROM data_keys ORDER BY project_id FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.project, &r.kekID, &r.wrapped); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	oldID, newID := KEKID(oldKEK), KEKID(newKEK)
	for _, r := range list {
		if r.kekID != oldID {
			return 0, fmt.Errorf("data key of %s: %w", r.project, ErrWrongKEK)
		}
		dek, err := UnwrapDEK(oldKEK, r.wrapped, r.project)
		if err != nil {
			return 0, fmt.Errorf("data key of %s: %w", r.project, err)
		}
		enc, err := WrapDEK(newKEK, dek, r.project)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE data_keys SET dek_enc = $2, kek_id = $3, rotated_at = now() WHERE project_id = $1`,
			r.project, enc, newID); err != nil {
			return 0, err
		}
	}
	var caKey []byte
	err = tx.QueryRow(ctx, `SELECT key_enc FROM server_ca WHERE id = 1 FOR UPDATE`).Scan(&caKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return 0, err
	default:
		enc, err := rewrapCAKey(oldKEK, newKEK, caKey)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE server_ca SET key_enc = $1 WHERE id = 1`, enc); err != nil {
			return 0, err
		}
	}
	if err := auditSystem(ctx, tx, "secret.rotate_kek", "kek", newID, "", map[string]any{"data_keys": len(list), "from": oldID, "to": newID}); err != nil {
		return 0, err
	}
	return len(list), tx.Commit(ctx)
}

// RotateDEK makes a new DEK for a project and re-encrypts every kept secret version with it.
// The running server reads the DEK for each use, so services keep running. It returns the number of versions.
func RotateDEK(ctx context.Context, pool *pgxpool.Pool, kek []byte, projectID string) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var wrapped []byte
	var kekID string
	err = tx.QueryRow(ctx, `SELECT dek_enc, kek_id FROM data_keys WHERE project_id = $1 FOR UPDATE`, projectID).Scan(&wrapped, &kekID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("project %s has no secrets", projectID)
	}
	if err != nil {
		return 0, err
	}
	if kekID != KEKID(kek) {
		return 0, ErrWrongKEK
	}
	oldDEK, err := UnwrapDEK(kek, wrapped, projectID)
	if err != nil {
		return 0, err
	}
	newDEK, err := NewDEK()
	if err != nil {
		return 0, err
	}
	type ver struct {
		secret     string
		version    int
		nonce, enc []byte
	}
	rows, err := tx.Query(ctx, `SELECT v.secret_id, v.version, v.nonce, v.ciphertext FROM secret_versions v
		JOIN secrets s ON s.id = v.secret_id WHERE s.project_id = $1 AND v.destroyed_at IS NULL FOR UPDATE OF v`, projectID)
	if err != nil {
		return 0, err
	}
	var list []ver
	for rows.Next() {
		var v ver
		if err := rows.Scan(&v.secret, &v.version, &v.nonce, &v.enc); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, v := range list {
		plain, err := Open(oldDEK, v.secret, v.version, v.nonce, v.enc)
		if err != nil {
			return 0, fmt.Errorf("version %d of %s: %w", v.version, v.secret, err)
		}
		nonce, enc, err := Seal(newDEK, v.secret, v.version, plain)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE secret_versions SET nonce = $3, ciphertext = $4 WHERE secret_id = $1 AND version = $2`,
			v.secret, v.version, nonce, enc); err != nil {
			return 0, err
		}
	}
	newWrapped, err := WrapDEK(kek, newDEK, projectID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE data_keys SET dek_enc = $2, rotated_at = now() WHERE project_id = $1`, projectID, newWrapped); err != nil {
		return 0, err
	}
	if err := auditSystem(ctx, tx, "secret.rotate_dek", "project", projectID, projectID, map[string]any{"versions": len(list)}); err != nil {
		return 0, err
	}
	return len(list), tx.Commit(ctx)
}

// auditSystem writes an audit record of a server command for the org (of the project, or the first org).
func auditSystem(ctx context.Context, q Querier, action, targetType, targetID, projectID string, diff any) error {
	var orgID string
	err := q.QueryRow(ctx, `SELECT COALESCE((SELECT org_id FROM projects WHERE id = $1),
		(SELECT id FROM orgs WHERE deleted_at IS NULL ORDER BY created_at, id LIMIT 1), '')`, projectID).Scan(&orgID)
	if err != nil || orgID == "" {
		return err // no org: nothing to audit yet
	}
	actor := os.Getenv("USER")
	if actor == "" {
		actor = "cli"
	}
	return WriteAudit(ctx, q, AuditEvent{OrgID: orgID, ActorType: "system", ActorID: "cli:" + actor, Action: action,
		TargetType: targetType, TargetID: targetID, ProjectID: projectID, Diff: diff})
}

// openDB opens the database and applies the migrations.
func openDB(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// RotateKEKCommand is "polipan-server admin rotate-kek". It makes a new master key, re-wraps all
// keys with it in one transaction and then replaces the master key file. Restart the server after it.
func RotateKEKCommand(ctx context.Context, cfg config.Config, getenv func(string) string, out io.Writer) error {
	if strings.TrimSpace(getenv("POLIPAN_MASTER_KEY")) != "" {
		return errors.New("rotate-kek changes the master key file: unset POLIPAN_MASTER_KEY and use master_key_file")
	}
	file := cfg.MasterKeyFile
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("master key file: %w", err)
	}
	oldKEK, err := pki.LoadKEK(file, getenv)
	if err != nil {
		return err
	}
	newKEK, err := NewDEK()
	if err != nil {
		return err
	}
	// Write the new key next to the old one first: if the database part fails, nothing is lost.
	tmp := file + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s exists: a former rotation did not finish, check it and remove it", tmp)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(newKEK); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	defer pool.Close()
	n, err := RotateKEK(ctx, pool, oldKEK, newKEK)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		return fmt.Errorf("the database uses the new key, but the file was not replaced: move %s to %s by hand: %w", tmp, file, err)
	}
	_, err = fmt.Fprintf(out, "rotated the master key: %d data keys re-wrapped (new key id %s).\nRestart the server now. Keep a safe copy of %s.\n",
		n, KEKID(newKEK), file)
	return err
}

// RotateDEKCommand is "polipan-server admin rotate-dek --project <id or slug>".
func RotateDEKCommand(ctx context.Context, cfg config.Config, args []string, getenv func(string) string, out io.Writer) error {
	fset := flag.NewFlagSet("rotate-dek", flag.ContinueOnError)
	project := fset.String("project", "", "id or slug of the project")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errors.New("--project is required")
	}
	kek, err := pki.LoadKEK(cfg.MasterKeyFile, getenv)
	if err != nil {
		return err
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	var id string
	err = pool.QueryRow(ctx, `SELECT id FROM projects WHERE (id = $1 OR slug = $1) AND deleted_at IS NULL ORDER BY created_at LIMIT 1`, *project).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("project %q not found", *project)
	}
	if err != nil {
		return err
	}
	n, err := RotateDEK(ctx, pool, kek, id)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "rotated the data key of %s: %d secret versions re-encrypted\n", id, n)
	return err
}
