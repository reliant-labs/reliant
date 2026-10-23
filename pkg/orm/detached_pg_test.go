package orm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Detached is a pure ctx transform: it hides the carried transactions and
// nothing else — no database needed to prove that.
func TestDetached_HidesTransactionsAndKeepsTheRest(t *testing.T) {
	type key struct{}
	parent, cancel := context.WithDeadline(context.WithValue(context.Background(), key{}, "v"), time.Now().Add(time.Hour))
	defer cancel()
	// A ctx carrying an active transaction, as RunTx hands fn.
	carried := withTx(parent, &txState{})

	if !InTx(carried) {
		t.Fatal("precondition: InTx is false for a ctx carrying an active transaction")
	}
	ctx := Detached(carried)
	if InTx(ctx) {
		t.Error("InTx(Detached(ctx)) = true; the transaction must be hidden")
	}
	if err := AfterCommit(ctx, func(context.Context) {}); !errors.Is(err, ErrNoTransaction) {
		t.Errorf("AfterCommit(Detached(ctx)) = %v, want ErrNoTransaction", err)
	}
	if ctx.Value(key{}) != "v" {
		t.Error("Detached dropped the ctx's other values")
	}
	if d, ok := ctx.Deadline(); !ok || !d.Equal(func() time.Time { d, _ := parent.Deadline(); return d }()) {
		t.Error("Detached dropped the ctx's deadline")
	}
	cancel()
	if ctx.Err() == nil {
		t.Error("Detached does not follow its parent's cancellation")
	}
	// A transaction opened UNDER the detached ctx is visible again.
	if !InTx(withTx(ctx, &txState{})) {
		t.Error("a transaction carried above Detached is hidden; only the ones below it should be")
	}
}

// The ledger shape: inside a caller's RunTx, a write made with Detached(ctx)
// commits on its own — on every path that resolves the carried transaction —
// and survives the caller's rollback, while the caller's own write does not.
func TestDetached_WritesOnThePoolAndSurviveTheCallersRollback(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	sqlc := c.DBTX()

	paths := []struct {
		id    string
		write func(ctx context.Context, id string) error
	}{
		{"builder", func(ctx context.Context, id string) error { return insertNote(ctx, c, id) }},
		{"bun-exec", func(ctx context.Context, id string) error {
			_, err := c.Bun().ExecContext(ctx, "INSERT INTO tx_notes (id, body) VALUES (?, 'b')", id)
			return err
		}},
		{"bun-raw", func(ctx context.Context, id string) error {
			_, err := c.Bun().NewRaw("INSERT INTO tx_notes (id, body) VALUES (?, 'b')", id).Exec(ctx)
			return err
		}},
		{"raw-exec", func(ctx context.Context, id string) error {
			_, err := c.Exec(ctx, "INSERT INTO tx_notes (id, body) VALUES ($1, 'b')", id)
			return err
		}},
		{"sqlconn", func(ctx context.Context, id string) error {
			_, err := c.SQLConn(ctx).ExecContext(ctx, "INSERT INTO tx_notes (id, body) VALUES ($1, 'b')", id)
			return err
		}},
		{"dbtx", func(ctx context.Context, id string) error {
			_, err := sqlc.ExecContext(ctx, "INSERT INTO tx_notes (id, body) VALUES ($1, 'b')", id)
			return err
		}},
	}

	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if err := insertNote(ctx, c, "caller"); err != nil {
			return err
		}
		dctx := Detached(ctx)
		if c.InTx(dctx) || InTx(dctx) {
			t.Error("InTx is true for a detached ctx")
		}
		for _, p := range paths {
			if err := p.write(dctx, p.id); err != nil {
				return fmt.Errorf("%s: %w", p.id, err)
			}
		}
		// Committed already: an outside observer sees every detached row
		// and not the caller's uncommitted one.
		if got := countNotes(t, c); got != int64(len(paths)) {
			t.Errorf("outside observer sees %d rows inside the caller's transaction, want the %d detached ones", got, len(paths))
		}
		// And the caller's transaction does not see a detached read of its
		// own uncommitted row.
		n, err := c.Bun().NewSelect().Model((*txNote)(nil)).Where("id = ?", "caller").Count(dctx)
		if err != nil {
			return err
		}
		if n != 0 {
			t.Error("a detached read saw the caller's uncommitted write")
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("RunTx: %v, want the fn's error", err)
	}
	var ids []string
	if err := c.Bun().NewSelect().Model((*txNote)(nil)).Column("id").Order("id").Scan(ctx, &ids); err != nil {
		t.Fatalf("select ids: %v", err)
	}
	if len(ids) != len(paths) {
		t.Fatalf("rows after the caller's rollback = %v, want exactly the %d detached writes", ids, len(paths))
	}
	for _, id := range ids {
		if id == "caller" {
			t.Fatal("the caller's write survived its rollback")
		}
	}
}

// RunTx with a detached ctx begins a NEW transaction instead of joining the
// caller's, and the ctx it hands fn carries that new one.
func TestDetached_RunTxBeginsItsOwnTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	err := c.RunTx(ctx, Serializable, func(outer context.Context) error {
		if err := insertNote(outer, c, "caller"); err != nil {
			return err
		}
		inner := c.RunTx(Detached(outer), Serializable, func(ctx context.Context) error {
			if !c.InTx(ctx) {
				t.Error("InTx is false inside a detached RunTx")
			}
			if err := insertNote(ctx, c, "detached"); err != nil {
				return err
			}
			// Its own transaction: an outside observer sees nothing yet.
			if got := countNotes(t, c); got != 0 {
				t.Errorf("outside observer sees %d rows before the detached commit", got)
			}
			return nil
		})
		if inner != nil {
			return inner
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("RunTx: %v, want the fn's error", err)
	}
	var ids []string
	if err := c.Bun().NewSelect().Model((*txNote)(nil)).Column("id").Scan(ctx, &ids); err != nil {
		t.Fatalf("select ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != "detached" {
		t.Fatalf("rows = %v, want only the detached transaction's commit", ids)
	}
}

// An explicit *Tx handle is not a ctx resolution: Detached does not move a
// statement off it.
func TestDetached_ExplicitTxHandleIsUnaffected(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	tx, err := c.BeginDetached(ctx, ReadCommitted)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.Exec(Detached(ctx), "INSERT INTO tx_notes (id, body) VALUES ('h', 'b')"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if _, err := tx.Bun().NewInsert().Model(&txNote{ID: "hb", Body: "b"}).Exec(Detached(ctx)); err != nil {
		t.Fatalf("tx.Bun insert: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("%d rows survived the handle's rollback; statements on a *Tx are its own", got)
	}
}
