package orm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// database/sql code on the Client's pool (sqlc through DBTX, hand SQL through
// SQLConn) must be one transaction with the ORM, opened from either side.

var errRollback = errors.New("roll it back")

func TestDBTX_JoinsRunTxAndRollsBackWithIt(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	sqlc := c.DBTX()

	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if !c.InTx(ctx) {
			t.Error("InTx is false inside RunTx")
		}
		if _, err := sqlc.ExecContext(ctx, `INSERT INTO tx_notes (id, body) VALUES ($1, 'b')`, "sql"); err != nil {
			return err
		}
		if err := insertNote(ctx, c, "orm"); err != nil {
			return err
		}
		if got := countNotes(t, c); got != 0 {
			t.Errorf("an outside observer sees %d rows before commit; both inserts must be in the transaction", got)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("RunTx: %v, want the fn's error", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("%d rows survived the rollback", got)
	}
}

// A layer that owns its transaction (BeginTx, its own retry loop) hands work
// tx.RunTx's ctx: the ORM and database/sql both join it, and its Commit is
// what makes them visible.
func TestSQLConn_JoinsABeginTxTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	tx, err := c.BeginDetached(ctx, ReadCommitted)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	err = tx.Join(ctx, func(ctx context.Context) error {
		if _, err := c.SQLConn(ctx).ExecContext(ctx, `INSERT INTO tx_notes (id, body) VALUES ($1, 'b')`, "sql"); err != nil {
			return err
		}
		// The ORM reads the uncommitted database/sql write: one transaction.
		n, err := c.Bun().NewSelect().Model((*txNote)(nil)).Where("id = ?", "sql").Count(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("the ORM sees %d of the transaction's own rows, want 1", n)
		}
		return nil
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("tx.RunTx: %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("an outside observer sees %d rows before Commit", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := countNotes(t, c); got != 1 {
		t.Fatalf("%d rows after Commit, want 1", got)
	}
	if c.InTx(ctx) {
		t.Fatal("InTx is true for a ctx that never carried the transaction")
	}
}

// A row the transaction locked through the ORM is written again through
// DBTX with the same ctx. On a second connection that statement would wait
// on the lock forever; joined, it completes. The deadline bounds the failure.
func TestDBTX_DoesNotDeadlockOnARowTheTransactionLocked(t *testing.T) {
	c := newTxTestClient(t)
	if err := insertNote(context.Background(), c, "locked"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if _, err := c.Bun().NewUpdate().Model((*txNote)(nil)).Set("body = ?", "orm").Where("id = ?", "locked").Exec(ctx); err != nil {
			return err
		}
		_, err := c.DBTX().ExecContext(ctx, `UPDATE tx_notes SET body = 'sql' WHERE id = $1`, "locked")
		return err
	})
	if err != nil {
		t.Fatalf("RunTx: %v (a deadline here means DBTX ran outside the transaction)", err)
	}
	var body string
	if err := c.SQLConn(context.Background()).QueryRowContext(context.Background(), `SELECT body FROM tx_notes WHERE id = $1`, "locked").Scan(&body); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if body != "sql" {
		t.Fatalf("body = %q, want the second write's %q", body, "sql")
	}
}

// A ctx captured inside RunTx and used after it returned resolves to the
// pool, as every other Client path does.
func TestSQLConn_AFinishedTransactionResolvesToThePool(t *testing.T) {
	c := newTxTestClient(t)
	var leaked context.Context
	if err := c.RunTx(context.Background(), Serializable, func(ctx context.Context) error {
		leaked = ctx
		return nil
	}); err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	if c.InTx(leaked) {
		t.Fatal("InTx is true for a committed transaction")
	}
	if _, err := c.DBTX().ExecContext(leaked, `INSERT INTO tx_notes (id, body) VALUES ($1, 'b')`, "after"); err != nil {
		t.Fatalf("exec with a finished transaction's ctx: %v", err)
	}
	if got := countNotes(t, c); got != 1 {
		t.Fatalf("%d rows, want the pool write's 1", got)
	}
}
