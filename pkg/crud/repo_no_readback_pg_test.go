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

// NoReadBack makes a masked update exec-only: nothing is scanned back, so
// the entity's unwritten and generated fields keep what the caller put in
// them, while the row itself is written exactly as without the option.
func TestRepoUpdateMasked_NoReadBackIsExecOnly(t *testing.T) {
	db := newReturningTestDB(t)
	ctx := context.Background()
	repo := NewRepo[lineItem]()

	if err := repo.Create(ctx, db, &lineItem{ID: "li-1", Quantity: 3, UnitCents: 250}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stale := "stale"
	partial := &lineItem{ID: "li-1", Quantity: 5, UnitCents: 999, Note: &stale}
	if err := repo.UpdateMasked(ctx, db, partial, []string{"quantity"}, NoReadBack()); err != nil {
		t.Fatalf("UpdateMasked NoReadBack: %v", err)
	}
	if partial.UnitCents != 999 || noteOf(partial.Note) != "stale" || partial.TotalCents != 0 {
		t.Errorf("NoReadBack scanned into the entity: %+v", partial)
	}

	stored, err := repo.Get(ctx, db, "li-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Quantity != 5 || stored.UnitCents != 250 || stored.TotalCents != 1250 || noteOf(stored.Note) != "none" {
		t.Errorf("stored row = %+v, want quantity 5 written and every other column untouched", stored)
	}

	// The default is unchanged: the same write reads the row back.
	partial.Quantity = 6
	if err := repo.UpdateMasked(ctx, db, partial, []string{"quantity"}); err != nil {
		t.Fatalf("UpdateMasked: %v", err)
	}
	if partial.UnitCents != 250 || noteOf(partial.Note) != "none" || partial.TotalCents != 1500 {
		t.Errorf("default masked update did not read back: %+v", partial)
	}

	// The full replace honors it too.
	full := &lineItem{ID: "li-1", Quantity: 7, UnitCents: 10}
	if err := repo.Update(ctx, db, full, NoReadBack()); err != nil {
		t.Fatalf("Update NoReadBack: %v", err)
	}
	if full.TotalCents != 0 {
		t.Errorf("NoReadBack full update scanned the generated column: %d", full.TotalCents)
	}

	// A write that matches nothing is still NotFound.
	missing := &lineItem{ID: "nope", Quantity: 1}
	if err := repo.UpdateMasked(ctx, db, missing, []string{"quantity"}, NoReadBack()); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("NoReadBack on a missing row: %v, want orm.ErrNoRows", err)
	}

	// Preserve is a full-replace option; a mask cannot honor it.
	if err := repo.UpdateMasked(ctx, db, partial, []string{"quantity"}, Preserve("note")); err == nil {
		t.Error("UpdateMasked accepted Preserve")
	}
}

// runEvent is the spike's MarkDispatched shape: a wide outbox row whose
// worker stamps one column.
type runEvent struct {
	bun.BaseModel `bun:"table:run_events,alias:run_events"`

	ID           string     `bun:"id,pk"`
	ChatID       string     `bun:"chat_id,notnull"`
	ThreadID     string     `bun:"thread_id,notnull"`
	Kind         string     `bun:"kind,notnull"`
	Payload      string     `bun:"payload,notnull,type:jsonb"`
	Outcome      string     `bun:"outcome,notnull"`
	Attempts     int64      `bun:"attempts,notnull"`
	Error        *string    `bun:"error"`
	ClaimedUntil *time.Time `bun:"claimed_until"`
	DispatchedAt *time.Time `bun:"dispatched_at"`
	CreatedAt    time.Time  `bun:"created_at,notnull"`
	UpdatedAt    time.Time  `bun:"updated_at,notnull"`
}

// BenchmarkRepoUpdateMasked_OneColumnStamp measures the read-back cost F12.10
// removes: one column stamped on a 12-column row, with and without
// NoReadBack. Compare allocs/op and B/op.
func BenchmarkRepoUpdateMasked_OneColumnStamp(b *testing.B) {
	if testing.Short() {
		b.Skip("boots postgres; skipped under -short")
	}
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		b.Fatalf("pgtest.New: %v", err)
	}
	b.Cleanup(cleanup)
	db, err := orm.NewClientWithDB(sqldb, "postgres")
	if err != nil {
		b.Fatalf("orm.NewClientWithDB: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Bun().NewCreateTable().Model((*runEvent)(nil)).Exec(ctx); err != nil {
		b.Fatalf("create run_events: %v", err)
	}
	repo := NewRepo[runEvent]()
	ev := &runEvent{ID: "ev-1", ChatID: "chat", ThreadID: "thread", Kind: "message", Payload: `{"a":1}`, Outcome: "pending"}
	if err := repo.Create(ctx, db, ev); err != nil {
		b.Fatalf("Create: %v", err)
	}

	for _, tc := range []struct {
		name string
		opts []UpdateOption
	}{
		{"readback", nil},
		{"no-readback", []UpdateOption{NoReadBack()}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				now := time.Now()
				ev.DispatchedAt = &now
				if err := repo.UpdateMasked(ctx, db, ev, []string{"dispatched_at"}, tc.opts...); err != nil {
					b.Fatalf("UpdateMasked: %v", err)
				}
			}
		})
	}
}
