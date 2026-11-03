package crud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// These tests exercise the generic Repo against a REAL embedded postgres —
// the same correctness oracle as the e2e CRUD-lifecycle gate, but at the
// library level so a regression localizes here instead of in a scaffolded
// project. They boot postgres, so they're skipped under -short (the inner
// loop), matching the epic's velocity rule.

func newRepoTestDB(t *testing.T) orm.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("repo pg tests boot embedded postgres; skipped under -short")
	}
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	client, err := orm.NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("orm.NewClientWithDB: %v", err)
	}
	return client
}

// widget is a soft-deleting, timestamped entity with an array column and a
// server-allocated PK — it touches every Repo code path at once (the
// lifecycle gate's "maximum coverage in one entity" stance).
type widget struct {
	bun.BaseModel `bun:"table:widgets,alias:widgets"`

	ID        int64      `bun:"id,pk,autoincrement"`
	Name      string     `bun:"name,notnull"`
	Tags      []string   `bun:"tags,array,notnull"`
	CreatedAt time.Time  `bun:"created_at,notnull"`
	UpdatedAt time.Time  `bun:"updated_at,notnull"`
	DeletedAt *time.Time `bun:"deleted_at,soft_delete,nullzero"`
}

func createWidgetsTable(t *testing.T, db orm.Context) {
	t.Helper()
	_, err := db.Bun().NewCreateTable().Model((*widget)(nil)).
		IfNotExists().Exec(context.Background())
	if err != nil {
		t.Fatalf("create widgets table: %v", err)
	}
}

func TestRepoLifecycle_WidgetFullSurface(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)

	repo := NewRepo[widget]()

	// ── Create: server PK populated, timestamps stamped, nil array → {} ──
	w := &widget{Name: "alpha"} // Tags nil on purpose
	if err := repo.Create(ctx, db, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w.ID == 0 {
		t.Error("Create did not read back the server-allocated PK")
	}
	if w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() {
		t.Error("Create did not stamp managed timestamps")
	}

	// Read back: array bound as {} (non-NULL).
	got, err := repo.Get(ctx, db, w.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tags == nil {
		t.Error("nil array was not normalized to a non-nil empty slice on insert")
	}

	// ── Update (full): updated_at advances, array round-trips ──
	firstUpdatedAt := got.UpdatedAt
	time.Sleep(2 * time.Millisecond)
	got.Name = "alpha-2"
	got.Tags = []string{"x", "y"}
	if err := repo.Update(ctx, db, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reread, _ := repo.Get(ctx, db, w.ID)
	if reread.Name != "alpha-2" {
		t.Errorf("Update did not persist name: %q", reread.Name)
	}
	if len(reread.Tags) != 2 {
		t.Errorf("Update did not persist array: %v", reread.Tags)
	}
	if !reread.UpdatedAt.After(firstUpdatedAt) {
		t.Errorf("Update did not advance updated_at: %v !> %v", reread.UpdatedAt, firstUpdatedAt)
	}

	// ── UpdateMasked: only the named column changes (non-clobber) ──
	masked := &widget{ID: w.ID, Name: "SHOULD-NOT-WIN", Tags: []string{"only-tags"}}
	if err := repo.UpdateMasked(ctx, db, masked, []string{"tags"}); err != nil {
		t.Fatalf("UpdateMasked: %v", err)
	}
	afterMask, _ := repo.Get(ctx, db, w.ID)
	if afterMask.Name != "alpha-2" {
		t.Errorf("masked update clobbered an unmasked column: name=%q want alpha-2", afterMask.Name)
	}
	if len(afterMask.Tags) != 1 || afterMask.Tags[0] != "only-tags" {
		t.Errorf("masked update did not write the masked column: %v", afterMask.Tags)
	}

	// ── UpdateMasked: unknown / immutable path → UnknownFieldError ──
	var unknown *orm.UnknownFieldError
	err = repo.UpdateMasked(ctx, db, masked, []string{"id"})
	if !errors.As(err, &unknown) || unknown.Field != "id" {
		t.Errorf("masked update of PK should be UnknownFieldError{id}, got %v", err)
	}

	// ── soft delete: row survives with deleted_at set, vanishes from reads ──
	if err := repo.Delete(ctx, db, w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, db, w.ID); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("soft-deleted row should be invisible to Get, got %v", err)
	}
	live, _ := repo.List(ctx, db)
	if len(live) != 0 {
		t.Errorf("soft-deleted row should be invisible to List, got %d rows", len(live))
	}
	all, _ := repo.ListAll(ctx, db)
	if len(all) != 1 {
		t.Errorf("ListAll should include the tombstone, got %d rows", len(all))
	}
	if all[0].DeletedAt == nil {
		t.Error("soft delete did not stamp deleted_at (decorative soft delete)")
	}

	// ── update-guard: an UPDATE must not resurrect/mutate a tombstone,
	//    and must SAY it changed nothing rather than report success ──
	all[0].Name = "zombie"
	if err := repo.Update(ctx, db, all[0]); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Update on a tombstone = %v, want orm.ErrNoRows — the row is not visible to Get either", err)
	}
	stillTomb, _ := repo.ListAll(ctx, db)
	if stillTomb[0].Name == "zombie" {
		t.Error("UPDATE mutated a soft-deleted row — the deleted_at IS NULL guard is missing")
	}

	// ── the row is already gone: a second Delete is a miss, not a hit ──
	if err := repo.Delete(ctx, db, w.ID); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("re-deleting a tombstoned row = %v, want orm.ErrNoRows", err)
	}
}

// TestRepo_WriteToMissingRowIsNotFound_PG is the real-database half of the
// Get/Delete agreement. Reading RowsAffected is the only way a repository
// can tell "row written" from "no such row" — an UPDATE/DELETE never
// produces sql.ErrNoRows — and without it every generated Delete answered a
// nonexistent id with 200 and an empty body while Get answered 404.
func TestRepo_WriteToMissingRowIsNotFound_PG(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	repo := NewRepo[widget]()

	const missing = int64(987654)
	if err := repo.Delete(ctx, db, missing); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Delete of an id that never existed = %v, want orm.ErrNoRows", err)
	}
	if err := repo.Update(ctx, db, &widget{ID: missing, Name: "x"}); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Update of an id that never existed = %v, want orm.ErrNoRows", err)
	}
	if err := repo.UpdateMasked(ctx, db, &widget{ID: missing, Name: "x"}, []string{"name"}); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("UpdateMasked of an id that never existed = %v, want orm.ErrNoRows", err)
	}

	// And the negative: a write that DOES match a row must still succeed.
	// A guard that fails everything is not a guard.
	w := &widget{Name: "real"}
	if err := repo.Create(ctx, db, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	w.Name = "real-2"
	if err := repo.Update(ctx, db, w); err != nil {
		t.Fatalf("Update of an existing row must succeed: %v", err)
	}
	if err := repo.Delete(ctx, db, w.ID); err != nil {
		t.Fatalf("Delete of an existing row must succeed: %v", err)
	}
}

// gadgetExtra stands in for a generated <Entity>Extra type: a computed /
// in-memory-only field carrier embedded into the generated entity struct.
// Every field must carry bun:"-" or Bun would try to persist it.
type gadgetExtra struct {
	DisplayLabel string `bun:"-"`
}

// gadget mirrors the shape the generator now emits for an entity whose
// repo-ext seam declares an Extra type: the ordinary Bun-tagged columns,
// plus the embedded Extra struct as the LAST field (see writeEntityStruct).
type gadget struct {
	bun.BaseModel `bun:"table:gadgets,alias:gadgets"`

	ID   int64  `bun:"id,pk,autoincrement"`
	Name string `bun:"name,notnull"`
	gadgetExtra
}

func createGadgetsTable(t *testing.T, db orm.Context) {
	t.Helper()
	_, err := db.Bun().NewCreateTable().Model((*gadget)(nil)).
		IfNotExists().Exec(context.Background())
	if err != nil {
		t.Fatalf("create gadgets table: %v", err)
	}
}

// TestRepoLifecycle_ComputedFieldSeamNeverPersistedOrScanned is the
// runtime half of the computed-field seam: a model with an embedded
// struct whose fields are all bun:"-" must round-trip through
// Create/Get/Update completely unaffected, and the computed field must
// never reach the database — no column, no write, no read.
func TestRepoLifecycle_ComputedFieldSeamNeverPersistedOrScanned(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createGadgetsTable(t, db)

	// The embedded struct must contribute NO column to the actual table —
	// confirmed against information_schema rather than Bun's in-memory
	// Table (that would only prove the generator's premise about Bun's
	// reflection, not that the seam survives an actual CREATE TABLE).
	var colCount int
	if err := db.Bun().NewSelect().
		ColumnExpr("count(*)").
		Table("information_schema.columns").
		Where("table_name = ?", "gadgets").
		Scan(ctx, &colCount); err != nil {
		t.Fatalf("introspect gadgets columns: %v", err)
	}
	if colCount != 2 { // id, name — NOT display_label
		t.Errorf("gadgets table has %d columns, want 2 (id, name) — the bun:\"-\" computed field must not become a column", colCount)
	}

	repo := NewRepo[gadget]()

	g := &gadget{Name: "widget-shaped"}
	g.DisplayLabel = "set before Create"
	if err := repo.Create(ctx, db, g); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.ID == 0 {
		t.Error("Create did not read back the server-allocated PK")
	}
	// The computed field is in-memory only: it survives on the same Go
	// value (nothing zeroes it), but a fresh read never populates it.
	if g.DisplayLabel != "set before Create" {
		t.Errorf("Create must not clear the caller's in-memory computed field, got %q", g.DisplayLabel)
	}

	got, err := repo.Get(ctx, db, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "widget-shaped" {
		t.Errorf("Get did not read back Name: %q", got.Name)
	}
	if got.DisplayLabel != "" {
		t.Errorf("Get scanned a value into the computed field %q — it must never be read from the DB", got.DisplayLabel)
	}

	got.Name = "renamed"
	got.DisplayLabel = "set before Update"
	if err := repo.Update(ctx, db, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reread, err := repo.Get(ctx, db, g.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if reread.Name != "renamed" {
		t.Errorf("Update did not persist Name: %q", reread.Name)
	}
	if reread.DisplayLabel != "" {
		t.Errorf("Update wrote the computed field to the DB — reread got %q, want empty (never persisted)", reread.DisplayLabel)
	}
}
