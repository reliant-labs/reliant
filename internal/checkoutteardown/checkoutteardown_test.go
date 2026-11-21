// Copyright (c) 2026 Reliant Labs

package checkoutteardown

import (
	"context"
	"errors"
	"testing"

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

// A machine that cannot answer leaves the op pending, and one too old to
// know ops says so (ErrMachineOutdated): nothing is released on a guess.
func TestQueryOnAMachineThatCannotSay(t *testing.T) {
	t.Parallel()
	outdated := errors.New(`this machine is running an older version of Reliant (v1) that doesn't support "git.teardown_status"`)
	for name, tc := range map[string]struct {
		err          error
		wantOutdated bool
	}{
		"offline":  {err: errors.New("no daemon connected")},
		"outdated": {err: outdated, wantOutdated: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			op := &db.CheckoutTeardown{ID: "op-1", Kind: db.CheckoutTeardownRemove, State: db.CheckoutTeardownPending}
			store := &MockStore{SettleCheckoutTeardownFunc: func(context.Context, string, string, string) (*db.CheckoutTeardown, error) {
				t.Fatal("settled an op the machine could not say anything about")
				return nil, nil
			}}
			m := &MockMachine{SendFunc: func(context.Context, string, any, any, int32) error { return tc.err }}
			got, err := Query(context.Background(), store, m, op)
			assert.Equal(t, db.CheckoutTeardownPending, got.State)
			require.Error(t, err)
			assert.Equal(t, tc.wantOutdated, errors.Is(err, ErrMachineOutdated), "err = %v", err)
		})
	}
}

// An op sent to a machine before it knew op ids (the old protocol, which
// journaled nothing) is settled as soon as the machine is updated: the
// updated daemon has never seen the id, fences it, and the op is not run.
func TestQuerySettlesAnOldOpOnceTheMachineIsUpdated(t *testing.T) {
	t.Parallel()
	op := &db.CheckoutTeardown{ID: "op-1", Kind: db.CheckoutTeardownRemove, State: db.CheckoutTeardownPending}
	store := &MockStore{SettleCheckoutTeardownFunc: func(_ context.Context, id, state, detail string) (*db.CheckoutTeardown, error) {
		out := *op
		out.State, out.Detail = state, detail
		return &out, nil
	}}
	m := &MockMachine{SendFunc: func(_ context.Context, command string, payload, resp any, _ int32) error {
		assert.Equal(t, StatusCommand, command)
		assert.Equal(t, map[string]string{"op_id": "op-1"}, payload)
		*resp.(*Ack) = Ack{OpID: "op-1", OpState: "fenced"}
		return nil
	}}
	got, err := Query(context.Background(), store, m, op)
	require.NoError(t, err)
	assert.Equal(t, db.CheckoutTeardownNotRun, got.State)
}

// A settled op is not asked about again.
func TestQuerySkipsASettledOp(t *testing.T) {
	t.Parallel()
	op := &db.CheckoutTeardown{ID: "op-1", State: db.CheckoutTeardownDone}
	m := &MockMachine{SendFunc: func(context.Context, string, any, any, int32) error {
		t.Fatal("asked the machine about a settled op")
		return nil
	}}
	got, err := Query(context.Background(), &MockStore{}, m, op)
	require.NoError(t, err)
	assert.Equal(t, db.CheckoutTeardownDone, got.State)
}
