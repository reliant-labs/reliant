// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// Pushed is reliant/pr.pushed@1: it finishes this run's fix attempt (the
// one pr.next or pr.verdict started, at the signature it was answering);
// the run then decides the PR again on its new head. `with: {from:}` may
// name the git_publish node, whose output says what was pushed; a push it
// refused finishes the attempt as failed.
func (s *Service) Pushed(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	p, err := s.loadActive(ctx, req)
	if err != nil {
		return nil, err
	}
	inv := req.Invocation
	key := inv.ExecutionKey
	pushedHead, outcome := "", core.PipelineAttemptSucceeded
	if from := req.From; from != nil && from.Ran {
		pushedHead, _ = from.Output["sha"].(string)
		if refused, _ := from.Output["refused"].(bool); refused {
			outcome = core.PipelineAttemptFailed
		}
	}

	var attempt *core.PipelineAttempt
	err = s.deps.Store.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		fixes, err := s.attempts(ctx, p.rec.ID, core.PipelineAttemptFix, attemptFix)
		if err != nil {
			return err
		}
		for _, a := range fixes {
			if key != "" && detailOf(a).PushedKey == key {
				attempt = a // a retry of a push already recorded
				return nil
			}
		}
		fix := running(fixes, inv.RunID, "")
		if fix == nil {
			return serveraction.Errorf(serveraction.CodeFailedPrecondition, "no fix of %s is in flight for this run", prName(p))
		}
		detail := detailOf(fix)
		detail.PushedKey, detail.PushedHead = key, pushedHead
		detail.Sessions = append(detail.Sessions, inv.ThreadID)
		if attempt, err = s.deps.Store.FinishPipelineAttempt(ctx, fix.ID, outcome, detail.raw()); err != nil {
			return err
		}
		to := *p.row
		to.OwnerRunID = inv.RunID
		// The run goes on to decide itself; the due time is the watchdog
		// that wakes the PR if it dies first.
		to.NextAction, to.NextActionAt = NextDecide, timePtr(p.now.Add(ownerLease))
		if fix.Signature != "" && to.FixSignature == "" {
			to.FixSignature, to.FixAttempts = fix.Signature, fix.Attempt
		}
		return s.transition(ctx, p, to)
	})
	if err != nil {
		var public *serveraction.Error
		if errors.As(err, &public) {
			return nil, public
		}
		return nil, casError(err)
	}
	return &serveraction.Result{
		Data: map[string]any{
			"next": NextDecide, "attempt": attempt.Attempt, "signature": attempt.Signature,
			"outcome": string(attempt.Outcome), "pushed_head": detailOf(attempt).PushedHead,
		},
		Content: "decide: fix attempt recorded",
	}, nil
}
