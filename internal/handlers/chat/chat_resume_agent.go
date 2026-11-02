// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// A sub-agent that stopped on an error — its step's retries exhausted, a
// provider error, a provider usage limit, credits run out — is stopped, not
// finished: a message to it resumes it where it stopped. The workflow side is
// internal/workflow/runtime/subagent_resume.go; this is the send side.
//
// The message is queued in the agent's mailbox like any message to an agent,
// and the agent's doorbell rung in the chat's run, which resumes it. A chat
// whose run has ended — the usual state of a chat once the turn that read the
// failure is over — gets a run started for the message: a successor of the
// chat's run that resumes the agent before anything else, and whose root
// waits for the agent's new outcome before it takes a turn. Either way the
// agent's first turn reads the message.

// stoppedAgentResumability is whether a message can resume a stopped
// sub-agent now, and how.
type stoppedAgentResumability struct {
	// refusal is the user-facing reason it cannot, "" when it can.
	refusal string
	// runLive is whether the chat's run is live (running, pending or
	// paused): it holds the agent's params, and resumes it in place. A
	// paused run is resumed by the message first (resumePausedRunForMessage).
	runLive bool
}

// isResumableStoppedAgent reports whether thread is a sub-agent that stopped
// on an error — retries exhausted, a provider error, a usage limit — rather
// than one that finished. Such an agent is resumed by a message to it. One
// that completed, or that the user stopped (cancelled), is finished.
func isResumableStoppedAgent(thread *db.Thread, chat *db.Chat) bool {
	return thread != nil && chat != nil && thread.ID != chat.MainThreadID() &&
		thread.Origin == db.ThreadOriginSpawn && thread.Status == db.ThreadStatusFailed
}

// stoppedAgentResumability decides whether a message can resume a stopped
// sub-agent now.
//
// The agent reports to the agent that started it, so that one must be there
// to hear it: the chat's main thread always is (a run is started for it when
// none is going), and a sub-agent parent must not have finished. A parent
// that stopped on an error itself is resumed first, by a message of its own.
// A row that cannot be read fails open: the doorbell or the run that follows
// still finds out.
func (s *Service) stoppedAgentResumability(ctx context.Context, chat *db.Chat, thread *db.Thread) stoppedAgentResumability {
	var r stoppedAgentResumability
	if rootRun, err := s.deps.Repo.GetWorkflow(ctx, chat.MainThreadID()); err == nil && rootRun != nil {
		r.runLive = rootRun.Status.Live()
	}

	if thread.ParentThreadID != nil && *thread.ParentThreadID != chat.MainThreadID() {
		parent, err := s.deps.Repo.GetThread(ctx, *thread.ParentThreadID)
		if err == nil && parent != nil && core.ThreadStatusIsTerminal(parent.Status) {
			if isResumableStoppedAgent(parent, chat) {
				r.refusal = fmt.Sprintf(
					"This agent stopped on an error, and so did the agent that started it (%s). "+
						"Message that agent first to resume it; it can then resume this one.", parent.ID)
				return r
			}
			r.refusal = "This agent stopped on an error, and the agent that started it has since finished, so its result would have nowhere to go. " +
				fmt.Sprintf("Send a message to the chat instead — the main agent can resume it with spawn(agent_id=%q).", thread.ID)
		}
	}
	return r
}

// canResumeStoppedAgent reports whether threadID is a sub-agent that stopped
// on an error, that a message could resume, and whose chat's run is live to
// hold the params it will resume with.
func (s *Service) canResumeStoppedAgent(ctx context.Context, chat *db.Chat, threadID string) bool {
	thread, err := s.deps.Repo.GetThread(ctx, threadID)
	if err != nil || !isResumableStoppedAgent(thread, chat) {
		return false
	}
	r := s.stoppedAgentResumability(ctx, chat, thread)
	return r.refusal == "" && r.runLive
}

// stoppedAgentResumeReceipt is the receipt for a message that resumes a
// sub-agent that had stopped on an error. As honest as queuedReceipt: queued
// is not read.
const stoppedAgentResumeReceipt = "Queued. This agent had stopped on an error; your message resumes it where it stopped, " +
	"and it will read it at its first turn — it has not been read yet."

// stoppedAgentRunStartedReceipt is the receipt when the chat had no run and
// one was started to resume the agent.
const stoppedAgentRunStartedReceipt = "Queued. This agent had stopped on an error and the chat was idle, " +
	"so it has been restarted where it stopped; it will read your message at its first turn — it has not been read yet."

// resumeStoppedAgent delivers messageID, already queued for the stopped
// sub-agent on threadID: it rings the agent's doorbell in the chat's run,
// which resumes it (a run that is ending leaves that to its successor), or —
// when the chat has no run — starts one that resumes it.
//
// The doorbell, or the run, is the only thing that resumes a stopped agent:
// it has no next turn of its own to drain the row at. So unlike a live
// agent's, a doorbell that cannot be rung on a run that is still open is not
// a late delivery but none — the row is withdrawn rather than promised.
func (s *Service) resumeStoppedAgent(ctx context.Context, userID string, chat *db.Chat, threadID, messageID string, r stoppedAgentResumability) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	if err := s.notifyThreadWake(ctx, chat, threadID, threadwake.ReasonMailbox); err == nil {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: true,
			Message: stoppedAgentResumeReceipt,
		}), nil
	}
	if s.chatRunClosed(ctx, chat) {
		return s.startRunResumingStoppedAgent(ctx, userID, chat, threadID, messageID)
	}
	s.withdrawQueuedAgentMessage(ctx, chat, messageID)
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: false,
		Message: "This agent stopped on an error, and its run could not be reached to resume it. Try sending again in a moment.",
	}), nil
}

// chatRunClosed reports whether Temporal has no open execution for the chat's
// run. False when it cannot tell: a run that may be open must not be replaced.
func (s *Service) chatRunClosed(ctx context.Context, chat *db.Chat) bool {
	if s.runs == nil || s.deps.Temporal == nil {
		return false
	}
	state, err := s.runs.State(ctx, chatRunWorkflowID(chat))
	if err != nil {
		logging.WarnContext(ctx, "Could not tell whether a chat's run is open", "error", err, "chatID", chat.ID)
		return false
	}
	return !state.Exists || !state.IsRunning
}

// chatRunWorkflowID is the workflow ID of the chat's run: the one execution
// that drives every thread in the chat.
func chatRunWorkflowID(chat *db.Chat) string {
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		return *chat.WorkflowID
	}
	return chat.ID
}

// withdrawQueuedAgentMessage cancels a queued row nothing will deliver, so the
// composer's refusal — which keeps the text — is the whole story.
func (s *Service) withdrawQueuedAgentMessage(ctx context.Context, chat *db.Chat, messageID string) {
	if _, err := s.deps.Repo.CancelQueuedAgentMessage(ctx, messageID, chat.ID); err != nil {
		logging.WarnContext(ctx, "Could not withdraw a message queued to a stopped agent that could not be resumed",
			"error", err, "chatID", chat.ID, "messageID", messageID)
	}
}

// startRunResumingStoppedAgent starts the chat's run to resume the stopped
// sub-agent on threadID, for messageID queued to it while the chat had no run.
//
// It is the run SendMessage would start for an interrupted chat — the chat's
// workflow, resumed from durable state rather than entered at graph entry
// (ResumeInputFromDurableState) — with two additions: the stopped agent is
// resumed before the run enters its loop (ResumeInput.ResumeStopped, derived
// from the agent's durable rows), and the root waits on it first
// (AwaitSpawnsFirst). So nothing is asked of the root's model until the
// agent reports, and the root's next turn is the one that reads the report.
//
// Under the run-control lock, like every start of a chat's run: a send that
// started the run meanwhile is rung instead of replaced.
func (s *Service) startRunResumingStoppedAgent(ctx context.Context, userID string, chat *db.Chat, threadID, messageID string) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	refuse := func(cause error, message string) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
		logging.WarnContext(ctx, "Could not start a run to resume a stopped sub-agent; withdrawing the message queued to it",
			"error", cause, "chatID", chat.ID, "threadID", threadID, "messageID", messageID)
		s.withdrawQueuedAgentMessage(ctx, chat, messageID)
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{Success: false, Message: message}), nil
	}
	const tryAgain = "This agent stopped on an error, and the chat's run could not be started to resume it. Try sending again in a moment."
	if s.runs == nil || s.deps.Temporal == nil {
		return refuse(fmt.Errorf("no run service"), tryAgain)
	}

	release := s.runs.LockRunControl(ctx, chat.ID)
	defer release()

	// A send that held the lock first may have started the run since.
	if !s.chatRunClosed(ctx, chat) {
		if err := s.notifyThreadWake(ctx, chat, threadID, threadwake.ReasonMailbox); err != nil {
			return refuse(err, tryAgain)
		}
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: true,
			Message: stoppedAgentResumeReceipt,
		}), nil
	}

	stopped, err := v2.StoppedSpawnFromDurableState(ctx, s.deps.Repo, chat.ID, threadID)
	if err != nil {
		return refuse(err, tryAgain)
	}
	if stopped == nil {
		return refuse(fmt.Errorf("thread %s has no stopped spawn run", threadID),
			"This agent is no longer stopped on an error, so there is nothing to resume. Send a message to the chat instead.")
	}
	if chat.WorkflowName == nil || *chat.WorkflowName == "" {
		return refuse(fmt.Errorf("chat has no workflow"), tryAgain)
	}
	workflowName := *chat.WorkflowName
	workflowID := chat.MainThreadID()

	// Place the chat on its machine before its inputs are built: they are
	// read from that machine's catalog (presets, input schema), the same one
	// the run's own loads resolve against once it is pinned. Building first
	// read the union for a chat with no pin yet, then ran another machine's
	// definition.
	s.chatPinner().Pin(ctx, chat)

	// The run continues the chat's last one, so it runs on that run's inputs
	// while that run is still open to say them. A closed run is never asked
	// (Temporal would replay its whole history to answer; rtemporal.
	// QueryOpenRun refuses instead): the inputs are rebuilt as a new run's
	// would be — presets plus the chat's stored selection, which holds the
	// model the user last sent with — and checked as SendMessage checks them.
	inputs := s.runInputsForSend(ctx, workflowID)
	if inputs != nil {
		delete(inputs, v2.InputKeyLaunchRun)
		v2.DropActingIdentity(inputs)
		inputs = s.overSelection(ctx, chat.ID, workflowName, inputs)
		s.refreshProviderPolicy(ctx, chat, inputs)
	} else {
		presets := make(map[string]string, len(chat.SelectedPresets))
		for k, v := range chat.SelectedPresets {
			if v != "" {
				presets[k] = v
			}
		}
		var err error
		inputs, err = s.launcher().BuildChatWorkflowInputs(ctx, userID, chat, workflowName, presets, nil)
		if err != nil {
			return refuse(err, "This chat's settings need attention before it can run again: "+err.Error()+".")
		}
		if validationErrors := s.launcher().ValidateChatWorkflowInputs(ctx, userID, chat, workflowName, inputs); len(validationErrors) > 0 {
			msgs := make([]string, len(validationErrors))
			for i, e := range validationErrors {
				msgs[i] = e.Error()
			}
			return refuse(fmt.Errorf("workflow input validation failed: %s", strings.Join(msgs, "; ")),
				"This chat's settings need attention before it can run again: "+strings.Join(msgs, "; ")+". Fix them, then send again.")
		}
	}
	s.fallBackFromUnservableModels(ctx, userID, chat, workflowName, workflowID, workflowID, inputs, nil)
	s.refreshChatProvider(ctx, userID, chat, workflowName, inputs)
	launch.InjectSessionDaemonID(inputs, chat)

	resume := v2.ResumeInputFromDurableState(ctx, s.deps.Repo, chat.ID, workflowID)
	resume.ResumeStopped = []v2.SpawnHandoff{*stopped}
	resume.ResumeAllStopped = true
	resume.AwaitSpawnsFirst = true

	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chat.ID,
		WorkflowName: workflowName,
		Thread:       workflowID,
		ThreadMode:   model.ThreadModeInherit,
	}
	if jwt, ok := auth.GetUserJWT(userID); ok {
		execContext.UserJWT = jwt
	}
	run, err := s.deps.Temporal.ExecuteWorkflow(ctx, s.newRunOptions(workflowID), v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID:       chat.ID,
		WorkflowName: workflowName,
		Inputs:       inputs,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.deps.Repo, chat.ID),
		Resume:       resume,
	})
	if err != nil {
		return refuse(err, tryAgain)
	}
	s.runs.RecordRun(ctx, chat.ID, workflowID, run.GetRunID())
	logging.InfoContext(ctx, "Started the chat's run to resume a sub-agent that stopped on an error, for a message sent to it",
		"chatID", chat.ID, "workflowID", workflowID, "runID", run.GetRunID(),
		"threadID", threadID, "toolCallID", stopped.ToolCallID)
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: stoppedAgentRunStartedReceipt,
	}), nil
}
