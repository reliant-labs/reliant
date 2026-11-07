package orm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// InTx is a pure ctx probe: no transaction, no database.
func TestInTx_FalseWithoutATransaction(t *testing.T) {
	if InTx(context.Background()) {
		t.Fatal("InTx(context.Background()) = true, want false")
	}
	//nolint:staticcheck // SA1012: a nil ctx must not panic the probe.
	if InTx(nil) {
		t.Fatal("InTx(nil) = true, want false")
	}
}

// InTx tracks exactly the window in which the ctx's queries join a
// transaction: inside RunTx (and a join through Tx.RunTx), and not after it
// ended — including for a ctx captured inside and used afterwards, and for
// the ctx AfterCommit callbacks receive.
func TestInTx_TracksTheCarriedTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	var leaked context.Context
	var inCallback bool
	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if !InTx(ctx) {
			t.Error("InTx = false inside RunTx")
		}
		leaked = ctx
		return AfterCommit(ctx, func(ctx context.Context) { inCallback = InTx(ctx) })
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	if inCallback {
		t.Error("InTx = true in an AfterCommit callback; the transaction has committed")
	}
	if InTx(leaked) {
		t.Error("InTx = true for a ctx leaked past RunTx; its queries run on the pool")
	}

	tx, err := c.BeginDetached(ctx, Serializable.WithMaxAttempts(1))
	if err != nil {
		t.Fatalf("BeginDetached: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if InTx(ctx) {
		t.Error("InTx = true for the ctx BeginDetached was called with; it carries nothing")
	}
	if err := tx.Join(ctx, func(ctx context.Context) error {
		if !InTx(ctx) {
			t.Error("InTx = false inside tx.RunTx")
		}
		return nil
	}); err != nil {
		t.Fatalf("tx.RunTx: %v", err)
	}
}

// openNoteTx is the shape BeginDetached exists for: a helper that opens a
// transaction through the injected orm.Context interface — not the concrete
// *Client — writes in it, and RETURNS with the transaction still open.
func openNoteTx(ctx context.Context, db Context, id string) (*Tx, error) {
	tx, err := db.BeginDetached(ctx, ReadCommitted)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO tx_notes (id, body) VALUES ($1, 'b')", id); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func TestBeginDetached_OutlivesTheCallThatOpenedIt(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	tx, err := openNoteTx(ctx, c, "kept")
	if err != nil {
		t.Fatalf("openNoteTx: %v", err)
	}
	var level string
	if err := tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level); err != nil {
		t.Fatalf("SHOW transaction_isolation: %v", err)
	}
	if level != "read committed" {
		t.Errorf("isolation = %q, want the requested read committed", level)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("outside observer saw %d rows before commit, want 0", got)
	}

	// Code written against the plain client joins it through tx.RunTx.
	if err := tx.Join(ctx, func(ctx context.Context) error { return insertNote(ctx, c, "joined") }); err != nil {
		t.Fatalf("tx.RunTx insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := countNotes(t, c); got != 2 {
		t.Fatalf("after commit: %d rows, want 2", got)
	}

	tx, err = openNoteTx(ctx, c, "discarded")
	if err != nil {
		t.Fatalf("openNoteTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := countNotes(t, c); got != 2 {
		t.Fatalf("after rollback: %d rows, want 2", got)
	}
}

// Detached means detached: opened from inside a caller's RunTx (through the
// *Tx handle or the client) it is a second transaction whose commit survives
// the caller's rollback.
func TestBeginDetached_IsIndependentOfTheCarriedTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	sentinel := errors.New("outer fails")
	err := c.RunTransaction(ctx, ReadCommitted, func(outer Context) error {
		if err := outer.RunTx(ctx, Serializable, func(ctx context.Context) error { return insertNote(ctx, c, "outer") }); err != nil {
			return err
		}
		for _, db := range []Context{outer, c} {
			tx, err := db.BeginDetached(ctx, Serializable.WithMaxAttempts(1))
			if err != nil {
				return err
			}
			id := "detached-tx"
			if db == Context(c) {
				id = "detached-client"
			}
			if _, err := tx.Exec(ctx, "INSERT INTO tx_notes (id, body) VALUES ($1, 'b')", id); err != nil {
				_ = tx.Rollback()
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunTransaction: %v, want the sentinel", err)
	}
	var ids []string
	if err := c.Bun().NewSelect().Model((*txNote)(nil)).Column("id").Order("id").Scan(ctx, &ids); err != nil {
		t.Fatalf("select ids: %v", err)
	}
	if len(ids) != 2 || ids[0] != "detached-client" || ids[1] != "detached-tx" {
		t.Fatalf("rows = %v, want both detached commits and not the rolled-back outer write", ids)
	}
}

// The ctx bounds the transaction: cancelling it rolls the transaction back,
// which is what makes a request-scoped lock release itself.
func TestBeginDetached_CtxCancelRollsBack(t *testing.T) {
	c := newTxTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())

	tx, err := openNoteTx(ctx, c, "cancelled")
	if err != nil {
		t.Fatalf("openNoteTx: %v", err)
	}
	cancel()
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit after ctx cancel succeeded; the transaction should have been rolled back")
	}
	// database/sql rolls back asynchronously; the row must never appear.
	time.Sleep(50 * time.Millisecond)
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("after cancel: %d rows, want 0", got)
	}
}

func TestBeginDetached_ReadOnlySerializableIsDeferrable(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	tx, err := c.BeginDetached(ctx, Serializable.WithReadOnly().WithMaxAttempts(1))
	if err != nil {
		t.Fatalf("BeginDetached: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var deferrable, readOnly string
	if err := tx.QueryRow(ctx, "SHOW transaction_deferrable").Scan(&deferrable); err != nil {
		t.Fatalf("SHOW transaction_deferrable: %v", err)
	}
	if err := tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		t.Fatalf("SHOW transaction_read_only: %v", err)
	}
	if deferrable != "on" || readOnly != "on" {
		t.Fatalf("deferrable=%s read_only=%s, want on/on", deferrable, readOnly)
	}
}

func TestBeginDetached_TxWithoutClientErrors(t *testing.T) {
	if _, err := (&Tx{}).BeginDetached(context.Background(), Serializable.WithMaxAttempts(1)); err == nil {
		t.Fatal("BeginDetached on a Tx with no Client succeeded")
	}
}
