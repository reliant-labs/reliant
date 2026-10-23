package orm

import (
	"context"
	"testing"
)

// TestContextInterfaceExposesRunTransaction pins the seam that matters:
// RunTransaction must be reachable through the orm.Context INTERFACE, not
// merely on the concrete *Client.
//
// forge's CRUD generator writes `DB orm.Context` into a service's Deps,
// and forgeconv-deps-are-interfaces requires every Deps field to be an
// interface — so orm.Context is the only handle app code gets.
// When RunTransaction lived on *Client alone, an app that had to make two
// writes atomic had to re-declare this method as a local interface and
// type-assert the injected DB to it, copying forge's own seam per project.
//
// A compile-time assertion is the whole test: if the method leaves the
// interface, this file stops building.
func TestContextInterfaceExposesRunTransaction(t *testing.T) {
	var runViaInterface func(Context) error = func(db Context) error {
		return db.RunTransaction(context.Background(), ReadCommitted, func(tx Context) error {
			if tx == nil {
				t.Fatal("RunTransaction handed fn a nil Context")
			}
			return nil
		})
	}
	_ = runViaInterface

	// Both shipped implementations must satisfy Context, including *Tx —
	// which joins rather than nesting, so an interactor wrapping its work
	// in RunTransaction composes inside a transaction someone else opened.
	var _ Context = (*Client)(nil)
	var _ Context = (*Tx)(nil)
}

// TestTxRunTransactionJoins asserts *Tx.RunTransaction runs fn with the
// SAME transaction rather than opening a nested one, and propagates fn's
// error instead of swallowing it.
func TestTxRunTransactionJoins(t *testing.T) {
	tx := &Tx{}
	var got Context
	if err := tx.RunTransaction(context.Background(), ReadCommitted, func(inner Context) error {
		got = inner
		return nil
	}); err != nil {
		t.Fatalf("join returned an error: %v", err)
	}
	if got != Context(tx) {
		t.Errorf("fn must receive the SAME *Tx (a join), got %#v", got)
	}

	sentinel := context.Canceled
	if err := tx.RunTransaction(context.Background(), ReadCommitted, func(Context) error {
		return sentinel
	}); err != sentinel {
		t.Errorf("fn's error must propagate to the transaction owner; got %v", err)
	}
}
