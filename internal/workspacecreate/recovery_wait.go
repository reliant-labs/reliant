// Copyright (c) 2026 Reliant Labs
package workspacecreate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// A daemon refuses new work for a moment while it finalizes a worktree's
// recovery (a remove or reclone; toolexec.ErrRecoveryFinalizing). The
// commands creation sends — worktree.create, worktree.copy_paths and the
// rollback's deletes — name no path the daemon can scope, so it refuses them
// while ANY worktree on the machine is finalizing, not only this one. Nothing
// ran, so the command is sent again once the finalization is over. Prod
// 2026-11-21 (obs bug reports b793ea9b, cf91a1ef): an agent's worktree tool
// failed outright, and left a FAILED row, because another workspace on the
// machine was being removed at that moment.
//
// A finalization holds its lease for at most two minutes (the daemon's
// recovery admission), and usually far less, so a refusal that outlasts
// recoveryWaitBudget is not one that clears by waiting.

// retryableRefusal is how the router's error for a daemon's refusal describes
// itself (toolexec's daemonReportedFailure, the same interface errclass
// reads). Declared here because toolexec imports this package.
type retryableRefusal interface{ RetryableRefusal() bool }

// isRecoveryRefusal reports whether err is a daemon refusing the command for
// a moment, before running any of it.
func isRecoveryRefusal(err error) bool {
	var r retryableRefusal
	return errors.As(err, &r) && r.RetryableRefusal()
}

// Tests replace the clock and sleep so they wait no real time.
var (
	recoveryWaitNow = time.Now
	// recoveryWaitFirst is the first wait, doubling up to recoveryWaitMax.
	recoveryWaitFirst = 2 * time.Second
	recoveryWaitMax   = 5 * time.Second
	// recoveryWaitBudget is the most a command waits in total: a little over
	// the longest a finalization holds its lease.
	recoveryWaitBudget = 150 * time.Second
	// recoveryWaitSleep waits d, or until ctx ends (its error).
	recoveryWaitSleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

// recoveryOutlastedError is a command the daemon was still refusing when
// recoveryWaitBudget ran out, so machineFailure can say so. It wraps the last
// refusal.
type recoveryOutlastedError struct {
	waited time.Duration
	cause  error
}

func (e *recoveryOutlastedError) Error() string {
	return fmt.Sprintf("refused for %s while the machine finalized a workspace's recovery: %v", e.waited, e.cause)
}

func (e *recoveryOutlastedError) Unwrap() error { return e.cause }

// recoveryPatientMachine sends each command again while the daemon refuses it
// for a recovery finalization, waiting between sends, for at most
// recoveryWaitBudget in total.
type recoveryPatientMachine struct{ Machine }

func (m recoveryPatientMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	started := recoveryWaitNow()
	// Sends share the budget too, including an in-flight retry. Do not restart
	// a full command timeout after every refusal.
	budgetCtx, cancel := context.WithTimeout(ctx, recoveryWaitBudget)
	defer cancel()
	wait := recoveryWaitFirst
	var refusal error
	for {
		err := m.Machine.Send(budgetCtx, commandType, payload, resp, timeoutMs)
		elapsed := recoveryWaitNow().Sub(started)
		if isRecoveryRefusal(err) {
			if refusal == nil {
				logging.InfoContext(ctx, "Machine is finalizing a workspace's recovery; waiting to send the command again",
					"command", commandType, "error", err)
			}
			refusal = err
		} else {
			if err == nil {
				if refusal != nil {
					logging.InfoContext(ctx, "Daemon command ran after the machine finished a recovery finalization",
						"command", commandType, "waited", elapsed)
				}
				return nil
			}
			if refusal == nil || ctx.Err() != nil || budgetCtx.Err() == nil {
				return err
			}
		}
		remaining := recoveryWaitBudget - elapsed
		if remaining <= 0 || (ctx.Err() == nil && budgetCtx.Err() != nil) {
			return &recoveryOutlastedError{waited: elapsed, cause: refusal}
		}
		if serr := recoveryWaitSleep(budgetCtx, min(wait, remaining)); serr != nil {
			if ctx.Err() == nil && budgetCtx.Err() != nil {
				return &recoveryOutlastedError{waited: recoveryWaitNow().Sub(started), cause: refusal}
			}
			// Cancelled while waiting: the refusal is still what happened.
			return refusal
		}
		if elapsed = recoveryWaitNow().Sub(started); elapsed >= recoveryWaitBudget {
			return &recoveryOutlastedError{waited: elapsed, cause: refusal}
		}
		wait = min(2*wait, recoveryWaitMax)
	}
}
