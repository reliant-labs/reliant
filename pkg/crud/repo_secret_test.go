package crud

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
)

// vault is an entity carrying a `// forge:secret` column. The
// stored secret is what a client can NEVER read back — the generated toProto
// read path strips it, so a Get returns secret="". A maskless full-replace
// Update built from that round-tripped entity therefore arrives with
// secret="" and, absent the fix, would clobber the stored credential. The
// repo must PRESERVE the secret on full replace (Spec.SecretColumns) while
// keeping it settable on Create and on an explicit masked Update.
type vault struct {
	bun.BaseModel `bun:"table:vaults,alias:vaults"`

	ID   string `bun:"id,pk"`
	Name string `bun:"name,notnull"`
	// The protection now rides on the column: `forge:immutable` on the
	// migration projects to Bun's ,skipupdate, which drops the column from a
	// full-replace SET clause. It replaces the Spec column list this test
	// originally exercised.
	Secret string `bun:"secret,notnull,skipupdate"`
}

// dialectOnlyDB is a minimal orm.Context that exposes only a dialect-backed
// bun handle — enough for ensureMeta's schema reflection, which never touches
// a live connection. It lets the meta-classification test run hermetically
// (no embedded postgres) under -short.
type dialectOnlyDB struct{ bdb *bun.DB }

func (d dialectOnlyDB) Bun() bun.IDB { return d.bdb }
func (d dialectOnlyDB) Exec(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}
func (d dialectOnlyDB) Query(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}
func (d dialectOnlyDB) QueryRow(context.Context, string, ...any) *sql.Row { return nil }

// RunTransaction runs fn against this same fake: the meta-classification
// path never opens a real transaction, and a fake that silently skipped fn
// would hide a caller that expected its body to run.
func (d dialectOnlyDB) RunTx(ctx context.Context, _ orm.TxPolicy, fn func(context.Context) error) error {
	return fn(ctx)
}

func (d dialectOnlyDB) RunTxReadOnly(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (d dialectOnlyDB) RunTransaction(ctx context.Context, _ orm.TxPolicy, fn func(orm.Context) error) error {
	return fn(d)
}

func (d dialectOnlyDB) BeginDetached(context.Context, orm.TxPolicy) (*orm.Tx, error) {
	return nil, errors.New("dialectOnlyDB opens no transactions")
}
func (d dialectOnlyDB) Dialect() orm.Dialect { return nil }

func containsCol(cols []string, want string) bool {
	for _, c := range cols {
		if c == want {
			return true
		}
	}
	return false
}

// TestSecretColumnClassification is the hermetic unit for the fix: with a
// Spec listing a secret column, the FULL-REPLACE SET list (r.m.updatable)
// must exclude it, while the MASKED-update allowlist (r.m.updatableSet) must
// still include it. This is the whole mechanism, asserted without a database.
func TestSecretColumnClassification(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}
	repo := NewRepo[vault]()
	repo.ensureMeta(db)

	// The maskless full-replace SET clause must NOT write the secret column.
	if containsCol(repo.m.updatable, "secret") {
		t.Errorf("full-replace SET list must exclude the secret column, got %v", repo.m.updatable)
	}
	// A normal column stays in the full-replace SET (the fix is surgical).
	if !containsCol(repo.m.updatable, "name") {
		t.Errorf("full-replace SET list dropped a non-secret column, got %v", repo.m.updatable)
	}
	// An explicit update_mask MAY still name the secret — it stays in the
	// masked-update allowlist (Create's sibling deliberate-write path).
	if !repo.m.updatableSet["secret"] {
		t.Error("masked-update allowlist must include the secret column (settable via explicit mask)")
	}
}

// TestRepoSecretPreservedOnFullReplace is the runtime proof against a REAL
// embedded postgres (repo_pg_test.go's oracle): a maskless full-replace
// Update leaves the stored secret untouched, an explicit masked Update
// naming it changes it. RED before the fix (secret wiped to ""), GREEN after.
func TestRepoSecretPreservedOnFullReplace(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Bun().NewCreateTable().Model((*vault)(nil)).IfNotExists().Exec(ctx); err != nil {
		t.Fatalf("create vaults table: %v", err)
	}

	repo := NewRepo[vault]()

	// Birth: the client authors the secret on Create — it IS written.
	v := &vault{ID: "v1", Name: "prod", Secret: "topsecret"}
	if err := repo.Create(ctx, db, v); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The repo layer performs no strip (that is the generated toProto's job),
	// so a direct Get reads the raw stored row.
	if got, _ := repo.Get(ctx, db, "v1"); got.Secret != "topsecret" {
		t.Fatalf("Create did not store the secret: got %q", got.Secret)
	}

	// The client round-trip: it read the entity (secret came back "" over the
	// wire), changed another field, and issued a MASKLESS full-replace Update
	// carrying Secret="". Pre-fix, the full-replace SET clause wrote that ""
	// over the stored credential.
	roundTripped := &vault{ID: "v1", Name: "prod-renamed", Secret: ""}
	if err := repo.Update(ctx, db, roundTripped); err != nil {
		t.Fatalf("Update (maskless full replace): %v", err)
	}
	got, err := repo.Get(ctx, db, "v1")
	if err != nil {
		t.Fatalf("Get after full replace: %v", err)
	}
	if got.Name != "prod-renamed" {
		t.Errorf("full-replace Update did not persist the non-secret field: name=%q", got.Name)
	}
	if got.Secret != "topsecret" {
		t.Errorf("SECRET WIPED: maskless Update overwrote the stored secret with %q; want it PRESERVED as %q", got.Secret, "topsecret")
	}

	// A masked Update that NAMES the secret is deliberate intent → it writes.
	rotated := &vault{ID: "v1", Secret: "rotated"}
	if err := repo.UpdateMasked(ctx, db, rotated, []string{"secret"}); err != nil {
		t.Fatalf("UpdateMasked(secret): %v", err)
	}
	if got, _ := repo.Get(ctx, db, "v1"); got.Secret != "rotated" {
		t.Errorf("masked Update naming the secret did not write it: got %q, want %q", got.Secret, "rotated")
	}
}
