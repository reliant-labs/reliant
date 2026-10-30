// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/machinewait"
	"github.com/reliant-labs/reliant/internal/workflow/model"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// ContinueQueued starts a run to deliver a message the chat holds that no run
// is going to read, and reports whether it started one. Nothing calls it on a
// user's request — the user's own send already does all of this. It exists
// for the two places a message is left owed by something the user did not do:
//
//   - a message queued for the chat's machine (chats.queued_for_machine_at):
//     its run ended because the machine failed to start, was removed, or was
//     still down at the wait's cap, and the machine has since connected
//     (internal/queueddelivery);
//   - a message the run had taken in when the reconciler ended it as wedged:
//     a queued mailbox row, or a user message last in the thread.
//
// The run it starts is the one the user's next send would have started,
// without the new message: SendMessage's resume of an interrupted run —
// reset-and-replay when the history still replays, otherwise a fresh run at
// the last checkpoint (#672's path) — which reads the message on its first
// turn, from history or by draining the mailbox.
//
// It is idempotent and never delivers twice. It holds the chat's run-control
// lock, as SendMessage does, so it and a send (or a second caller: the sweep,
// another replica's event) take effect one after the other; and it starts
// nothing unless the root run is FAILED in the database and not running in
// Temporal. The run it starts is recorded as running before the lock is
// released, so the next caller finds it live and does nothing. Within that
// run the message is read once: a history message by its first turn, a
// mailbox row by the drain, whose claim is atomic.
func (s *Service) ContinueQueued(ctx context.Context, chatID string) (bool, error) {
	release := s.runs.LockRunControl(ctx, chatID)
	defer release()

	chat, err := s.deps.Repo.GetChat(ctx, chatID)
	if err != nil || chat == nil {
		return false, fmt.Errorf("load chat %s: %w", chatID, err)
	}
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return false, nil
	}
	if chat.State == db.ChatStateArchived {
		s.dropQueuedMessage(ctx, chatID, "the chat is archived")
		return false, nil
	}
	root, err := s.deps.Repo.GetWorkflow(ctx, workflowID)
	if err != nil || root == nil {
		return false, fmt.Errorf("load root run %s: %w", workflowID, err)
	}
	switch {
	case root.Status.Live():
		// A run that has not ended reads the message itself.
		return false, nil
	case root.Status != db.Failed():
		// Completed or stopped: a later run answered it, or the user stopped
		// the run. Either way nothing is owed.
		s.dropQueuedMessage(ctx, chatID, "the chat's run "+root.Status.Label())
		return false, nil
	}

	// Claim the queued message. Clearing the marker is the claim — of two
	// callers exactly one sees it change — and a failure below puts it back,
	// so the next connect or sweep tries again.
	queued, err := s.deps.Repo.SetChatQueuedForMachine(ctx, chatID, false)
	if err != nil {
		return false, err
	}
	requeue := func(why error) {
		logging.WarnContext(ctx, "[ContinueQueued] Could not start a run for the chat's undelivered message",
			"chatID", chatID, "workflowID", workflowID, "error", why)
		if queued {
			if _, err := s.deps.Repo.SetChatQueuedForMachine(ctx, chatID, true); err != nil {
				logging.ErrorContext(ctx, "[ContinueQueued] Could not re-queue the message for the machine; the user will have to send it again",
					"chatID", chatID, "error", err)
			}
		}
	}
	if !queued {
		owed, err := s.awaitsDelivery(ctx, root.Thread)
		if err != nil || !owed {
			return false, err
		}
	}

	inspection, err := s.runs.Inspect(ctx, chatID)
	if err != nil {
		requeue(err)
		return false, err
	}
	if inspection.Stuck {
		// The database says failed while Temporal still runs it; starting a
		// run now would terminate a live one. The reconciler converges that
		// state, and the next pass delivers.
		requeue(fmt.Errorf("run %s is still running in Temporal", workflowID))
		return false, nil
	}

	if inspection.Recoverable {
		// No send, so nothing to change: the run keeps its own inputs. A
		// model its provider can no longer serve is moved only by a send,
		// where the user acts and is told (#685).
		outcome, err := s.runs.ResumeInterrupted(ctx, chatID, nil)
		if err != nil {
			requeue(err)
			return false, err
		}
		if outcome.Kind == runs.OutcomeResumed {
			logging.InfoContext(ctx, "[ContinueQueued] Resumed the interrupted run to deliver the chat's undelivered message",
				"chatID", chatID, "workflowID", workflowID, "runID", outcome.RunID, "queuedForMachine", queued)
			return true, nil
		}
		if outcome.HistoryLimitExceeded {
			s.notifyHistoryLimitRestart(ctx, chatID, workflowID, root.Thread)
		}
	}

	runID, err := s.restartAtCheckpoint(ctx, chat, root)
	if err != nil {
		requeue(err)
		return false, err
	}
	logging.InfoContext(ctx, "[ContinueQueued] Started a run at the last checkpoint to deliver the chat's undelivered message",
		"chatID", chatID, "workflowID", workflowID, "runID", runID, "queuedForMachine", queued)
	return true, nil
}

// machineBackNote is what the agent is told when its chat is resumed because
// its machine came back. It sits in the thread, so the resumed run reads it on
// its next turn: the tool results just above it are the failures of the outage.
const machineBackNote = "Your machine has reconnected. Tool calls that failed because it was unreachable can be retried; continue where you left off."

// ResumePausedForMachine resumes a chat the daemon-offline circuit breaker
// paused (chats.paused_for_machine_at), now that its machine is back, and
// reports whether it did. It is SendMessage's resume of a paused run without a
// message: PauseService.ResumeWorkflow signals the parked run in place, or
// reset-replays a dead-but-replayable one, so the agent continues where it
// stopped. Callers (internal/queueddelivery) have already checked the machine
// is connected.
//
// It resumes only what the breaker paused. The mark is the whole difference
// between that and a chat the user paused, which must stay paused.
//
// It is idempotent and safe across replicas. It holds the chat's run-control
// lock, as SendMessage does, and clearing the mark is the claim — of two
// callers exactly one sees it change — so a connect handled twice, or raced by
// the sweep or by the user's own send, resumes once. A resume that fails puts
// the mark back so the next connect or sweep tries again.
func (s *Service) ResumePausedForMachine(ctx context.Context, chatID string) (bool, error) {
	release := s.runs.LockRunControl(ctx, chatID)
	defer release()

	chat, err := s.deps.Repo.GetChat(ctx, chatID)
	if err != nil || chat == nil {
		return false, fmt.Errorf("load chat %s: %w", chatID, err)
	}
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return false, nil
	}
	root, err := s.deps.Repo.GetWorkflow(ctx, workflowID)
	if err != nil || root == nil {
		return false, fmt.Errorf("load root run %s: %w", workflowID, err)
	}
	if chat.State == db.ChatStateArchived || root.Status != db.Paused() {
		// Archived, or the run is no longer paused: a send or the user already
		// resumed or ended it. Nothing is owed; drop the mark.
		if _, err := s.deps.Repo.SetChatPausedForMachine(ctx, chatID, false); err != nil {
			logging.WarnContext(ctx, "[ResumePausedForMachine] Could not clear the paused-for-machine mark", "chatID", chatID, "error", err)
		}
		return false, nil
	}

	claimed, err := s.deps.Repo.SetChatPausedForMachine(ctx, chatID, false)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, nil
	}
	restore := func(why error) {
		logging.WarnContext(ctx, "[ResumePausedForMachine] Could not resume the chat; it stays paused for its machine",
			"chatID", chatID, "workflowID", workflowID, "error", why)
		if _, err := s.deps.Repo.SetChatPausedForMachine(ctx, chatID, true); err != nil {
			logging.ErrorContext(ctx, "[ResumePausedForMachine] Could not put the paused-for-machine mark back; the user's next message resumes the chat",
				"chatID", chatID, "error", err)
		}
	}

	// Saved before the resume so it is in the thread when the woken run takes
	// its next turn. Best-effort: without it the agent's next tool call simply
	// succeeds.
	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	if _, err := s.deps.Repo.SaveMessageToThread(ctx, chatID, root.Thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		machineBackNote, &workflowID, nil, &hidden); err != nil {
		logging.WarnContext(ctx, "[ResumePausedForMachine] Could not tell the agent its machine is back", "chatID", chatID, "error", err)
	}

	outcome, err := s.runs.Resume(ctx, chatID, nil)
	if err != nil {
		restore(err)
		return false, err
	}
	if outcome.Kind != runs.OutcomeResumed {
		// The run is gone or wedged; resuming it in place is not possible. The
		// user's next message recovers it (SendMessage restarts at position),
		// and retrying here would only fail the same way.
		logging.WarnContext(ctx, "[ResumePausedForMachine] The paused run cannot be resumed in place; the user's next message recovers it",
			"chatID", chatID, "workflowID", workflowID, "outcome", outcome.Kind)
		return false, nil
	}
	logging.InfoContext(ctx, "[ResumePausedForMachine] The chat's machine is back; resumed the run the offline breaker paused",
		"chatID", chatID, "workflowID", workflowID, "runID", outcome.RunID)
	return true, nil
}

// awaitsDelivery reports whether a thread holds input that no turn has read:
// a queued mailbox row, or a user message that is its latest turn.
func (s *Service) awaitsDelivery(ctx context.Context, threadID string) (bool, error) {
	queuedRows, err := s.deps.Repo.CountQueuedAgentMessagesForThread(ctx, threadID)
	if err != nil {
		return false, fmt.Errorf("count queued mailbox rows for %s: %w", threadID, err)
	}
	if queuedRows > 0 {
		return true, nil
	}
	return s.deps.Repo.ThreadAwaitsReply(ctx, threadID)
}

// dropQueuedMessage clears a queued-for-machine marker nothing will deliver
// (see ContinueQueued). Best-effort.
func (s *Service) dropQueuedMessage(ctx context.Context, chatID, why string) {
	cleared, err := s.deps.Repo.SetChatQueuedForMachine(ctx, chatID, false)
	if err != nil {
		logging.WarnContext(ctx, "[ContinueQueued] Could not clear the queued-for-machine marker", "chatID", chatID, "error", err)
		return
	}
	if cleared {
		logging.InfoContext(ctx, "[ContinueQueued] Dropped a queued-for-machine marker nothing is owed for", "chatID", chatID, "why", why)
	}
}

// restartAtCheckpoint starts a fresh run for a chat whose root run is closed,
// entering at its last checkpoint (ResumeInputFromDurableState) on the root
// thread — SendMessage's coarse resume, without a new message. It returns the
// new run's id.
//
// The inputs are built the way SendMessage builds a closed chat's next run
// (inputsAfterClosedRun): presets plus the chat's stored selection, which is
// what every send and param edit recorded. They are not read back from the
// closed run — Temporal would replay its whole history to answer.
func (s *Service) restartAtCheckpoint(ctx context.Context, chat *db.Chat, root *db.Workflow) (string, error) {
	workflowID := root.ID
	workflowName := activeWorkflowNameForResume(chat, root)
	if workflowName == "" {
		return "", fmt.Errorf("chat %s has no workflow", chat.ID)
	}

	inputs, err := s.inputsAfterClosedRun(ctx, chat, workflowName)
	if err != nil {
		return "", err
	}
	// This run answers a person, not whatever launched the chat.
	delete(inputs, v2.InputKeyLaunchRun)
	v2.DropActingIdentity(inputs)
	s.chatPinner().Pin(ctx, chat)
	launch.InjectSessionDaemonID(inputs, chat)

	// The ended run left its thread terminal, and a terminal thread with a
	// queued mailbox row is what the reconciler's orphaned-mailbox sweep
	// marks undelivered. Revive it before the run exists.
	if _, err := s.deps.Repo.ReviveThread(ctx, root.Thread); err != nil {
		logging.WarnContext(ctx, "[ContinueQueued] Could not revive the root thread before restarting its run; starting it anyway",
			"chatID", chat.ID, "threadID", root.Thread, "error", err)
	}

	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chat.ID,
		WorkflowName: workflowName,
		Thread:       root.Thread,
		ThreadMode:   model.ThreadModeInherit,
	}
	if jwt, ok := auth.GetUserJWT(chat.UserID); ok {
		execContext.UserJWT = jwt
	}
	run, err := s.deps.Temporal.ExecuteWorkflow(ctx, s.newRunOptions(workflowID), v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID:       chat.ID,
		WorkflowName: workflowName,
		Inputs:       inputs,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.deps.Repo, chat.ID),
		Resume:       resumeWholeChatInput(v2.ResumeInputFromDurableState(ctx, s.deps.Repo, chat.ID, workflowID)),
	})
	if err != nil {
		return "", fmt.Errorf("start run: %w", err)
	}
	s.runs.RecordRun(ctx, chat.ID, workflowID, run.GetRunID())
	// Recorded as running now, not when the run's own "started" lands, so a
	// caller that takes the run-control lock next finds it live — but only
	// over the closed run's status: the new one may already have written its
	// own (workflow.MarkWokenRunActive).
	workflow.MarkWokenRunActive(ctx, s.deps.Repo, workflowID, root.Status, true)
	return run.GetRunID(), nil
}

// inputsAfterClosedRun builds the inputs for a run that follows a closed one
// without a request to carry params: the chat's presets, with its stored
// selection over them (launch.Launcher.BuildChatWorkflowInputs) — the same
// inputs SendMessage gives a closed chat's next run. The selection is the
// durable record of what each send and param edit gave the run
// (recordSelection), so it is what the closed run was running on.
//
// They are deliberately NOT read back from the closed run with a query.
// Temporal answers a query on a closed run by replaying its entire history on
// a worker, and on 2026-10-23 those replays (up to 37k events each, 13-17
// minutes of two cores) starved the worker pool (rtemporal.QueryOpenRun).
func (s *Service) inputsAfterClosedRun(ctx context.Context, chat *db.Chat, workflowName string) (map[string]interface{}, error) {
	presets := make(map[string]string, len(chat.SelectedPresets))
	for group, name := range chat.SelectedPresets {
		if name != "" {
			presets[group] = name
		}
	}
	return s.launcher().BuildChatWorkflowInputs(ctx, chat.UserID, chat, workflowName, presets, nil)
}

// nudgeMachineWait asks the chat's run, if it is parked waiting for its
// machine, to check the machine now rather than at its next recheck.
// Best-effort: a run that misses it checks on its own timer.
func (s *Service) nudgeMachineWait(ctx context.Context, chat *db.Chat) {
	workflowID := chat.MainThreadID()
	if workflowID == "" || s.deps.Temporal == nil {
		return
	}
	if err := s.deps.Temporal.SignalWorkflow(ctx, workflowID, "", machinewait.SignalName, machinewait.Signal{
		Reason: machinewait.ReasonUserMessage,
	}); err != nil {
		logging.WarnContext(ctx, "Could not ask the chat's run to check its machine", "chatID", chat.ID, "workflowID", workflowID, "error", err)
	}
}

// abandonMachineWait is "Continue without machine" seen from the chat it
// leaves: the conversation carries on in a no-machine branch, so this chat
// must neither keep a run waiting for the machine nor deliver its queued
// message when the machine comes back — that would answer the same request
// twice, in two chats, one of them unattended. It drops the queued message and
// tells a run parked in its preflight machine wait to end (the run ends
// cancelled; a run doing anything else does not listen and is left as it is).
// Best-effort: the branch has already been created.
func (s *Service) abandonMachineWait(ctx context.Context, chat *db.Chat) {
	s.dropQueuedMessage(ctx, chat.ID, "the user continued without the machine")
	workflowID := chat.MainThreadID()
	if workflowID == "" || s.deps.Temporal == nil {
		return
	}
	// Only a run that is waiting for its machine is told; any other run is
	// the user's to stop, and branching is not stopping it.
	if chat.Activity == nil || reliantv1.ChatActivity(*chat.Activity) != reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON {
		return
	}
	if err := s.deps.Temporal.SignalWorkflow(ctx, workflowID, "", machinewait.SignalName, machinewait.Signal{
		Abandon: true,
		Reason:  machinewait.ReasonContinuedWithoutMachine,
	}); err != nil {
		logging.WarnContext(ctx, "[BranchChat] Could not tell the source chat's run to stop waiting for its machine",
			"chatID", chat.ID, "workflowID", workflowID, "error", err)
	}
}
