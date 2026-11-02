// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// A message the user sends to a paused chat is the user asking it to go on,
// whichever way it is sent: SendMessage saves it to the thread's history,
// SendAgentMessage queues it in the thread's mailbox (the composer's queue,
// and any message to a sub-agent). Both resume the run here, the same way.
//
// SendAgentMessage used to only ring the thread's doorbell. A paused run's
// threads are parked behind the pause gate, which the doorbell does not open,
// so the message sat queued with nothing coming for it. Prod, chat adcee226
// (2026-10-21): the user paused, typed "continue" to the root, and the run
// stayed paused with the message unread.

// resumePausedRun resumes the chat's paused run for a message the user just
// sent to targetThread, and reports what the run service did.
//
// The run resumes with an input update: stateUpdate (the send's validated
// param/preset changes, nil for a send that carries none) and the run's own
// model pin, each moved off a provider that cannot serve it (#685) — even when
// the send carries no model at all (resumeInputUpdate). When the run is
// resumed in place, the model moves are announced on targetThread, recorded in
// the chat's selection, and the run's row is marked running.
//
// An error is a resume that did not reach the run; the caller's message is
// already durable, so a retry resumes from exactly here. Any outcome other
// than OutcomeResumed is the caller's to recover: the paused run is gone or
// wedged.
func (s *Service) resumePausedRun(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName, targetThread string,
	stateUpdate map[string]interface{},
	sent map[string]*structpb.Value,
) (runs.ResumeOutcome, error) {
	workflowID := chat.MainThreadID()

	runInputs := s.runInputsForSend(ctx, workflowID)
	inputUpdate, modelNotices, modelMoves := s.resumeInputUpdate(ctx, userID, chat, workflowName, workflowID,
		s.underSelection(ctx, chat.ID, workflowName, stateUpdate), runInputs)
	inputUpdate = s.withProviderPolicy(ctx, userID, chat, runInputs, inputUpdate)
	inputUpdate = s.refreshLiveChatProvider(ctx, userID, chat, workflowName, inputUpdate)

	// Only add "params changed" message if params actually changed from current workflow state
	if len(inputUpdate) > 0 && (runInputs == nil || inputsDiffer(runInputs, inputUpdate)) {
		hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
		_, err := s.deps.Repo.SaveMessageToThread(ctx, chat.ID, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
		if err != nil {
			logging.WarnContext(ctx, "Failed to save system message about param changes", "error", err, "chatID", chat.ID)
		}
	}

	// Resume the run. The service signals a live execution, reset-replays a
	// dead-but-replayable one, and refreshes the chat's run id when a reset
	// minted a new one — and whichever run wakes gets the input update first.
	before, known := workflow.StatusBeforeWake(ctx, s.deps.Repo, workflowID)
	outcome, err := s.runs.Resume(ctx, chat.ID, inputUpdate)
	if err != nil || outcome.Kind != runs.OutcomeResumed {
		return outcome, err
	}
	// A message that resumes the chat resumes all of it: every sub-agent a
	// provider stopped goes on too, without a message of its own.
	if err := s.resumeWholeChat(ctx, chat); err != nil {
		logging.WarnContext(ctx, "Could not wake every unfinished thread after a message resumed the chat", "chatID", chat.ID, "error", err)
	}

	// Told only now: the run that has the moved models is awake. A fresh run
	// (the caller's recovery) makes and announces its own.
	s.postModelFallbackNotices(ctx, chat.ID, workflowID, targetThread, modelNotices)
	_, _ = s.recordSelection(ctx, userID, chat, workflowName, sent, inputUpdate, modelMoves, 0)

	// The run is awake: mark it running, but only over the status it had
	// before the resume. By now the woken run may well have finished, and an
	// unconditional write of active this late overwrote the run's own
	// COMPLETED, leaving a closed run whose chat read as running (e2e story
	// 05). The resume usually marked it already, which makes this a no-op.
	workflow.MarkWokenRunActive(ctx, s.deps.Repo, workflowID, before, known)
	return outcome, nil
}

// resumePausedRunForMessage resumes the chat's run if it is paused, for
// messageID, which SendAgentMessage has just queued to threadID. It returns a
// response for SendAgentMessage to return instead of its receipt when the
// message cannot be delivered, and nil when the caller should carry on — the
// run was resumed, or was not paused.
//
// The pause is the chat's: every thread of the chat runs in its one
// execution, so the root workflow's row says whether the run is paused, for a
// sub-agent's message as much as the root's.
//
// It holds the run-control lock from the status read through the resume, as
// SendMessage does, so a pause still being applied (signalled, not yet
// written) is seen as the pause it is rather than as a running run that only
// needs its doorbell rung. The caller rings the doorbell after this returns:
// resuming opens the pause gate, but a thread parked on its own wait (its
// background spawns, an ask_user question) wakes only for the doorbell.
//
// A paused run that cannot be resumed in place (gone from Temporal, or wedged)
// is not resumed here: the queued message is withdrawn and the user is told to
// send it to the chat, where SendMessage recovers the run at its position. The
// composer keeps the text on a refusal, so nothing is lost.
func (s *Service) resumePausedRunForMessage(ctx context.Context, userID string, chat *db.Chat, threadID, messageID string) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	if s.runs == nil || s.deps.Temporal == nil {
		return nil, nil
	}
	release := s.runs.LockRunControl(ctx, chat.ID)
	defer release()

	root, err := s.deps.Repo.GetWorkflow(ctx, chat.MainThreadID())
	if err != nil || root == nil || root.Status != db.Paused() {
		// Not paused, or unreadable: the doorbell the caller rings is the
		// delivery, as it is for any live run.
		return nil, nil
	}

	outcome, err := s.resumePausedRun(ctx, userID, chat, root.WorkflowName, threadID, nil, nil)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resume paused chat"))
	}
	if outcome.Kind != runs.OutcomeResumed {
		logging.WarnContext(ctx, "A paused chat could not be resumed in place for a queued message; withdrawing it",
			"chatID", chat.ID, "threadID", threadID, "outcome", outcome.Kind)
		if _, err := s.deps.Repo.CancelQueuedAgentMessage(ctx, messageID, chat.ID); err != nil {
			logging.WarnContext(ctx, "Could not withdraw the queued message", "error", err, "chatID", chat.ID, "messageID", messageID)
		}
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: "This chat is paused and its run could not be resumed where it stopped. " +
				"Send this as a normal message to the chat instead — that restarts it from where it was.",
		}), nil
	}
	logging.InfoContext(ctx, "Resumed a paused chat for a message queued to it",
		"chatID", chat.ID, "threadID", threadID, "runID", outcome.RunID)
	return nil, nil
}
