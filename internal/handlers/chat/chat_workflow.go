// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/serviceerror"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// rootRunIsLive reports whether the chat's root run is executing or paused, so
// a param update has a run to signal. A pending, completed or failed run has
// none: the update is stored and the next run starts from it.
func (s *Service) rootRunIsLive(ctx context.Context, workflowID string) bool {
	wf, err := s.deps.Repo.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return false
	}
	return wf.Status == db.Active() || wf.Status == db.Paused()
}

// signalParamUpdate delivers an input update to the chat's CURRENT run (empty
// run id): the run id on the chat row names whichever run was current when it
// was written, and a continue-as-new closes that one. Thread-scoped updates
// ride in the payload under __thread.
func (s *Service) signalParamUpdate(ctx context.Context, workflowID string, update map[string]interface{}) error {
	err := s.deps.Temporal.SignalWorkflow(ctx, workflowID, "", "update_workflow_state", update)
	if err == nil {
		return nil
	}
	if stale := staleWorkflowSignalError(err); stale != nil {
		return stale
	}
	return svcerr.WrapOr(fmt.Errorf("signal workflow %s: %w", workflowID, err), svcerr.Internal("failed to update workflow params"))
}

// threadAcceptsParamUpdate reports whether thread-scoped params can be applied
// to threadID: nil when its run is going, or when it is a sub-agent that
// stopped on an error — its params are the ones it resumes with, and
// switching its model is how a user gets it past a provider's usage limit.
// A thread that finished has no next run for them to apply to.
func (s *Service) threadAcceptsParamUpdate(ctx context.Context, chat *db.Chat, threadID string) error {
	wf, err := s.deps.Repo.GetWorkflowByThread(ctx, chat.ID, threadID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get the thread's workflow"))
	}
	if wf == nil {
		return svcerr.WithMessage(svcerr.ErrNotFound, "no workflow found for thread")
	}
	if wf.Status == db.Active() {
		return nil
	}
	if s.canResumeStoppedAgent(ctx, chat, threadID) {
		return nil
	}
	return svcerr.FailedPrecondition("thread workflow is not running")
}

// staleWorkflowSignalError is the error for a param-update signal that found
// no running workflow, and nil for any other signal failure.
//
// The run finished between the handler's status check and the signal
// ("workflow execution already completed"), so there is no run for the params
// to apply to. The caller's view is stale: a FailedPrecondition — a user error
// — not a server failure. As an Internal it was 11 of the 12 ERRORs this RPC
// logged in five days of prod.
func staleWorkflowSignalError(err error) error {
	var notFound *serviceerror.NotFound
	if !errors.As(err, &notFound) {
		return nil
	}
	return svcerr.FailedPrecondition("the chat's workflow is no longer running")
}

// workflowTreeBuilder holds everything buildWorkflowExecutionTree needs, all
// loaded once per request so the walk itself issues no queries.
type workflowTreeBuilder struct {
	threadsByID      map[string]*db.Thread
	forkedThreads    map[string]bool
	childrenByParent map[string][]*db.Workflow
	stepsByWorkflow  map[string][]*db.ChatStepExecution
	// missingThreads is every workflow thread build found no row for.
	missingThreads []string
}

// reportMissingThreads logs, once for the whole tree, the workflow threads
// that had no row in the chat's thread list. A failure here is NOT cosmetic:
// Origin is what tells the UI a thread is a spawned sub-agent, and a spawn
// whose origin is missing renders its entire transcript inline in the parent
// chat. Swallowing it left that looking like a frontend bug for a long time,
// so it stays an ERROR — unless the thread list itself failed to load, which
// was already reported and explains every miss.
func (b *workflowTreeBuilder) reportMissingThreads(ctx context.Context, chatID string, threadsErr error) {
	if len(b.missingThreads) == 0 || threadsErr != nil {
		return
	}
	sample := b.missingThreads
	if len(sample) > 5 {
		sample = sample[:5]
	}
	logging.ErrorContext(ctx, "[WorkflowTree] Failed to load thread for workflow; Origin will be empty and spawned threads will render inline",
		"error", fmt.Errorf("%d workflow thread(s) not found in chat, e.g. %q", len(b.missingThreads), sample),
		"missing", len(b.missingThreads),
		"threads", sample,
		"chatID", chatID)
}

// build recursively builds the workflow execution tree
func (b *workflowTreeBuilder) build(wf *db.Workflow) *reliantv1.WorkflowExecution {
	proto := &reliantv1.WorkflowExecution{
		Id:           wf.ID,
		WorkflowName: wf.WorkflowName,
		Thread:       wf.Thread,
		State:        wf.Status.State,
		StopReason:   wf.Status.StopReason,
		CreatedAt:    wf.CreatedAt.Format(time.RFC3339),
		MessageCount: 0, // TODO: count messages by thread
	}

	if wf.ParentID != nil {
		proto.ParentId = wf.ParentID
	}
	if wf.SpawnedByNodeID != nil {
		proto.SpawnedByNodeId = wf.SpawnedByNodeID
	}
	// The run's verdict, beside its lifecycle status: a run that ran to its
	// `failed` terminal node is Status=COMPLETED, Outcome=failure, and a
	// supervisor must be able to tell that from a run that built the app.
	if wf.Outcome != nil && *wf.Outcome != "" {
		proto.Outcome = wf.Outcome
	}
	// Populate Origin, ForkedFromThread, ParentThread, and ThreadTitle from the
	// Thread table (single source of truth for thread identity). A missing
	// thread is reported by reportMissingThreads.
	thread := b.threadsByID[wf.Thread]
	if thread == nil {
		// Collected and reported once per tree (see reportMissingThreads),
		// not once per workflow: one chat whose thread list did not load was
		// 568 ERROR events, five per request (prod 48884f4d).
		b.missingThreads = append(b.missingThreads, wf.Thread)
	}
	if thread != nil {
		if thread.ParentThreadID != nil {
			// ForkedFromThread: only for actual forks, not plain child threads
			// (e.g. spawn). ForkThread always links the thread's initial (sequence
			// 0) context window to the parent's via ParentContextWindowID, even
			// when ForkAtMessageID is nil (forking an empty parent thread) --
			// createThreadInternal never sets that link. That link, not
			// ForkAtMessageID, is what "is this a fork" needs to test.
			if b.forkedThreads[wf.Thread] {
				proto.ForkedFromThread = thread.ParentThreadID
			}
			// ParentThread: always set when parent exists (both fork and new)
			proto.ParentThread = thread.ParentThreadID
		}
		if thread.Title != nil {
			proto.ThreadTitle = thread.Title
		}
		proto.Origin = thread.Origin
		proto.OriginNodeId = thread.OriginNodeID
	}
	if wf.LoopIteration != nil {
		iteration := int32(*wf.LoopIteration)
		proto.Iteration = &iteration
	}
	if wf.CompletedAt != nil {
		completedAt := wf.CompletedAt.Format(time.RFC3339)
		proto.CompletedAt = &completedAt
	}

	// Add step executions.
	//
	// output_json is never sent on this path. The only thing a client read out
	// of it here was the saved message id of a "-save" step, which now arrives
	// as its own scalar field — so the response carries one UUID per save step
	// instead of that step's entire output (37 MB for the worst real chat).
	//
	// output_json still arrives for user-facing activities, which ActivityIndicator
	// and the workflow viewer's Output panel render. The query withholds it only
	// for internal plumbing (model.InternalActivities) — and that is where all
	// the weight is: 32,013 of that chat's 32,023 steps, against 23 kB for the
	// ten that remain.
	if steps, ok := b.stepsByWorkflow[wf.ID]; ok {
		proto.Steps = make([]*reliantv1.StepExecution, len(steps))
		for i, step := range steps {
			proto.Steps[i] = &reliantv1.StepExecution{
				Id:           step.ID,
				WorkflowId:   step.WorkflowID,
				StepId:       step.StepID,
				ActivityName: step.ActivityName,
				CreatedAt:    step.CreatedAt.Format(time.RFC3339),
			}
			proto.Steps[i].SavedMessageId = step.SavedMessageID
			proto.Steps[i].OutputJson = step.OutputJSON
			if step.ExitCode != nil {
				exitCode := int32(*step.ExitCode)
				proto.Steps[i].ExitCode = &exitCode
			}
			proto.Steps[i].Success = step.Success
			proto.Steps[i].DurationMs = step.DurationMs
			proto.Steps[i].LoopNodeId = step.LoopNodeID
			if step.LoopIteration != nil {
				loopIter := int32(*step.LoopIteration)
				proto.Steps[i].LoopIteration = &loopIter
			}
		}
	}

	// Find and add children
	for _, child := range b.childrenByParent[wf.ID] {
		proto.Children = append(proto.Children, b.build(child))
	}

	return proto
}
