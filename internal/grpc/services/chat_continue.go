// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
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
func (s *ChatService) ContinueQueued(ctx context.Context, chatID string) (bool, error) {
	release := s.runs.LockRunControl(ctx, chatID)
	defer release()

	chat, err := s.database.GetChat(ctx, chatID)
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
	root, err := s.database.GetWorkflow(ctx, workflowID)
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
	queued, err := s.database.SetChatQueuedForMachine(ctx, chatID, false)
	if err != nil {
		return false, err
	}
	requeue := func(why error) {
		logging.Warn("[ContinueQueued] Could not start a run for the chat's undelivered message",
			"chatID", chatID, "workflowID", workflowID, "error", why)
		if queued {
			if _, err := s.database.SetChatQueuedForMachine(ctx, chatID, true); err != nil {
				logging.Error("[ContinueQueued] Could not re-queue the message for the machine; the user will have to send it again",
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
			logging.Info("[ContinueQueued] Resumed the interrupted run to deliver the chat's undelivered message",
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
	logging.Info("[ContinueQueued] Started a run at the last checkpoint to deliver the chat's undelivered message",
		"chatID", chatID, "workflowID", workflowID, "runID", runID, "queuedForMachine", queued)
	return true, nil
}

// awaitsDelivery reports whether a thread holds input that no turn has read:
// a queued mailbox row, or a user message that is its latest turn.
func (s *ChatService) awaitsDelivery(ctx context.Context, threadID string) (bool, error) {
	queuedRows, err := s.database.CountQueuedAgentMessagesForThread(ctx, threadID)
	if err != nil {
		return false, fmt.Errorf("count queued mailbox rows for %s: %w", threadID, err)
	}
	if queuedRows > 0 {
		return true, nil
	}
	return s.database.ThreadAwaitsReply(ctx, threadID)
}

// dropQueuedMessage clears a queued-for-machine marker nothing will deliver
// (see ContinueQueued). Best-effort.
func (s *ChatService) dropQueuedMessage(ctx context.Context, chatID, why string) {
	cleared, err := s.database.SetChatQueuedForMachine(ctx, chatID, false)
	if err != nil {
		logging.Warn("[ContinueQueued] Could not clear the queued-for-machine marker", "chatID", chatID, "error", err)
		return
	}
	if cleared {
		logging.Info("[ContinueQueued] Dropped a queued-for-machine marker nothing is owed for", "chatID", chatID, "why", why)
	}
}

// closedRunInputsTimeout bounds the get_workflow_inputs query on a closed run,
// which Temporal answers by replaying its history on a worker.
const closedRunInputsTimeout = 30 * time.Second

// restartAtCheckpoint starts a fresh run for a chat whose root run is closed,
// entering at its last checkpoint (ResumeInputFromDurableState) on the root
// thread — SendMessage's coarse resume, without a new message. It returns the
// new run's id.
//
// The inputs are the closed run's own, read back with its get_workflow_inputs
// query (as startRunForQueuedRow does): no request carries params here, and
// rebuilding them from the chat's presets would quietly swap the model the user
// chose for the default. Only when that history cannot be replayed (a wedged
// run, past retention) are they rebuilt from the presets.
func (s *ChatService) restartAtCheckpoint(ctx context.Context, chat *db.Chat, root *db.Workflow) (string, error) {
	workflowID := root.ID
	workflowName := activeWorkflowNameForResume(chat, root)
	if workflowName == "" {
		return "", fmt.Errorf("chat %s has no workflow", chat.ID)
	}

	inputs := s.closedRunInputs(ctx, workflowID)
	if inputs == nil {
		presets := make(map[string]string, len(chat.SelectedPresets))
		for group, name := range chat.SelectedPresets {
			if name != "" {
				presets[group] = name
			}
		}
		checkout := s.launcher().GetEffectiveCheckout(ctx, chat)
		inputs = s.launcher().BuildWorkflowInputs(ctx, chat.UserID, checkout, chat.ProjectID, workflowName, presets, nil)
	}
	// This run answers a person, not whatever launched the chat.
	delete(inputs, v2.InputKeyLaunchRun)
	launch.InjectSessionDaemonID(inputs, chat)

	// The ended run left its thread terminal, and a terminal thread with a
	// queued mailbox row is what the reconciler's orphaned-mailbox sweep
	// marks undelivered. Revive it before the run exists.
	if _, err := s.database.ReviveThread(ctx, root.Thread); err != nil {
		logging.Warn("[ContinueQueued] Could not revive the root thread before restarting its run; starting it anyway",
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
	run, err := s.tempClient.ExecuteWorkflow(ctx, s.newRunOptions(workflowID), v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID:       chat.ID,
		WorkflowName: workflowName,
		Inputs:       inputs,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.database, chat.ID),
		Resume:       v2.ResumeInputFromDurableState(ctx, s.database, chat.ID, workflowID),
	})
	if err != nil {
		return "", fmt.Errorf("start run: %w", err)
	}
	s.runs.RecordRun(ctx, chat.ID, workflowID, run.GetRunID())
	// Recorded as running now, not when the run's own "started" lands, so a
	// caller that takes the run-control lock next finds it live.
	if err := s.database.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
		logging.Warn("[ContinueQueued] Started the run but could not mark it running; the run's own status write corrects it",
			"chatID", chat.ID, "workflowID", workflowID, "error", err)
	}
	return run.GetRunID(), nil
}

// closedRunInputs reads a closed run's inputs back with its
// get_workflow_inputs query; nil when Temporal cannot answer.
func (s *ChatService) closedRunInputs(ctx context.Context, workflowID string) map[string]interface{} {
	queryCtx, cancel := context.WithTimeout(ctx, closedRunInputsTimeout)
	defer cancel()
	encoded, err := s.tempClient.QueryWorkflow(queryCtx, workflowID, "", "get_workflow_inputs")
	if err != nil {
		logging.Info("[ContinueQueued] Could not read the closed run's inputs; rebuilding them from the chat's presets",
			"workflowID", workflowID, "error", err)
		return nil
	}
	var inputs map[string]interface{}
	if err := encoded.Get(&inputs); err != nil || inputs == nil {
		logging.Info("[ContinueQueued] Could not decode the closed run's inputs; rebuilding them from the chat's presets",
			"workflowID", workflowID, "error", err)
		return nil
	}
	return inputs
}

// nudgeMachineWait asks the chat's run, if it is parked waiting for its
// machine, to check the machine now rather than at its next recheck.
// Best-effort: a run that misses it checks on its own timer.
func (s *ChatService) nudgeMachineWait(ctx context.Context, chat *db.Chat) {
	workflowID := chat.MainThreadID()
	if workflowID == "" || s.tempClient == nil {
		return
	}
	if err := s.tempClient.SignalWorkflow(ctx, workflowID, "", machinewait.SignalName, machinewait.Signal{
		Reason: machinewait.ReasonUserMessage,
	}); err != nil {
		logging.Warn("Could not ask the chat's run to check its machine", "chatID", chat.ID, "workflowID", workflowID, "error", err)
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
func (s *ChatService) abandonMachineWait(ctx context.Context, chat *db.Chat) {
	s.dropQueuedMessage(ctx, chat.ID, "the user continued without the machine")
	workflowID := chat.MainThreadID()
	if workflowID == "" || s.tempClient == nil {
		return
	}
	// Only a run that is waiting for its machine is told; any other run is
	// the user's to stop, and branching is not stopping it.
	if chat.Activity == nil || reliantv1.ChatActivity(*chat.Activity) != reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON {
		return
	}
	if err := s.tempClient.SignalWorkflow(ctx, workflowID, "", machinewait.SignalName, machinewait.Signal{
		Abandon: true,
		Reason:  machinewait.ReasonContinuedWithoutMachine,
	}); err != nil {
		logging.Warn("[BranchChat] Could not tell the source chat's run to stop waiting for its machine",
			"chatID", chat.ID, "workflowID", workflowID, "error", err)
	}
}
