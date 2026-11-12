package crud

import (
	"context"
	"errors"
	"testing"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Every write must leave the caller's entity holding the STORED row for each
// column the database computes or fills: GENERATED ALWAYS AS (...) STORED
// columns on Create, Update, UpdateMasked and Upsert, and DB-defaulted
// columns on insert. Without it every caller re-reads the row after writing
// — or, worse, returns the stale value to its client. Measured: an estimate
// line item's line_total_cents came back 0 after Create and stale after an
// Update, as did an invoice's balance_cents.

// lineItem is tagged exactly as the generator tags these shapes: a generated
// column carries ,nullzero,skipupdate plus forge:"generated"; a nullable
// column with a DEFAULT carries ,default:<expr>.
type lineItem struct {
	bun.BaseModel `bun:"table:line_items,alias:line_items"`

	ID         string  `bun:"id,pk"`
	Quantity   int64   `bun:"quantity,notnull"`
	UnitCents  int64   `bun:"unit_price_cents,notnull"`
	TotalCents int64   `bun:"line_total_cents,notnull,nullzero,skipupdate" forge:"generated"`
	Note       *string `bun:"note,default:'none'"`
}

// serialLineItem is the same shape on a server-allocated integer PK — the
// path whose explicit RETURNING of the PK used to suppress every other
// read-back.
type serialLineItem struct {
	bun.BaseModel `bun:"table:serial_line_items,alias:serial_line_items"`

	ID         int64   `bun:"id,pk,autoincrement"`
	Quantity   int64   `bun:"quantity,notnull"`
	UnitCents  int64   `bun:"unit_price_cents,notnull"`
	TotalCents int64   `bun:"line_total_cents,notnull,nullzero,skipupdate" forge:"generated"`
	Note       *string `bun:"note,default:'none'"`
}

func newReturningTestDB(t *testing.T) orm.Context {
	t.Helper()
	db := newRepoTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE line_items (
			id TEXT PRIMARY KEY,
			quantity BIGINT NOT NULL,
			unit_price_cents BIGINT NOT NULL,
			line_total_cents BIGINT GENERATED ALWAYS AS (quantity * unit_price_cents) STORED,
			note TEXT DEFAULT 'none')`,
		`CREATE TABLE serial_line_items (
			id BIGSERIAL PRIMARY KEY,
			quantity BIGINT NOT NULL,
			unit_price_cents BIGINT NOT NULL,
			line_total_cents BIGINT GENERATED ALWAYS AS (quantity * unit_price_cents) STORED,
			note TEXT DEFAULT 'none')`,
	} {
		if _, err := db.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func noteOf(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestRepoWrites_ScanBackGeneratedAndDefaultedColumns(t *testing.T) {
	db := newReturningTestDB(t)
	ctx := context.Background()
	repo := NewRepo[lineItem]()

	item := &lineItem{ID: "li-1", Quantity: 3, UnitCents: 250}
	if err := repo.Create(ctx, db, item); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if item.TotalCents != 750 {
		t.Errorf("Create: line_total_cents = %d, want 750 (generated, scanned back)", item.TotalCents)
	}
	if noteOf(item.Note) != "none" {
		t.Errorf("Create: note = %s, want the DB default 'none' scanned back", noteOf(item.Note))
	}

	item.Quantity = 4
	if err := repo.Update(ctx, db, item); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if item.TotalCents != 1000 {
		t.Errorf("Update: line_total_cents = %d, want 1000 (recomputed, scanned back)", item.TotalCents)
	}

	// The mask names only unit_price_cents; the generated column depends on
	// it and must still come back current.
	item.UnitCents = 300
	if err := repo.UpdateMasked(ctx, db, item, []string{"unit_price_cents"}); err != nil {
		t.Fatalf("UpdateMasked: %v", err)
	}
	if item.TotalCents != 1200 {
		t.Errorf("UpdateMasked: line_total_cents = %d, want 1200", item.TotalCents)
	}

	// Upsert: insert path, then conflict path.
	up := &lineItem{ID: "li-2", Quantity: 2, UnitCents: 50}
	if err := repo.Upsert(ctx, db, up); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	if up.TotalCents != 100 || noteOf(up.Note) != "none" {
		t.Errorf("Upsert insert: total=%d note=%s, want 100 and 'none'", up.TotalCents, noteOf(up.Note))
	}
	up.Quantity = 5
	if err := repo.Upsert(ctx, db, up); err != nil {
		t.Fatalf("Upsert conflict: %v", err)
	}
	if up.TotalCents != 250 {
		t.Errorf("Upsert conflict: line_total_cents = %d, want 250", up.TotalCents)
	}

	// What the entity holds must equal what a fresh read returns.
	stored, err := repo.Get(ctx, db, "li-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.TotalCents != item.TotalCents || noteOf(stored.Note) != noteOf(item.Note) {
		t.Errorf("entity after writes = %+v, stored row = %+v", item, stored)
	}

	// A caller-supplied value for a defaulted column is written and kept,
	// not replaced by the default.
	note := "rush"
	explicit := &lineItem{ID: "li-3", Quantity: 1, UnitCents: 1, Note: &note}
	if err := repo.Create(ctx, db, explicit); err != nil {
		t.Fatalf("Create explicit note: %v", err)
	}
	if noteOf(explicit.Note) != "rush" {
		t.Errorf("explicit note = %s, want 'rush'", noteOf(explicit.Note))
	}
}

func TestRepoWrites_ServerAllocatedPKStillReadBackWithGeneratedColumns(t *testing.T) {
	db := newReturningTestDB(t)
	ctx := context.Background()
	repo := NewRepo[serialLineItem]()

	item := &serialLineItem{Quantity: 2, UnitCents: 125}
	if err := repo.Create(ctx, db, item); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if item.ID == 0 {
		t.Error("Create did not read back the server-allocated PK")
	}
	if item.TotalCents != 250 {
		t.Errorf("Create: line_total_cents = %d, want 250", item.TotalCents)
	}
	if noteOf(item.Note) != "none" {
		t.Errorf("Create: note = %s, want 'none'", noteOf(item.Note))
	}

	up := &serialLineItem{Quantity: 1, UnitCents: 10}
	if err := repo.Upsert(ctx, db, up); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if up.ID == 0 || up.TotalCents != 10 {
		t.Errorf("Upsert: id=%d total=%d, want an allocated id and 10", up.ID, up.TotalCents)
	}
}

// RETURNING turns an UPDATE into a row-producing statement. A write that
// matches no row must still be NotFound (orm.ErrNoRows), not a scan error.
func TestRepoWrites_ReturningUpdateOfMissingRowIsStillNotFound(t *testing.T) {
	db := newReturningTestDB(t)
	ctx := context.Background()
	repo := NewRepo[lineItem]()

	ghost := &lineItem{ID: "missing", Quantity: 1, UnitCents: 1}
	if err := repo.Update(ctx, db, ghost); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Update of a missing row = %v, want orm.ErrNoRows", err)
	}
	if err := repo.UpdateMasked(ctx, db, ghost, []string{"quantity"}); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("UpdateMasked of a missing row = %v, want orm.ErrNoRows", err)
	}
}
