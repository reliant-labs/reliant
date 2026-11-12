package crud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
)

// archivedRow is what the generator emits for a deleted_at declared
// `forge:no-soft-delete` (pkg/schemadef.ColumnMarkerNoSoftDelete): an ordinary
// nullable timestamp, with neither Bun's ,soft_delete nor forge's soft-delete
// tag. reliant's worktrees.deleted_at is this shape — an ARCHIVE stamp, where
// an archived row must stay readable and writable and Delete must delete.
type archivedRow struct {
	bun.BaseModel `bun:"table:archived_rows,alias:archived_rows"`

	ID        string     `bun:"id,pk"`
	Name      string     `bun:"name,notnull"`
	DeletedAt *time.Time `bun:"deleted_at"`
}

// legacyTombstone is the legacy TEXT soft delete: a deleted_at Bun's
// ,soft_delete cannot own, DECLARED with forge's soft-delete tag.
type legacyTombstone struct {
	bun.BaseModel `bun:"table:legacy_tombstones,alias:legacy_tombstones"`

	ID        string  `bun:"id,pk"`
	Name      string  `bun:"name,notnull"`
	DeletedAt *string `bun:"deleted_at" forge:"soft-delete"`
}

// The hermetic half: soft delete is read from a TAG, never inferred from a
// column called deleted_at. Before the fix the repo soft-deleted every column
// of that name Bun's ,soft_delete had not claimed, so archivedRow came back
// legacyTextSoftDel=true with deleted_at barred from every write.
func TestSoftDeleteIsDeclaredNotInferredFromTheColumnName(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}

	plain := NewRepo[archivedRow]()
	plain.ensureMeta(db)
	if plain.m.legacyTextSoftDel || plain.m.nativeSoftDel {
		t.Fatalf("an untagged deleted_at must not soft-delete: legacy=%v native=%v", plain.m.legacyTextSoftDel, plain.m.nativeSoftDel)
	}
	if !containsCol(plain.m.updatable, "deleted_at") || !plain.m.updatableSet["deleted_at"] {
		t.Errorf("an ordinary deleted_at must be writable by Update and UpdateMasked: updatable=%v", plain.m.updatable)
	}

	legacy := NewRepo[legacyTombstone]()
	legacy.ensureMeta(db)
	if !legacy.m.legacyTextSoftDel {
		t.Fatal(`a deleted_at tagged forge:"soft-delete" must soft-delete`)
	}
	if containsCol(legacy.m.updatable, "deleted_at") || legacy.m.updatableSet["deleted_at"] {
		t.Errorf("a soft-delete column must stay out of both write sets: updatable=%v", legacy.m.updatable)
	}

	native := NewRepo[widget]()
	native.ensureMeta(db)
	if !native.m.nativeSoftDel || native.m.updatableSet["deleted_at"] {
		t.Errorf("Bun's ,soft_delete column must soft-delete and stay unwritable: native=%v set=%v", native.m.nativeSoftDel, native.m.updatableSet)
	}
}

// The behaviour against real postgres: archived rows are visible to Get and
// List, Update writes and clears the stamp, and Delete removes the row.
func TestRepoNoSoftDelete_ArchivedRowsVisibleAndDeleteHardDeletes(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE TABLE archived_rows (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    deleted_at TIMESTAMPTZ
)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	repo := NewRepo[archivedRow]()

	live := &archivedRow{ID: "live", Name: "live"}
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	archived := &archivedRow{ID: "archived", Name: "archived", DeletedAt: &stamp}
	for _, r := range []*archivedRow{live, archived} {
		if err := repo.Create(ctx, db, r); err != nil {
			t.Fatalf("Create %s: %v", r.ID, err)
		}
	}

	got, err := repo.Get(ctx, db, "archived")
	if err != nil {
		t.Fatalf("Get of an archived row: %v — an archive stamp is not a tombstone", err)
	}
	if got.DeletedAt == nil || !got.DeletedAt.Equal(stamp) {
		t.Errorf("archived row's deleted_at = %v, want %v", got.DeletedAt, stamp)
	}
	rows, err := repo.List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("List returned %d rows, want 2 (archived rows included)", len(rows))
	}

	// Update writes an archived row, and can write the stamp itself.
	got.Name = "renamed"
	got.DeletedAt = nil
	if err := repo.Update(ctx, db, got); err != nil {
		t.Fatalf("Update of an archived row: %v", err)
	}
	reread, _ := repo.Get(ctx, db, "archived")
	if reread.Name != "renamed" || reread.DeletedAt != nil {
		t.Errorf("Update did not write the archived row: name=%q deleted_at=%v", reread.Name, reread.DeletedAt)
	}
	live.DeletedAt = &stamp
	if err := repo.UpdateMasked(ctx, db, live, []string{"deleted_at"}); err != nil {
		t.Fatalf("UpdateMasked naming deleted_at: %v", err)
	}
	if reread, _ := repo.Get(ctx, db, "live"); reread.DeletedAt == nil {
		t.Error("UpdateMasked did not stamp deleted_at")
	}

	// Delete deletes.
	if err := repo.Delete(ctx, db, "archived"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var n int
	if err := db.Bun().NewRaw(`SELECT count(*) FROM archived_rows WHERE id = 'archived'`).Scan(ctx, &n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("Delete left the row in place (%d rows): it must be a hard DELETE, not a stamp", n)
	}
	if _, err := repo.Get(ctx, db, "archived"); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Get after Delete = %v, want not found", err)
	}
}
