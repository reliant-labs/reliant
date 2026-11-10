package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/store"
)

// CheckoutTeardown is one remove or reclone of a project's checkout on a
// machine, by the operation id the machine acknowledges (reliant#2429). While
// one is pending, no branch workspace of that checkout is created.
type CheckoutTeardown = store.CheckoutTeardown

// Teardown kinds.
const (
	CheckoutTeardownRemove  = "remove"
	CheckoutTeardownReclone = "reclone"
)

// Teardown states.
const (
	CheckoutTeardownPending = store.CheckoutTeardownPending
	CheckoutTeardownDone    = store.CheckoutTeardownDone
	CheckoutTeardownFailed  = store.CheckoutTeardownFailed
	CheckoutTeardownNotRun  = store.CheckoutTeardownNotRun
	CheckoutTeardownExpired = store.CheckoutTeardownExpired
)

// CheckoutTeardownPendingError is CreateWorktree refusing a branch workspace
// of a checkout whose teardown has not settled: the machine may still be
// deleting the repository the workspace would be a worktree of.
type CheckoutTeardownPendingError struct {
	Teardown *CheckoutTeardown
}

func (e *CheckoutTeardownPendingError) Error() string {
	return fmt.Sprintf("a %s of the project's checkout on this machine (operation %s) has not been confirmed finished",
		e.Teardown.Kind, e.Teardown.ID)
}

// CheckoutTeardownBusyError is BeginCheckoutTeardown finding a different
// teardown of the checkout still pending: a remove while a reclone is
// unsettled, or one of another path.
type CheckoutTeardownBusyError struct {
	Teardown *CheckoutTeardown
}

func (e *CheckoutTeardownBusyError) Error() string {
	return fmt.Sprintf("a %s of the project's checkout on this machine (operation %s) has not finished",
		e.Teardown.Kind, e.Teardown.ID)
}

// BeginCheckoutTeardown records a teardown of the project's checkout on the
// machine, before the command is sent, and returns it. A pending teardown of
// the same kind and path is a retry: it is returned as is, so the retry sends
// the same operation id and the machine runs it at most once. A pending one of
// another kind or path is a CheckoutTeardownBusyError. A returned retry has
// Reused set: whether the first delivery ran, and on which kind of daemon, is
// unknown, so the caller asks the machine (checkoutteardown.Query) before
// sending anything.
//
// guard, when not nil, runs inside the transaction, under the exclusive side
// of the checkout lock a workspace insert takes shared (CreateWorktree), so its
// view of the workspaces (requireNoWorkspacesOn) holds until the row commits;
// from then on the row itself refuses new workspaces.
func (r *Repo) BeginCheckoutTeardown(ctx context.Context, projectID, daemonID, kind, path string, guard func(ctx context.Context) error) (*CheckoutTeardown, error) {
	if projectID == "" || daemonID == "" || path == "" {
		return nil, fmt.Errorf("project ID, daemon ID and path cannot be empty")
	}
	if kind != CheckoutTeardownRemove && kind != CheckoutTeardownReclone {
		return nil, fmt.Errorf("unknown checkout teardown kind %q", kind)
	}
	var op *CheckoutTeardown
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		op = nil
		if err := store.LockProjectCheckout(txCtx, r.ORM(), projectID, daemonID, false); err != nil {
			return err
		}
		pending, err := store.PendingCheckoutTeardown(txCtx, r.ORM(), projectID, daemonID, true)
		if err != nil {
			return err
		}
		if pending != nil {
			if pending.Kind != kind || pending.Path != path {
				return &CheckoutTeardownBusyError{Teardown: pending}
			}
			pending.Reused = true
			op = pending
			return nil
		}
		if guard != nil {
			if err := guard(txCtx); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		row := &CheckoutTeardown{
			ID: uuid.NewString(), ProjectID: projectID, DaemonID: daemonID,
			Kind: kind, Path: path, State: CheckoutTeardownPending,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := store.CreateCheckoutTeardown(txCtx, r.ORM(), row); err != nil {
			return fmt.Errorf("failed to record the checkout teardown: %w", err)
		}
		op = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return op, nil
}

// SettleCheckoutTeardown records the outcome of the pending teardown id and
// returns the row as it then is. A remove that is done drops the machine's
// project_daemons row (the checkout is gone) in the same transaction, unless
// the row has since moved to another path. Settling a settled teardown
// changes nothing, so the answers of a retry and a status query can both
// arrive.
func (r *Repo) SettleCheckoutTeardown(ctx context.Context, id, state, detail string) (*CheckoutTeardown, error) {
	switch state {
	case CheckoutTeardownDone, CheckoutTeardownFailed, CheckoutTeardownNotRun, CheckoutTeardownExpired:
	default:
		return nil, fmt.Errorf("cannot settle a checkout teardown as %q", state)
	}
	var out *CheckoutTeardown
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		row, err := store.LockCheckoutTeardown(txCtx, r.ORM(), id)
		if err != nil {
			return err
		}
		if row == nil {
			return svcerr.NotFound("checkout teardown")
		}
		out = row
		if row.State != CheckoutTeardownPending {
			return nil
		}
		now := time.Now().UTC()
		if err := store.SettleCheckoutTeardownRow(txCtx, r.ORM(), id, state, detail, now); err != nil {
			return err
		}
		row.State, row.Detail, row.UpdatedAt, row.SettledAt = state, detail, now, &now
		if state != CheckoutTeardownDone || row.Kind != CheckoutTeardownRemove {
			return nil
		}
		pd, err := store.GetProjectDaemonByPK(txCtx, r.ORM(), row.ProjectID, row.DaemonID)
		if errors.Is(err, orm.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if pd.Path != row.Path {
			return nil
		}
		return store.RemoveProjectDaemon(txCtx, r.ORM(), row.ProjectID, row.DaemonID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
