// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCryptoRoundTripAndAAD(t *testing.T) {
	kek, dek := key(t), key(t)
	wrapped, err := secrets.WrapDEK(kek, dek, "prj_1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := secrets.UnwrapDEK(kek, wrapped, "prj_1"); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := secrets.UnwrapDEK(kek, wrapped, "prj_2"); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("a DEK must be bound to its project: %v", err)
	}
	if _, err := secrets.UnwrapDEK(key(t), wrapped, "prj_1"); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("wrong KEK: %v", err)
	}
	n1, c1, err := secrets.Seal(dek, "sec_1", 3, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	n2, _, _ := secrets.Seal(dek, "sec_1", 3, []byte("hunter2"))
	if bytes.Equal(n1, n2) || len(n1) != 12 {
		t.Fatal("each version needs a fresh 96-bit nonce")
	}
	if got, err := secrets.Open(dek, "sec_1", 3, n1, c1); err != nil || string(got) != "hunter2" {
		t.Fatalf("open: %q %v", got, err)
	}
	// AAD = secret id || version: the ciphertext cannot be moved to another secret or version.
	if _, err := secrets.Open(dek, "sec_1", 4, n1, c1); err == nil {
		t.Fatal("opened with another version")
	}
	if _, err := secrets.Open(dek, "sec_2", 3, n1, c1); err == nil {
		t.Fatal("opened with another secret id")
	}
	if secrets.KEKID(kek) == secrets.KEKID(key(t)) || strings.Contains(secrets.KEKID(kek), string(kek)) {
		t.Fatal("kek id")
	}
}

type fixture struct {
	pool    *pgxpool.Pool
	st      *store.Store
	orgID   string
	project string
}

func setup(t *testing.T, kek []byte) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pki.Load(ctx, pool, kek); err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, st: store.New(pool), orgID: store.NewID("org")}
	if _, err := pool.Exec(ctx, `INSERT INTO orgs (id, name, slug) VALUES ($1, 'O', 'o')`, f.orgID); err != nil {
		t.Fatal(err)
	}
	p, err := f.st.CreateProject(ctx, f.orgID, "P", "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.project = p.ID
	return f
}

func (f *fixture) secret(t *testing.T, p secrets.SecretProvider, name string, values ...string) *store.Secret {
	t.Helper()
	ctx := context.Background()
	x, err := f.st.CreateSecret(ctx, p, f.orgID, f.project, &store.Scope{}, store.NewSecret{Name: name, Value: values[0]}, store.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values[1:] {
		if x, err = f.st.SetSecretValue(ctx, p, f.orgID, x.ID, v, nil, nil, store.Actor{}); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

// AC-SEC-04: after rotate-kek (and rotate-dek) every secret version still decrypts with the new
// key, the CA key too, and the old key no longer works. Values are not re-encrypted by rotate-kek.
func TestAC_SEC_04_RotateKEKAndDEK(t *testing.T) {
	ctx := context.Background()
	oldKEK, newKEK := key(t), key(t)
	f := setup(t, oldKEK)
	p, _ := secrets.NewDBProvider(oldKEK)
	a := f.secret(t, p, "A", "alpha-1", "alpha-2")
	b := f.secret(t, p, "B", "beta-1")
	var before []byte
	_ = f.pool.QueryRow(ctx, `SELECT ciphertext FROM secret_versions WHERE secret_id = $1 AND version = 1`, a.ID).Scan(&before)

	n, err := secrets.RotateKEK(ctx, f.pool, oldKEK, newKEK)
	if err != nil || n != 1 {
		t.Fatalf("rotate-kek: %d %v", n, err)
	}
	var after []byte
	_ = f.pool.QueryRow(ctx, `SELECT ciphertext FROM secret_versions WHERE secret_id = $1 AND version = 1`, a.ID).Scan(&after)
	if !bytes.Equal(before, after) {
		t.Fatal("rotate-kek must only re-wrap the DEK")
	}
	np, _ := secrets.NewDBProvider(newKEK)
	check := func(prov secrets.SecretProvider) {
		t.Helper()
		for _, c := range []struct {
			id, want string
			version  int
		}{{a.ID, "alpha-1", 1}, {a.ID, "alpha-2", 2}, {b.ID, "beta-1", 1}} {
			got, err := prov.Get(ctx, f.pool, f.project, c.id, c.version)
			if err != nil || string(got) != c.want {
				t.Fatalf("%s v%d: %q %v", c.id, c.version, got, err)
			}
		}
	}
	check(np)
	if _, err := p.Get(ctx, f.pool, f.project, a.ID, 1); !errors.Is(err, secrets.ErrWrongKEK) {
		t.Fatalf("old key after rotation: %v", err)
	}
	if _, err := pki.Load(ctx, f.pool, newKEK); err != nil {
		t.Fatalf("CA with the new key: %v", err)
	}
	// A server that still runs with the old key cannot make a DEK for a new project.
	p2, _ := f.st.CreateProject(ctx, f.orgID, "Q", "q", nil)
	if _, err := f.st.CreateSecret(ctx, p, f.orgID, p2.ID, &store.Scope{}, store.NewSecret{Name: "X", Value: "y"}, store.Actor{}); !errors.Is(err, secrets.ErrWrongKEK) {
		t.Fatalf("old server after rotation: %v", err)
	}

	n, err = secrets.RotateDEK(ctx, f.pool, newKEK, f.project)
	if err != nil || n != 3 {
		t.Fatalf("rotate-dek: %d %v", n, err)
	}
	check(np)
	var audits int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'audit' AND payload->>'action' IN ('secret.rotate_kek','secret.rotate_dek')`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("rotation audit records: %d", audits)
	}
}

// AC-SEC-05: a dump of secret_versions and data_keys has no plain value, and without the
// master key nothing decrypts.
func TestAC_SEC_05_DumpHasNoPlaintext(t *testing.T) {
	ctx := context.Background()
	kek := key(t)
	f := setup(t, kek)
	p, _ := secrets.NewDBProvider(kek)
	values := []string{"plain-text-secret-value-1", "second-value-xyz"}
	x := f.secret(t, p, "DB_PASSWORD", values...)

	var dump bytes.Buffer
	for _, q := range []string{`SELECT t::text FROM secret_versions t`, `SELECT t::text FROM data_keys t`, `SELECT t::text FROM secrets t`, `SELECT t::text FROM events t`} {
		rows, err := f.pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			dump.WriteString(s + "\n")
		}
		rows.Close()
	}
	for _, v := range values {
		if strings.Contains(dump.String(), v) || bytes.Contains(dump.Bytes(), []byte(strings.ToLower(hexOf(v)))) {
			t.Fatalf("the dump holds the value %q", v)
		}
	}
	if !strings.Contains(dump.String(), "DB_PASSWORD") {
		t.Fatal("the dump test reads nothing")
	}
	// Without the master key: another key (as an attacker would try) cannot read a value.
	other, _ := secrets.NewDBProvider(key(t))
	if _, err := other.Get(ctx, f.pool, f.project, x.ID, 1); err == nil {
		t.Fatal("decrypted without the master key")
	}
	var wrapped []byte
	_ = f.pool.QueryRow(ctx, `SELECT dek_enc FROM data_keys`).Scan(&wrapped)
	if _, err := secrets.UnwrapDEK(make([]byte, 32), wrapped, f.project); err == nil {
		t.Fatal("unwrapped the DEK with a zero key")
	}
}

func hexOf(s string) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(s))
	for _, c := range []byte(s) {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
