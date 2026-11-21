package crud

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// A NOT NULL column with a DEFAULT must take that DEFAULT when the caller
// did not set the field, and must store an explicit zero when it did
// (reliant P5b, F12.5). The generated CreateThread stored `status = 0` for
// `status INTEGER NOT NULL DEFAULT 2`, and a `jsonb NOT NULL DEFAULT '{}'`
// insert failed with `""` (22P02).
//
// thread is tagged exactly as internal/generator tags these shapes
// (TestPlanORM_NotNullDefaultsSayUnsetWithTheirType pins the generator half):
// a scalar whose DEFAULT is not its Go zero is a pointer, a json document
// and an array with a non-empty DEFAULT take ,nullzero, and a DEFAULT equal
// to the zero changes nothing.
type thread struct {
	bun.BaseModel `bun:"table:threads,alias:threads"`

	ID       string          `bun:"id,pk"`
	Title    string          `bun:"title,notnull"`
	Status   *int64          `bun:"status,notnull"`
	Pinned   *bool           `bun:"pinned,notnull"`
	Kind     *string         `bun:"kind,notnull"`
	Metadata json.RawMessage `bun:"metadata,notnull,nullzero"`
	Labels   []string        `bun:"labels,array,notnull,nullzero"`
	Unread   int64           `bun:"unread,notnull"`
	Tone     string          `bun:"tone,notnull,nullzero"` // a born enum column: its CHECK refuses ''
}

func newThreadsTestDB(t *testing.T) orm.Context {
	t.Helper()
	db := newRepoTestDB(t)
	if _, err := db.Bun().ExecContext(context.Background(), `CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		status INTEGER NOT NULL DEFAULT 2,
		pinned BOOLEAN NOT NULL DEFAULT true,
		kind TEXT NOT NULL DEFAULT 'llm',
		metadata JSONB NOT NULL DEFAULT '{}',
		labels TEXT[] NOT NULL DEFAULT '{inbox}',
		unread BIGINT NOT NULL DEFAULT 0,
		tone TEXT NOT NULL DEFAULT 'WARM' CHECK (tone IN ('WARM', 'COOL')))`); err != nil {
		t.Fatalf("create threads: %v", err)
	}
	return db
}

func ptr[T any](v T) *T { return &v }

func storedThread(t *testing.T, db orm.Context, id string) thread {
	t.Helper()
	var got thread
	if err := db.Bun().NewSelect().Model(&got).Where("id = ?", id).Scan(context.Background()); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return got
}

func TestCreate_UnsetNotNullColumnsTakeTheirDefault(t *testing.T) {
	db := newThreadsTestDB(t)
	repo := NewRepo[thread]()
	ctx := context.Background()

	row := &thread{ID: "t1", Title: "hello"}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("Create with every defaulted column unset: %v", err)
	}
	got := storedThread(t, db, "t1")
	if got.Status == nil || *got.Status != 2 {
		t.Errorf("status = %v, want the DEFAULT 2", got.Status)
	}
	if got.Pinned == nil || !*got.Pinned {
		t.Errorf("pinned = %v, want the DEFAULT true", got.Pinned)
	}
	if got.Kind == nil || *got.Kind != "llm" {
		t.Errorf("kind = %v, want the DEFAULT 'llm'", got.Kind)
	}
	if string(got.Metadata) != "{}" {
		t.Errorf("metadata = %q, want the DEFAULT '{}'", got.Metadata)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "inbox" {
		t.Errorf("labels = %v, want the DEFAULT {inbox}", got.Labels)
	}
	if got.Tone != "WARM" {
		t.Errorf("tone = %q, want the DEFAULT 'WARM' (an unset enum, not a CHECK violation)", got.Tone)
	}
	// RETURNING: the caller's struct holds what was stored.
	if row.Status == nil || *row.Status != 2 || row.Pinned == nil || !*row.Pinned || string(row.Metadata) != "{}" {
		t.Errorf("after Create the entity does not hold the stored defaults: %+v", row)
	}
}

func TestCreate_ExplicitZeroIsStored(t *testing.T) {
	db := newThreadsTestDB(t)
	repo := NewRepo[thread]()
	ctx := context.Background()

	if err := repo.Create(ctx, db, &thread{ID: "t2", Title: "x", Status: ptr[int64](0), Pinned: ptr(false), Kind: ptr("")}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := storedThread(t, db, "t2")
	if *got.Status != 0 || *got.Pinned || *got.Kind != "" {
		t.Errorf("an explicit zero was replaced by the DEFAULT: status=%d pinned=%v kind=%q", *got.Status, *got.Pinned, *got.Kind)
	}
}

// Unset means the same thing on an Update: keep what is stored — never NULL
// into a NOT NULL column, never a reset to the DEFAULT.
func TestUpdate_UnsetColumnsKeepTheStoredValue(t *testing.T) {
	db := newThreadsTestDB(t)
	repo := NewRepo[thread]()
	ctx := context.Background()

	if err := repo.Create(ctx, db, &thread{ID: "t3", Title: "x", Status: ptr[int64](5), Metadata: json.RawMessage(`{"a":1}`), Labels: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	upd := &thread{ID: "t3", Title: "renamed"}
	if err := repo.Update(ctx, db, upd); err != nil {
		t.Fatalf("Update with defaulted columns unset: %v", err)
	}
	got := storedThread(t, db, "t3")
	if got.Title != "renamed" || *got.Status != 5 || string(got.Metadata) != `{"a": 1}` || len(got.Labels) != 1 || got.Labels[0] != "x" {
		t.Errorf("stored after Update = %+v (status %d), want title renamed and the rest kept", got, *got.Status)
	}
	if upd.Status == nil || *upd.Status != 5 {
		t.Errorf("Update must read the kept column back; status = %v", upd.Status)
	}
	if err := repo.Update(ctx, db, &thread{ID: "t3", Title: "again", Status: ptr[int64](0)}); err != nil {
		t.Fatal(err)
	}
	if got := storedThread(t, db, "t3"); *got.Status != 0 {
		t.Errorf("an explicit zero on Update must be written; status = %d", *got.Status)
	}
}

func TestUpsert_UnsetColumnsDefaultOnInsertAndKeepOnConflict(t *testing.T) {
	db := newThreadsTestDB(t)
	repo := NewRepo[thread]()
	ctx := context.Background()

	if err := repo.Upsert(ctx, db, &thread{ID: "t4", Title: "new"}); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	if got := storedThread(t, db, "t4"); *got.Status != 2 {
		t.Errorf("insert branch: status = %d, want the DEFAULT 2", *got.Status)
	}
	if err := repo.Update(ctx, db, &thread{ID: "t4", Title: "new", Status: ptr[int64](7)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, db, &thread{ID: "t4", Title: "conflict"}); err != nil {
		t.Fatalf("Upsert conflict: %v", err)
	}
	if got := storedThread(t, db, "t4"); got.Title != "conflict" || *got.Status != 7 {
		t.Errorf("conflict branch: title=%q status=%d, want the title written and status kept at 7", got.Title, *got.Status)
	}
}

// A masked update that names an unset column asks for it to be unset: the
// column goes back to its DEFAULT (SET … = DEFAULT, on the bind path).
func TestUpdateMasked_NamingAnUnsetColumnRestoresItsDefault(t *testing.T) {
	db := newThreadsTestDB(t)
	repo := NewRepo[thread]()
	ctx := context.Background()
	if err := repo.Create(ctx, db, &thread{ID: "t5", Title: "x", Status: ptr[int64](9)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateMasked(ctx, db, &thread{ID: "t5"}, []string{"status"}); err != nil {
		t.Fatalf("UpdateMasked naming an unset column: %v", err)
	}
	if got := storedThread(t, db, "t5"); *got.Status != 2 {
		t.Errorf("status = %d, want the DEFAULT 2", *got.Status)
	}
}
