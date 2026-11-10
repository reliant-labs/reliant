package db

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// While a remove or reclone of a checkout is unsettled, no branch workspace of
// it is created there; once the machine has said how it ended, creation
// proceeds. Another machine's checkout is never held.
func TestCheckoutTeardown_HoldsBackWorkspacesUntilSettled(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()

	op, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownReclone, "/clone", nil)
	require.NoError(t, err)
	assert.Equal(t, CheckoutTeardownPending, op.State)

	err = f.repo.CreateWorktree(ctx, f.workspace("held", f.here))
	var pending *CheckoutTeardownPendingError
	require.ErrorAs(t, err, &pending)
	assert.Equal(t, op.ID, pending.Teardown.ID)
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("other-machine", f.elsewhere)))

	settled, err := f.repo.SettleCheckoutTeardown(ctx, op.ID, CheckoutTeardownDone, "")
	require.NoError(t, err)
	assert.Equal(t, CheckoutTeardownDone, settled.State)
	require.NotNil(t, settled.SettledAt)
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("after", f.here)))
}

// A retry of a teardown whose outcome is unknown reuses its op, so the
// machine gets the same id and runs it at most once; a different teardown of
// the same checkout waits for the first.
func TestBeginCheckoutTeardown_ARetryReusesThePendingOp(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()

	first, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownRemove, "/clone", nil)
	require.NoError(t, err)
	guardRan := false
	retry, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownRemove, "/clone", func(context.Context) error {
		guardRan = true
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, retry.ID)
	assert.False(t, guardRan, "the barrier has held since the first began; nothing new to guard")

	_, err = f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownReclone, "/clone", nil)
	var busy *CheckoutTeardownBusyError
	require.ErrorAs(t, err, &busy)
	assert.Equal(t, first.ID, busy.Teardown.ID)

	// Settled, the next teardown is a new op.
	_, err = f.repo.SettleCheckoutTeardown(ctx, first.ID, CheckoutTeardownFailed, "permission denied")
	require.NoError(t, err)
	next, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownReclone, "/clone", nil)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, next.ID)
}

// The guard runs under the exclusive checkout lock, and a refusal records
// nothing.
func TestBeginCheckoutTeardown_AGuardRefusalRecordsNothing(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()
	refused := errors.New("workspaces live there")
	_, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownRemove, "/clone", func(context.Context) error { return refused })
	require.ErrorIs(t, err, refused)
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("free", f.here)), "no op was recorded, so nothing is held")
}

// A remove that is done drops the machine's checkout row in the same step, so
// whichever answer settles it — the reply, a retry's, or a status query —
// leaves the records consistent. A second answer changes nothing.
func TestSettleCheckoutTeardown_ADoneRemoveDropsTheCheckoutRowOnce(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()

	op, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownRemove, "/clone", nil)
	require.NoError(t, err)
	_, err = f.repo.SettleCheckoutTeardown(ctx, op.ID, CheckoutTeardownDone, "")
	require.NoError(t, err)

	rows, err := f.repo.ListProjectDaemonsForProject(ctx, f.pid)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, f.elsewhere, rows[0].DaemonID)

	again, err := f.repo.SettleCheckoutTeardown(ctx, op.ID, CheckoutTeardownFailed, "late answer")
	require.NoError(t, err)
	assert.Equal(t, CheckoutTeardownDone, again.State, "a settled op keeps its first outcome")
}

// A remove that failed or never ran keeps the row: the checkout is still on
// that disk and the row is its only record.
func TestSettleCheckoutTeardown_AnUnfinishedRemoveKeepsTheRow(t *testing.T) {
	t.Parallel()
	for _, state := range []string{CheckoutTeardownFailed, CheckoutTeardownNotRun, CheckoutTeardownExpired} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newCheckoutFixture(t)
			ctx := context.Background()
			op, err := f.repo.BeginCheckoutTeardown(ctx, f.pid, f.here, CheckoutTeardownRemove, "/clone", nil)
			require.NoError(t, err)
			_, err = f.repo.SettleCheckoutTeardown(ctx, op.ID, state, "")
			require.NoError(t, err)
			rows, err := f.repo.ListProjectDaemonsForProject(ctx, f.pid)
			require.NoError(t, err)
			assert.Len(t, rows, 2)
		})
	}
}
