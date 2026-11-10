// Copyright (c) 2026 Reliant Labs

package checkoutteardown

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestStateMapsTheMachinesAnswer(t *testing.T) {
	t.Parallel()
	for opState, want := range map[string]string{
		"done":        db.CheckoutTeardownDone,
		"failed":      db.CheckoutTeardownFailed,
		"interrupted": db.CheckoutTeardownFailed,
		"fenced":      db.CheckoutTeardownNotRun,
		"running":     db.CheckoutTeardownPending,
		"":            db.CheckoutTeardownPending,
		"from-later":  db.CheckoutTeardownPending,
	} {
		assert.Equal(t, want, State(opState), "op_state %q", opState)
	}
}

// An acknowledgement of another op settles nothing.
func TestSettleRefusesAnotherOpsAck(t *testing.T) {
	t.Parallel()
	op := &db.CheckoutTeardown{ID: "op-1", State: db.CheckoutTeardownPending}
	store := &MockStore{SettleCheckoutTeardownFunc: func(context.Context, string, string, string) (*db.CheckoutTeardown, error) {
		t.Fatal("settled on another op's acknowledgement")
		return nil, nil
	}}
	_, err := Settle(context.Background(), store, op, Ack{OpID: "op-2", OpState: "done"})
	assert.Error(t, err)
}

// A machine that cannot answer leaves the op pending; one too old to know ops
// holds it for LegacyGrace, then releases it expired.
func TestQueryOnAMachineThatCannotSay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	outdated := errors.New(`this machine is running an older version of Reliant (v1) that doesn't support "git.teardown_status"`)
	for name, tc := range map[string]struct {
		err       error
		age       time.Duration
		wantState string
		wantErr   bool
		legacy    bool
	}{
		"offline":           {err: errors.New("no daemon connected"), wantState: db.CheckoutTeardownPending, wantErr: true},
		"old, in the grace": {err: outdated, age: time.Minute, wantState: db.CheckoutTeardownPending, wantErr: true, legacy: true},
		"old, past it":      {err: outdated, age: LegacyGrace + time.Minute, wantState: db.CheckoutTeardownExpired},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			op := &db.CheckoutTeardown{ID: "op-1", Kind: db.CheckoutTeardownRemove, State: db.CheckoutTeardownPending, CreatedAt: now.Add(-tc.age)}
			store := &MockStore{SettleCheckoutTeardownFunc: func(_ context.Context, id, state, detail string) (*db.CheckoutTeardown, error) {
				out := *op
				out.State, out.Detail = state, detail
				return &out, nil
			}}
			m := &MockMachine{SendFunc: func(context.Context, string, any, any, int32) error { return tc.err }}
			got, err := Query(context.Background(), store, m, op, now)
			assert.Equal(t, tc.wantState, got.State)
			assert.Equal(t, tc.wantErr, err != nil, "err = %v", err)
			var legacy *LegacyPendingError
			assert.Equal(t, tc.legacy, errors.As(err, &legacy))
		})
	}
}

// A settled op is not asked about again.
func TestQuerySkipsASettledOp(t *testing.T) {
	t.Parallel()
	op := &db.CheckoutTeardown{ID: "op-1", State: db.CheckoutTeardownDone}
	m := &MockMachine{SendFunc: func(context.Context, string, any, any, int32) error {
		t.Fatal("asked the machine about a settled op")
		return nil
	}}
	got, err := Query(context.Background(), &MockStore{}, m, op, time.Now())
	require.NoError(t, err)
	assert.Equal(t, db.CheckoutTeardownDone, got.State)
}
