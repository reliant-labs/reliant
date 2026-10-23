package crud

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A managed timestamp is a default: Create fills one the caller left unset
// and keeps one the caller set, for updated_at exactly as for created_at.
// Create used to overwrite a caller-set updated_at with now, so a row
// imported or replayed with its own times lost them.
func TestRepoCreate_KeepsCallerSetTimestamps(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	repo := NewRepo[widget]()

	created := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	w := &widget{Name: "imported", Tags: []string{}, CreatedAt: created, UpdatedAt: updated}
	if err := repo.Create(ctx, db, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, db, w.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("stored created_at %v / updated_at %v; want the caller's %v / %v", got.CreatedAt, got.UpdatedAt, created, updated)
	}

	before := time.Now().Add(-time.Second)
	fresh := &widget{Name: "fresh", Tags: []string{}}
	if err := repo.Create(ctx, db, fresh); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if fresh.CreatedAt.Before(before) || !fresh.UpdatedAt.Equal(fresh.CreatedAt) {
		t.Fatalf("unset stamps = %v / %v; want both now, one instant", fresh.CreatedAt, fresh.UpdatedAt)
	}

	// Update is not Create: an entity read back carries its old updated_at,
	// and the write still re-stamps it.
	got.Name = "edited"
	if err := repo.Update(ctx, db, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.UpdatedAt.After(before) {
		t.Fatalf("Update kept updated_at %v; want it re-stamped", got.UpdatedAt)
	}
}

// Upsert follows Create's rule on both branches: the row ends with the
// caller's updated_at when the caller set one, and now when not, whichever
// branch ran.
func TestRepoUpsert_KeepsCallerSetUpdatedAt(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createDocumentCountersStampedTable(t, db)
	repo := NewRepo[stampedCounter]()

	at := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	row := &stampedCounter{CompanyID: "c1", Kind: "invoice", NextValue: 1, UpdatedAt: at}
	if err := repo.Upsert(ctx, db, row); err != nil { // insert branch
		t.Fatalf("Upsert insert: %v", err)
	}
	got, err := repo.GetByPK(ctx, db, &stampedCounter{CompanyID: "c1", Kind: "invoice"})
	if err != nil || !got.UpdatedAt.Equal(at) {
		t.Fatalf("after insert-branch upsert: %+v, %v; want updated_at %v", got, err, at)
	}

	later := at.Add(48 * time.Hour)
	if err := repo.Upsert(ctx, db, &stampedCounter{CompanyID: "c1", Kind: "invoice", NextValue: 2, UpdatedAt: later}); err != nil {
		t.Fatalf("Upsert conflict: %v", err)
	}
	if got, err = repo.GetByPK(ctx, db, &stampedCounter{CompanyID: "c1", Kind: "invoice"}); err != nil || !got.UpdatedAt.Equal(later) || got.NextValue != 2 {
		t.Fatalf("after conflict-branch upsert: %+v, %v; want updated_at %v", got, err, later)
	}

	before := time.Now().Add(-time.Second)
	if err := repo.Upsert(ctx, db, &stampedCounter{CompanyID: "c1", Kind: "invoice", NextValue: 3}); err != nil {
		t.Fatalf("Upsert unset: %v", err)
	}
	if got, err = repo.GetByPK(ctx, db, &stampedCounter{CompanyID: "c1", Kind: "invoice"}); err != nil || got.UpdatedAt.Before(before) {
		t.Fatalf("after unset upsert: %+v, %v; want updated_at now", got, err)
	}
}

// stampedCounter is a composite-key table with managed timestamps.
type stampedCounter struct {
	bun.BaseModel `bun:"table:stamped_counters,alias:stamped_counters"`

	CompanyID string    `bun:"company_id,pk"`
	Kind      string    `bun:"kind,pk"`
	NextValue int64     `bun:"next_value,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

func createDocumentCountersStampedTable(t *testing.T, db orm.Context) {
	t.Helper()
	if _, err := db.Bun().NewCreateTable().Model((*stampedCounter)(nil)).IfNotExists().Exec(context.Background()); err != nil {
		t.Fatalf("create stamped_counters: %v", err)
	}
}

// widgetView reads a view over widgets (a view entity's store).
type widgetView struct {
	bun.BaseModel `bun:"table:widget_names,alias:widget_names"`

	ID   int64   `bun:"id,pk"`
	Name *string `bun:"name"`
}

// Every by-key verb answers a missing row the same way: svcerr.NotFound
// naming the entity — what a service returns and svcerr.Wrap maps to
// CodeNotFound — still errors.Is-true for orm.ErrNoRows. Get used to say
// one and Update/Delete the other, so a caller of both checked both.
func TestRepo_MissIsOneAnswerAcrossByKeyVerbs(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	createDocumentCountersTable(t, db)
	if _, err := db.Exec(ctx, `CREATE VIEW widget_names AS SELECT id, name FROM widgets`); err != nil {
		t.Fatalf("create view: %v", err)
	}
	widgets := NewRepo[widget]()
	counters := NewRepo[documentCounter]()
	views := NewRepo[widgetView]()
	const missing = int64(987654)
	missingKey := &documentCounter{CompanyID: "nope", Kind: "none", NextValue: 1}

	for _, tc := range []struct {
		verb, entity string
		err          error
	}{
		{"Get", "widget", second(widgets.Get(ctx, db, missing))},
		{"Update", "widget", widgets.Update(ctx, db, &widget{ID: missing, Name: "x"})},
		{"UpdateMasked", "widget", widgets.UpdateMasked(ctx, db, &widget{ID: missing, Name: "x"}, []string{"name"})},
		{"Delete", "widget", widgets.Delete(ctx, db, missing)},
		{"GetByPK", "documentcounter", second(counters.GetByPK(ctx, db, missingKey))},
		{"Update (composite)", "documentcounter", counters.Update(ctx, db, missingKey)},
		{"DeleteByPK", "documentcounter", counters.DeleteByPK(ctx, db, missingKey)},
		{"Get (view)", "widgetview", second(views.Get(ctx, db, missing))},
		{"ScopedGet", "widget", second(ScopedGet(ctx, "id", "987654", []orm.QueryOption{orm.WhereEq("name", "x")},
			func(ctx context.Context, _ string) (*widget, error) { return nil, nil },
			func(ctx context.Context, opts ...orm.QueryOption) ([]*widget, error) {
				return widgets.List(ctx, db, opts...)
			}))},
	} {
		if !svcerr.IsNotFound(tc.err) || !errors.Is(tc.err, orm.ErrNoRows) {
			t.Errorf("%s of a missing row = %v; want svcerr.NotFound that is also orm.ErrNoRows", tc.verb, tc.err)
			continue
		}
		var ce *connect.Error
		if !errors.As(svcerr.Wrap(tc.err), &ce) || ce.Code() != connect.CodeNotFound || ce.Message() != tc.entity+" not found" {
			t.Errorf("%s: svcerr.Wrap = %v; want not_found %q", tc.verb, svcerr.Wrap(tc.err), tc.entity+" not found")
		}
	}
}

func second[T any](_ T, err error) error { return err }

// List with no limit reads every matching row. It used to stop at 10,000
// without saying so: a caller asking for all of a table got the first 10,000.
func TestRepoList_NoLimitReadsEveryRow(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	const n = 10_001
	if _, err := db.Exec(ctx, `INSERT INTO widgets (name, tags, created_at, updated_at)
		SELECT 'w' || g, '{}', now(), now() FROM generate_series(1, $1) g`, n); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := NewRepo[widget]()
	got, err := repo.List(ctx, db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != n {
		t.Fatalf("limit-less List returned %d rows of %d", len(got), n)
	}
	if got, err := repo.List(ctx, db, orm.WithLimit(2)); err != nil || len(got) != 2 {
		t.Fatalf("List(WithLimit(2)) = %d rows, %v; want 2", len(got), err)
	}
}
