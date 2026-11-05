package crud

import (
	"context"
	"testing"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// immutableRow carries a column tagged ,skipupdate — the projection of a
// `forge:immutable` declaration on the column. `mutable` is an ordinary column
// with no wire field and no declaration: it must stay fully writable, because
// absence from the API says nothing about whether storage may rewrite it.
type immutableRow struct {
	bun.BaseModel `bun:"table:immutable_rows,alias:immutable_rows"`

	ID      string `bun:"id,pk"`
	Name    string `bun:"name,notnull"`
	OwnerID string `bun:"owner_id,notnull,skipupdate"`
	Mutable int64  `bun:"mutable,notnull"`
}

func immutableSchema(ctx context.Context, t *testing.T, db orm.Context) {
	t.Helper()
	if _, err := db.Exec(ctx, `
CREATE TABLE immutable_rows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    mutable BIGINT NOT NULL DEFAULT 0
)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

// A full-replace Update must leave an immutable column alone even when the
// in-memory entity carries a different value — that entity was built from a
// request that could not legitimately set it.
func TestImmutableColumnSurvivesFullReplace(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	immutableSchema(ctx, t, db)

	repo := NewRepo[immutableRow]()
	row := &immutableRow{ID: "r1", Name: "original", OwnerID: "owner-a", Mutable: 1}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate the round-trip: a client sends back the whole entity with the
	// owner blanked (it was never on the wire) and a new name.
	repl := &immutableRow{ID: "r1", Name: "renamed", OwnerID: "", Mutable: 2}
	if err := repo.Update(ctx, db, repl); err != nil {
		t.Fatalf("update: %v", err)
	}

	got := &immutableRow{}
	if err := db.Bun().NewSelect().Model(got).Where("id = ?", "r1").Scan(ctx); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.OwnerID != "owner-a" {
		t.Errorf("owner_id = %q, want owner-a — ,skipupdate did not hold the column", got.OwnerID)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q, want renamed — ordinary columns must still be written", got.Name)
	}
	// The decoupling guarantee: a column absent from the wire but NOT declared
	// immutable is ordinary mutable state and must be written.
	if got.Mutable != 2 {
		t.Errorf("mutable = %d, want 2 — an undeclared column must stay writable", got.Mutable)
	}
}

// The masked path is the deliberate-assertion path: naming an immutable
// column in an explicit update_mask (reassigning an owner, rotating a
// secret) must write it, even though the same column is untouchable via a
// full-replace Update.
func TestImmutableColumnUnderExplicitMask(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	immutableSchema(ctx, t, db)

	repo := NewRepo[immutableRow]()
	row := &immutableRow{ID: "r1", Name: "original", OwnerID: "owner-a", Mutable: 1}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("create: %v", err)
	}

	reassign := &immutableRow{ID: "r1", Name: "original", OwnerID: "owner-b", Mutable: 1}
	if err := repo.UpdateMasked(ctx, db, reassign, []string{"owner_id"}); err != nil {
		t.Fatalf("masked update: %v", err)
	}

	got := &immutableRow{}
	if err := db.Bun().NewSelect().Model(got).Where("id = ?", "r1").Scan(ctx); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.OwnerID != "owner-b" {
		t.Errorf("owner_id = %q, want owner-b — an explicit mask must be able to write a ,skipupdate column", got.OwnerID)
	}
}
