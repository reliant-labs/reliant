// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ErrExecutionNotCurrent is TerminateExecution's refusal: the chat's run is no
// longer the execution the caller asked to stop (it closed, or a later chain,
// such as a person's own turn, replaced it), so nothing was stopped.
var ErrExecutionNotCurrent = svcerr.FailedPrecondition("the chat's run is no longer the execution asked to stop")

// stopTarget is the run a stop is for: the chat's current execution of
// workflowID when runID is "" (Terminate), or exactly the execution runID
// (TerminateExecution).
type stopTarget struct {
	chatID, workflowID, runID string
}

// TerminateExecution is Terminate for one exact execution of the chat's run:
// the one Temporal reported as workflowID's current run when the caller
// decided to stop it. It stops that run as Terminate does (a cooperative
// cancel, so its finally: runs, and a hard terminate only for a run that does
// not acknowledge in time), and never any other.
//
// Terminate resolves whatever execution is current when it runs. That is
// right for a person pressing stop, and wrong for an automation that decided
// to stop a run it saw a moment ago: by now the chat's workflow id may carry a
// later chain, a person's turn, which must not be cancelled for it. So here:
//
//   - The decision is revalidated under the chat's run-control lock, the lock
//     every path that starts or routes a run for the chat holds: runID must
//     still be the chat's current, running execution. Otherwise nothing is
//     done and ErrExecutionNotCurrent is returned.
//   - Every Temporal call is addressed to runID, so Temporal itself refuses
//     the cancel, the terminate, the query, once that execution has closed.
//     A refused cancel and terminate return an error with nothing written.
//   - Only once Temporal has taken the cancel (or terminate) are the run's
//     checkpoint dropped and its questions voided, still under the lock.
//   - The writes after it closes (settling its row, the usage-limit wait)
//     re-take the lock and are made only while runID is still the chat's
//     latest execution: a chain started after it closed owns the row then.
//
// The lock is held only for those short sections, never across the cancel's
// waits, so a person's send waits at most a few round trips behind it.
func (s *Service) TerminateExecution(ctx context.Context, chatID, workflowID, runID string) error {
	if chatID == "" || workflowID == "" || runID == "" {
		return errors.New("terminate execution: a chat, workflow id and run id are required")
	}
	t := stopTarget{chatID: chatID, workflowID: workflowID, runID: runID}

	release := s.LockRunControl(ctx, chatID)
	if err := s.requireCurrent(ctx, t); err != nil {
		release()
		return err
	}
	canceller, asked := s.requestCancel(ctx, t)
	terminator, canTerminate := s.temporal.(TemporalTerminator)
	if !asked {
		if !canTerminate {
			release()
			return fmt.Errorf("terminate execution %s/%s: temporal client cannot terminate workflows", workflowID, runID)
		}
		if err := terminator.TerminateWorkflow(ctx, workflowID, runID, "Workflow terminated by operator"); err != nil {
			release()
			return fmt.Errorf("terminate execution %s/%s: %w", workflowID, runID, err)
		}
	}
	// Temporal has the stop for this execution: it is ending, so the next
	// message must start fresh and nothing may stay parked on its questions.
	if err := s.repo.DeleteWorkflowCheckpoint(ctx, workflowID); err != nil {
		logging.WarnContext(ctx, "[runs] Failed to clear workflow checkpoint while terminating",
			"workflowID", workflowID, "error", err)
	}
	s.voidPendingQuestions(ctx, chatID)
	release()

	if asked && s.awaitCancel(ctx, canceller, t) {
		s.whileCurrent(ctx, t, func() { s.clearUsageLimitWait(ctx, chatID) })
		return nil
	}
	if asked {
		if !canTerminate {
			return fmt.Errorf("terminate execution %s/%s: temporal client cannot terminate workflows", workflowID, runID)
		}
		if err := terminator.TerminateWorkflow(ctx, workflowID, runID, "Workflow terminated by operator"); err != nil {
			logging.WarnContext(ctx, "[runs] Failed to terminate workflow in Temporal; continuing to reconcile DB",
				"workflowID", workflowID, "runID", runID, "error", err)
		}
	}
	var markErr error
	s.whileCurrent(ctx, t, func() {
		markErr = s.markTerminated(ctx, workflowID)
		s.clearUsageLimitWait(ctx, chatID)
	})
	return markErr
}

// requireCurrent reports whether t's execution is still the chat's run: the
// chat's workflow id is t's, and Temporal shows runID as its current, running
// execution. Anything else, or a Temporal it cannot read, is a refusal.
func (s *Service) requireCurrent(ctx context.Context, t stopTarget) error {
	_, workflowID, err := s.chatRun(ctx, t.chatID)
	if err != nil {
		return err
	}
	if workflowID != t.workflowID {
		return ErrExecutionNotCurrent
	}
	current, err := s.state(ctx, t.workflowID, "")
	if err != nil {
		return fmt.Errorf("terminate execution %s/%s: %w", t.workflowID, t.runID, err)
	}
	if !current.Exists || current.RunID != t.runID || !current.IsRunning {
		return ErrExecutionNotCurrent
	}
	return nil
}

// whileCurrent runs write for t's run. For the chat's current run (Terminate)
// it just runs it. For an exact execution it runs it under the run-control
// lock, and only while Temporal still shows runID as workflowID's latest
// execution: the workflow's row and the chat's markers are shared by every
// chain on the workflow id, and once a later chain has started they are its.
func (s *Service) whileCurrent(ctx context.Context, t stopTarget, write func()) {
	if t.runID == "" {
		write()
		return
	}
	release := s.LockRunControl(ctx, t.chatID)
	defer release()
	latest, err := s.state(ctx, t.workflowID, "")
	if err != nil || (latest.Exists && latest.RunID != t.runID) {
		logging.InfoContext(ctx, "[runs] A later run owns the chat's row now; leaving it to that run",
			"workflowID", t.workflowID, "runID", t.runID, "latestRunID", latest.RunID, "error", err)
		return
	}
	write()
}
