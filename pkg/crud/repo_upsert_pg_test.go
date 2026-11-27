package crud

import (
	"context"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// upsertRow carries every axis Upsert's doc comment makes a claim about in
// one entity: managed timestamps, native soft delete, and a ,skipupdate
// column (the projection of a `forge:immutable` declaration) — so a single
// table exercises the insert path, the update path, the soft-delete
// resurrection decision, and the immutable-column carve-out together.
type upsertRow struct {
	bun.BaseModel `bun:"table:upsert_rows,alias:upsert_rows"`

	ID        string     `bun:"id,pk"`
	Name      string     `bun:"name,notnull"`
	OwnerID   string     `bun:"owner_id,notnull,skipupdate"`
	CreatedAt time.Time  `bun:"created_at,notnull"`
	UpdatedAt time.Time  `bun:"updated_at,notnull"`
	DeletedAt *time.Time `bun:"deleted_at,soft_delete,nullzero"`
}

func createUpsertRowsTable(t *testing.T, db orm.Context) {
	t.Helper()
	_, err := db.Bun().NewCreateTable().Model((*upsertRow)(nil)).
		IfNotExists().Exec(context.Background())
	if err != nil {
		t.Fatalf("create upsert_rows table: %v", err)
	}
}

// The insert path: a PK Upsert has never seen behaves like Create — the
// row is created, and BOTH managed timestamps are stamped, including a
// ,skipupdate column (owner_id), which Create's sibling insert path also
// writes freely.
func TestUpsert_InsertPath_PG(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createUpsertRowsTable(t, db)
	repo := NewRepo[upsertRow]()

	row := &upsertRow{ID: "r1", Name: "alpha", OwnerID: "owner-a"}
	if err := repo.Upsert(ctx, db, row); err != nil {
		t.Fatalf("Upsert (insert path): %v", err)
	}
	if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Error("Upsert insert path did not stamp managed timestamps")
	}

	got, err := repo.Get(ctx, db, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "alpha" {
		t.Errorf("name = %q, want alpha", got.Name)
	}
	if got.OwnerID != "owner-a" {
		t.Errorf("owner_id = %q, want owner-a — the insert path must write a ,skipupdate column", got.OwnerID)
	}
}

// The update path: an existing PK triggers ON CONFLICT DO UPDATE. Values
// change, created_at is untouched (the row's birth time does not move),
// updated_at advances, and the ,skipupdate column is protected exactly as
// it is under a full-replace Update — an upsert must not be a backdoor
// around the immutable-column rule.
func TestUpsert_UpdatePath_PG(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createUpsertRowsTable(t, db)
	repo := NewRepo[upsertRow]()

	row := &upsertRow{ID: "r1", Name: "alpha", OwnerID: "owner-a"}
	if err := repo.Upsert(ctx, db, row); err != nil {
		t.Fatalf("Upsert (insert path): %v", err)
	}
	firstCreatedAt := row.CreatedAt
	firstUpdatedAt := row.UpdatedAt

	time.Sleep(2 * time.Millisecond)
	// Simulate the round-trip: a client sends back the whole entity with a
	// new name and an owner it never legitimately controlled.
	repl := &upsertRow{ID: "r1", Name: "renamed", OwnerID: "owner-hijack"}
	if err := repo.Upsert(ctx, db, repl); err != nil {
		t.Fatalf("Upsert (update path): %v", err)
	}

	got, err := repo.Get(ctx, db, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q, want renamed", got.Name)
	}
	if got.OwnerID != "owner-a" {
		t.Errorf("owner_id = %q, want owner-a — the update path must not clobber a ,skipupdate column", got.OwnerID)
	}
	// Compare at microsecond precision: firstCreatedAt is the value the
	// stamper put in memory (full nanosecond resolution from time.Now()),
	// while got.CreatedAt has round-tripped through a Postgres timestamptz,
	// which stores microseconds and so truncates the last three digits. An
	// exact Equal here fails on any clock that actually resolves
	// nanoseconds — Linux CI does; macOS returns microsecond-granular times,
	// which is why this only ever went red on CI.
	if !got.CreatedAt.Equal(firstCreatedAt.Truncate(time.Microsecond)) {
		t.Errorf("created_at moved from %v to %v — an upsert on the update path must not rewrite the row's birth time", firstCreatedAt, got.CreatedAt)
	}
	// Same truncation applies, and it moves the stored value BACKWARDS by
	// up to a microsecond — compare against the truncated original so the
	// assertion measures the re-stamp, not the storage precision.
	if !got.UpdatedAt.After(firstUpdatedAt.Truncate(time.Microsecond)) {
		t.Errorf("updated_at did not advance: %v !> %v", got.UpdatedAt, firstUpdatedAt)
	}
}

// A soft-deleted row is, from Upsert's point of view, an absent one it is
// allowed to recreate: an upsert onto a tombstoned PK resurrects it
// (clears deleted_at) rather than silently no-op'ing or erroring, because
// there is no read-side "does this row exist" signal an INSERT can consult
// the way Update's WHERE guard does.
func TestUpsert_ResurrectsSoftDeletedRow_PG(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createUpsertRowsTable(t, db)
	repo := NewRepo[upsertRow]()

	row := &upsertRow{ID: "r1", Name: "alpha", OwnerID: "owner-a"}
	if err := repo.Upsert(ctx, db, row); err != nil {
		t.Fatalf("Upsert (insert path): %v", err)
	}
	if err := repo.Delete(ctx, db, "r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, db, "r1"); err == nil {
		t.Fatal("row should be invisible to Get after soft delete")
	}

	repl := &upsertRow{ID: "r1", Name: "resurrected", OwnerID: "owner-hijack"}
	if err := repo.Upsert(ctx, db, repl); err != nil {
		t.Fatalf("Upsert onto a tombstoned PK: %v", err)
	}

	got, err := repo.Get(ctx, db, "r1")
	if err != nil {
		t.Fatalf("Get after resurrection: %v", err)
	}
	if got.DeletedAt != nil {
		t.Errorf("deleted_at = %v, want nil — Upsert must clear the tombstone on resurrection", got.DeletedAt)
	}
	if got.Name != "resurrected" {
		t.Errorf("name = %q, want resurrected", got.Name)
	}
	if got.OwnerID != "owner-a" {
		t.Errorf("owner_id = %q, want owner-a — resurrection still goes through the update path's SET list", got.OwnerID)
	}
}

// An empty string PK is Upsert's other documented divergence from Create:
// Create ULID-generates a fresh value for an empty string PK, but Upsert
// does not, because a generated PK could never already be present and so
// ON CONFLICT could never fire — silently degrading Upsert into Create for
// the caller's most likely mistake (forgetting to set the PK) instead of
// letting a second call to the same empty PK conflict and update, exactly
// as any other PK value would.
func TestUpsert_EmptyStringPKDoesNotGenerate_PG(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createUpsertRowsTable(t, db)
	repo := NewRepo[upsertRow]()

	first := &upsertRow{ID: "", Name: "alpha", OwnerID: "owner-a"}
	if err := repo.Upsert(ctx, db, first); err != nil {
		t.Fatalf("Upsert with empty PK (insert path): %v", err)
	}
	if first.ID != "" {
		t.Errorf("Upsert must not ULID-generate an empty string PK, got %q", first.ID)
	}

	got, err := repo.Get(ctx, db, "")
	if err != nil {
		t.Fatalf("Get by empty PK: %v", err)
	}
	if got.Name != "alpha" {
		t.Errorf("name = %q, want alpha", got.Name)
	}

	// A second Upsert with the same empty PK must conflict and update, not
	// insert a second row — proving the empty PK really did land in the
	// table as a real conflictable value rather than being special-cased.
	second := &upsertRow{ID: "", Name: "beta", OwnerID: "owner-hijack"}
	if err := repo.Upsert(ctx, db, second); err != nil {
		t.Fatalf("Upsert with empty PK (update path): %v", err)
	}

	all, err := repo.ListAll(ctx, db)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows, want 1 — a second empty-PK Upsert must conflict, not insert", len(all))
	}
	if all[0].Name != "beta" {
		t.Errorf("name = %q, want beta", all[0].Name)
	}
	if all[0].OwnerID != "owner-a" {
		t.Errorf("owner_id = %q, want owner-a — the update path still protects ,skipupdate", all[0].OwnerID)
	}
}

// A server-allocated (autoincrement) PK left at its zero value can never
// conflict with an existing row — there is no matching value to find — so
// it always inserts a fresh row, matching Create's behavior for the same
// PK kind. Reuses widget (repo_pg_test.go), whose id is BIGSERIAL.
func TestUpsert_ZeroAutoIncrementPKAlwaysInserts_PG(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	repo := NewRepo[widget]()

	first := &widget{Name: "alpha"}
	if err := repo.Upsert(ctx, db, first); err != nil {
		t.Fatalf("Upsert #1: %v", err)
	}
	second := &widget{Name: "beta"}
	if err := repo.Upsert(ctx, db, second); err != nil {
		t.Fatalf("Upsert #2: %v", err)
	}
	if first.ID == 0 || second.ID == 0 {
		t.Fatal("Upsert did not read back the server-allocated PK")
	}
	if first.ID == second.ID {
		t.Fatalf("two zero-PK upserts produced the same id %d — they must not conflict with each other", first.ID)
	}

	all, err := repo.ListAll(ctx, db)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d rows, want 2", len(all))
	}
}
