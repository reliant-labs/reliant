package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// queuedMessageID is the id of a queued row: the id the client chose for its
// message (already vetted as a UUID), otherwise a fresh one.
func queuedMessageID(clientID string) string {
	if clientID != "" {
		return clientID
	}
	return uuid.New().String()
}

// SendAgentMessage queues a HUMAN message directly into a specific running
// thread's mailbox (agent_messages), without pausing or otherwise touching
// the chat's workflow/pause state. It is the human-facing counterpart to the
// spawn_send LLM tool: today a user's only way to steer a running sub-agent
// is to pause the whole chat first, which this RPC exists to close.
//
// Delivery reuses the same drain machinery spawn_send does — the message is
// folded into the target thread's history at its next agent-loop step
// boundary, not synchronously.
func (s *Service) SendAgentMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.SendAgentMessageRequest],
) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.ThreadId == "" {
		return nil, svcerr.InvalidArgument("thread_id is required")
	}
	if strings.TrimSpace(req.Msg.Message) == "" {
		return nil, svcerr.InvalidArgument("message is required")
	}
	clientID, err := parseClientMessageID(req.Msg.GetClientMessageId())
	if err != nil {
		return nil, err
	}

	// Ownership check: the chat must belong to the caller. Combined with the
	// thread.ChatID check below, this is what stops a user from addressing
	// an arbitrary thread in someone else's chat.
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat == nil {
		return nil, svcerr.NotFound("chat")
	}

	target, err := s.deps.Repo.GetThread(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get thread"))
	}
	if target == nil {
		return nil, svcerr.NotFound("thread")
	}
	if target.ChatID != req.Msg.ChatId {
		// Deliberately the same NotFound the chat-ownership check above
		// returns, rather than a distinguishable error: revealing that a
		// thread ID exists but belongs to a different chat is an
		// enumeration leak we don't need to offer.
		return nil, svcerr.NotFound("thread")
	}

	// A repeat of a send whose message the agent already read is answered with
	// the receipt it got the first time, before any liveness check: the run
	// that read it may well have finished since, and "this agent has finished"
	// is not the answer to a message that was delivered. A repeat of one still
	// queued carries on below and finds its row there.
	if clientID != "" {
		prior, err := s.deps.Repo.GetAgentMessage(ctx, clientID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check for an earlier send"))
		}
		if prior != nil {
			if !humanMessageIs(prior, chat.ID, req.Msg.Message, req.Msg.Attachments) {
				return nil, launch.ErrClientMessageReused
			}
			if prior.Status == core.AgentMessageStatusDelivered {
				return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
					Success: true,
					Message: queuedReceipt,
				}), nil
			}
		}
	}

	owningWorkflow := s.owningWorkflow(ctx, target)
	isMainThread := req.Msg.ThreadId == chat.MainThreadID()

	// A message to the chat's MAIN thread after its run ended is a message to
	// the chat, and gets what SendMessage gives one: a run started to answer
	// it. The composer routes a send to this queue for as long as it shows
	// the run as working — including the instant the run finishes — so
	// refusing here left the message in the composer to be sent a second
	// time. Only a run that ended with no position to resume from qualifies
	// (completed, or stopped by the user): those are the runs SendMessage
	// starts afresh. A failed run is SendMessage's to resume, so it is still
	// refused below.
	restartMainRun := isMainThread && s.deps.Temporal != nil && s.runs != nil && owningWorkflow != nil &&
		owningWorkflow.Status.IsStopped() && !owningWorkflow.Status.Resumable()

	// A sub-agent that stopped on an error is not finished: a message to it
	// resumes it, inside the chat's run, which relaunches it when its
	// doorbell rings — or, when the chat has no run, inside one started for
	// it (internal/workflow/runtime/subagent_resume.go). Its thread and its
	// own workflow row are terminal, so it bypasses both liveness checks
	// below.
	resumeStoppedAgent := isResumableStoppedAgent(target, chat)
	var resumability stoppedAgentResumability
	if resumeStoppedAgent {
		resumability = s.stoppedAgentResumability(ctx, chat, target)
		if resumability.refusal != "" {
			return connect.NewResponse(&reliantv1.SendAgentMessageResponse{Success: false, Message: resumability.refusal}), nil
		}
	}

	// A terminal thread means the loop has exited. This is trustworthy in the
	// closing direction because a thread is only revived by the run that
	// starts on it (WorkflowStatusActivity's "started" arm calls
	// ReviveThread), so a terminal status here is never merely a stale stamp
	// left by an earlier turn of a reused main thread.
	//
	// Deliberately NOT self-healed here from "the workflow is running", even
	// though that is the shape the revival repairs: a spawn thread that has
	// just written its own "completed" sits in exactly that state for the
	// moment before its workflow follows, and reviving it would queue a
	// message into an agent that really has stopped.
	if !restartMainRun && !resumeStoppedAgent && core.ThreadStatusIsTerminal(target.Status) {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: fmt.Sprintf(
				"This agent has already finished (status: %s) — its loop has exited and there is nothing to deliver into. "+
					"Send a new message to the chat instead.",
				core.ThreadStatusLabel(target.Status)),
		}), nil
	}

	// A non-terminal thread status is NOT sufficient to prove there is a next
	// turn to deliver into, so the terminal check above cannot stand alone.
	//
	// Delivery only happens in CallLLM, which drains the thread's mailbox
	// before it reads history. An agent that is not executing never reaches
	// another CallLLM, so a message queued to one sits at status=queued
	// indefinitely while the receipt claims it will be read next turn.
	//
	// threads.status cannot answer this. It is written by the ThreadStatus
	// activity, which only ever records "started" (=running) and a terminal
	// verb — there is no "went idle" transition, so a thread that stopped
	// without its workflow completing keeps claiming running forever. That is
	// not a corner case: in the live DB every one of the 142 main threads is
	// status=running with zero exceptions and no completed_at, including
	// chats last active weeks ago. Trusting it is exactly the bug.
	//
	// The workflow that owns the thread is the signal that does move. It is
	// reconciled against Temporal (the actual execution), so it is the same
	// truth the send path and the reconciler already act on, and it is one
	// indexed primary-key read rather than a Temporal round trip on this hot
	// user-facing path.
	//
	// PENDING and PAUSED count as live — see WorkflowStatus.Live. A message
	// queued to a pending run is drained when the run starts; one queued to a
	// paused run resumes it below (resumePausedRunForMessage) and is drained
	// at the thread's next turn. Refusing either would lose a message that
	// would have arrived.
	//
	// A workflow row that cannot be read fails open (owningWorkflow logs it):
	// it is not proof the agent is idle, and wrongly refusing loses the
	// message outright, whereas wrongly accepting only reproduces today's
	// late delivery.
	if !restartMainRun && !resumeStoppedAgent && owningWorkflow != nil && !owningWorkflow.Status.Live() {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: fmt.Sprintf(
				"This agent isn't currently running (its run is %s), so there is no next turn to deliver into. "+
					"Send a normal message to the chat to start one.",
				owningWorkflow.Status.Label()),
		}), nil
	}

	// The ended run left the thread terminal, and a terminal thread with
	// queued mail whose run Temporal reports closed is exactly what the
	// reconciler's orphaned-mailbox sweep marks undelivered. Revive it before
	// the row exists, so the sweep never sees the row in that shape; the run
	// that drains it — this one's successor, or the one started below — would
	// revive it anyway.
	if restartMainRun {
		if _, err := s.deps.Repo.ReviveThread(ctx, req.Msg.ThreadId); err != nil {
			logging.WarnContext(ctx, "Could not revive the main thread before queueing to its ended run",
				"error", err, "chatID", req.Msg.ChatId, "threadID", req.Msg.ThreadId)
		}
	}

	msg := &db.AgentMessage{
		ID: queuedMessageID(clientID),
		// FromThreadID is a required FK to threads(id), and the human
		// sending this has no thread of their own. The chat's root thread
		// is the closest stable stand-in for "the user's side of this
		// conversation" — Kind (HumanMessage) is what actually tells the
		// drain envelope this came from the user rather than a peer agent,
		// so FromThreadID here is not read as a sender label for this kind.
		FromThreadID: chat.MainThreadID(),
		ChatID:       req.Msg.ChatId,
		ToThreadID:   req.Msg.ThreadId,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         req.Msg.Message,
		Attachments:  req.Msg.Attachments,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now(),
	}
	// Queued once: a repeat of a send that already queued the message finds its
	// row and finishes around it, so what the first attempt may not have
	// reached (the wake, the run to start) still happens, and nothing is
	// queued twice.
	queued, inserted, err := s.enqueueHumanMessage(ctx, msg, clientID != "")
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to queue message"))
	}
	msg = queued
	if !inserted && msg.Status != core.AgentMessageStatusQueued {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{Success: true, Message: queuedReceipt}), nil
	}
	if !inserted && restartMainRun {
		// The first attempt may have started the run for this row already;
		// starting another would terminate it.
		if state, stateErr := s.runs.State(ctx, chat.MainThreadID()); stateErr == nil && state.Exists && state.IsRunning {
			return connect.NewResponse(&reliantv1.SendAgentMessageResponse{Success: true, Message: queuedReceipt}), nil
		}
	}

	// A paused chat takes no turn until it is resumed, and the doorbell below
	// does not resume it. A message is the user asking the chat to go on, so
	// it resumes the run first, as SendMessage does.
	if refused, err := s.resumePausedRunForMessage(ctx, userID, chat, req.Msg.ThreadId, msg.ID); refused != nil || err != nil {
		return refused, err
	}

	switch {
	case resumeStoppedAgent:
		return s.resumeStoppedAgent(ctx, userID, chat, req.Msg.ThreadId, msg.ID, resumability)
	case !isMainThread:
		_ = s.notifyThreadWake(ctx, chat, req.Msg.ThreadId, threadwake.ReasonMailbox)
	case restartMainRun && owningWorkflow.Status.StopReason == core.StopReasonCancelled:
		// A stopped run exits without reading its doorbell, even while
		// Temporal still has it open, so a wake would strand the row. Start
		// the run directly, as SendMessage does for a stopped run.
		return s.startRunForQueuedRow(ctx, chat, req.Msg.ThreadId, msg.ID)
	case s.wakeLiveRun(ctx, chat, req.Msg.ThreadId, threadwake.ReasonMailbox):
		// The chat's run has closed — before this RPC, or between the
		// liveness read above and this wake — so nothing will drain the row.
		// Start a run for it rather than leave it for the reconciler to mark
		// undelivered. A completed run that is still open is inside its
		// end-of-run check, which takes the wake and continues as a fresh
		// run for it (late_wake.go), so it lands here only once it is closed.
		return s.startRunForQueuedRow(ctx, chat, req.Msg.ThreadId, msg.ID)
	}

	// The receipt is deliberately as honest as spawn_send's: queued does not
	// mean read, and does not mean acted on.
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: queuedReceipt,
	}), nil
}

const queuedReceipt = "Queued for delivery. It will be read at that agent's next turn — it has not been read yet."

// owningWorkflow is the workflow row that owns a thread: the thread's own
// workflow, or the one named after it. Nil when it cannot be read.
func (s *Service) owningWorkflow(ctx context.Context, thread *db.Thread) *db.Workflow {
	workflowID := thread.ID
	if thread.WorkflowID != nil && *thread.WorkflowID != "" {
		workflowID = *thread.WorkflowID
	}
	wf, err := s.deps.Repo.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		logging.WarnContext(ctx, "Could not read owning workflow for agent-message liveness check; allowing the queue",
			"error", err, "chatID", thread.ChatID, "threadID", thread.ID, "workflowID", workflowID)
		return nil
	}
	return wf
}

// notifyThreadWake rings the thread-wake doorbell on the workflow that owns
// the recipient thread, so a thread parked waiting on its background spawns
// wakes up and takes a turn rather than sleeping until a sub-agent finishes.
//
// Two callers, one per reason:
//
//   - SendAgentMessage, after queuing a row into agent_messages. Without this
//     the enqueue is silent and delivery depends on the recipient reaching a
//     loop-step boundary for some other reason.
//   - SendMessage, after saving a user message to a thread whose run is live.
//     A PARKED loop never takes another turn without it, and a loop on its
//     LAST turn has already read history and would end the run with the
//     message unread; the run reads the wake in both places (late_wake.go).
//
// When the recipient is a parent blocked in awaitLiveDetachedSpawns — the
// ordinary state of a main thread that has fanned work out to sub-agents — no
// boundary is scheduled either way, so "it will be read at that agent's next
// turn" was a promise with no turn behind it.
//
// threadID is load-bearing and must be the ACTUAL recipient. Every thread in a
// chat is driven by one Temporal execution, so the workflow this signals is
// always the chat's; the payload's thread is the only thing that selects which
// gate wakes. Passing the root thread for a message aimed at a sub-thread
// would wake the parent for input that is not its own.
//
// Never fails the RPC: whatever prompted the wake is already durable — the
// mailbox row, or the saved message — and failing would report failure for a
// message that IS saved. The error is returned for the callers that must act
// on it: a wake that reaches no run means the run closed after the caller
// found it live, and nothing will read the input until a run is started for
// it (wakeLiveRun).
func (s *Service) notifyThreadWake(ctx context.Context, chat *db.Chat, threadID string, reason threadwake.Reason) error {
	if s.deps.Temporal == nil || threadID == "" {
		return nil
	}
	// Signal the chat's own workflow. A spawn is not a Temporal execution of
	// its own (dispatchSpawnBackground runs it as a goroutine inside the
	// parent), so every thread in the chat — main or spawned — is driven by
	// this one execution, and its tracker is where the notification must
	// land. Addressing the recipient thread inside the payload is what lets
	// the right gate wake.
	workflowID := chat.ID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}
	// Every caller is delivering a person's input, so the doorbell says so:
	// a thread waiting on the user's answer to a question yields to it. The
	// one exception is an async question's answer (the question service,
	// threadwake.WithoutQuestionYield): it answers a different question
	// than the one its thread may be parked on, which must stay open.
	if err := s.deps.Temporal.SignalWorkflow(ctx, workflowID, "", v2.ThreadWakeSignalName, v2.ThreadWakeSignal{
		Thread:   threadID,
		Reason:   reason,
		FromUser: threadwake.YieldsQuestions(ctx),
	}); err != nil {
		logging.WarnContext(ctx, "Could not wake thread",
			"error", err, "chatID", chat.ID, "threadID", threadID, "workflowID", workflowID, "reason", string(reason))
		return err
	}
	logging.InfoContext(ctx, "Woke thread",
		"chatID", chat.ID, "threadID", threadID, "workflowID", workflowID, "reason", string(reason))
	return nil
}

// startRunForQueuedRow starts a run for a row queued to the chat's main thread
// whose run had ended, or closed before the doorbell reached it. The new run's
// first call_llm drains the row.
//
// It is the run the closed one would have continued as had the doorbell
// arrived a moment sooner (late_wake.go): the same workflow at graph entry on
// the same thread, with the inputs the closed run was running on
// (inputsAfterClosedRun: presets plus the chat's stored selection). The queue
// carries no params of its own to build fresh inputs from.
//
// Ordering is what keeps the reconciler off the row. The closed run left the
// thread terminal, and a terminal thread with queued mail is exactly what
// resolveOrphanedAgentMessages marks undelivered; so the thread is revived
// BEFORE the run starts, and the sweep never sees the row as orphaned.
//
// If the run cannot be started, the row is withdrawn and the user told to send
// it as a message — the composer keeps the text on a refusal — rather than
// promised a delivery nothing will make.
func (s *Service) startRunForQueuedRow(ctx context.Context, chat *db.Chat, threadID, messageID string) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	workflowID := chat.MainThreadID()
	refuse := func(cause error) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
		logging.WarnContext(ctx, "Could not start a run for a message queued to a run that closed; withdrawing it",
			"error", cause, "chatID", chat.ID, "workflowID", workflowID)
		if _, err := s.deps.Repo.CancelQueuedAgentMessage(ctx, messageID, chat.ID); err != nil {
			logging.WarnContext(ctx, "Could not withdraw the queued message", "error", err, "chatID", chat.ID, "messageID", messageID)
		}
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
			Success: false,
			Message: "The agent finished just as this was queued, so there was no turn to deliver it into. Send it as a normal message instead.",
		}), nil
	}

	if chat.WorkflowName == nil || *chat.WorkflowName == "" {
		return refuse(fmt.Errorf("chat has no workflow"))
	}
	inputs, err := s.inputsAfterClosedRun(ctx, chat, *chat.WorkflowName)
	if err != nil {
		return refuse(err)
	}
	// This run answers a person, not whatever launched the chat.
	delete(inputs, v2.InputKeyLaunchRun)
	v2.DropActingIdentity(inputs)
	launch.InjectSessionDaemonID(inputs, chat)

	if _, err := s.deps.Repo.ReviveThread(ctx, threadID); err != nil {
		return refuse(err)
	}

	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chat.ID,
		WorkflowName: *chat.WorkflowName,
		Thread:       threadID,
		ThreadMode:   model.ThreadModeNew,
	}
	if jwt, ok := auth.GetUserJWT(chat.UserID); ok {
		execContext.UserJWT = jwt
	}
	run, err := s.deps.Temporal.ExecuteWorkflow(ctx, s.newRunOptions(workflowID), v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID:       chat.ID,
		WorkflowName: *chat.WorkflowName,
		Inputs:       inputs,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.deps.Repo, chat.ID),
		Resume:       resumeWholeChatInput(nil),
	})
	if err != nil {
		return refuse(err)
	}
	s.runs.RecordRun(ctx, chat.ID, workflowID, run.GetRunID())
	logging.InfoContext(ctx, "Started a run for a message queued as the previous one closed",
		"chatID", chat.ID, "workflowID", workflowID, "runID", run.GetRunID())
	return connect.NewResponse(&reliantv1.SendAgentMessageResponse{
		Success: true,
		Message: "The agent finished just as this was queued, so a new turn has started to read it.",
	}), nil
}

// newRunOptions is how every run SendMessage starts for an existing chat is
// started: under the chat's workflow ID, replacing any execution still holding
// it.
func (s *Service) newRunOptions(workflowID string) client.StartWorkflowOptions {
	return client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                workflow.SharedTaskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}
}

// wakeLiveRun rings the doorbell for input just saved to a run the caller
// found live, and reports whether that run has CLOSED instead — the one case
// where the caller must start a run for the input, because no turn of the old
// one will ever read it.
//
// A delivered signal needs nothing more: Temporal will not complete or
// continue-as-new the run without handing it the signal first, and the run
// gives a late wake a turn (late_wake.go). A failed one is only a closed run if
// Temporal says so. If the run is still open the failure was transient and the
// doorbell is rung once more; if Temporal cannot be asked, the answer is "not
// closed", because starting a second run over a live one would terminate it
// (WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING).
func (s *Service) wakeLiveRun(ctx context.Context, chat *db.Chat, threadID string, reason threadwake.Reason) (runClosed bool) {
	if s.notifyThreadWake(ctx, chat, threadID, reason) == nil || s.runs == nil {
		return false
	}
	workflowID := chat.ID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}
	state, err := s.runs.State(ctx, workflowID)
	if err != nil {
		logging.WarnContext(ctx, "Could not tell whether a run that refused its wake is still open; leaving the input for its next turn",
			"error", err, "chatID", chat.ID, "workflowID", workflowID)
		return false
	}
	if !state.Exists || !state.IsRunning {
		return true
	}
	_ = s.notifyThreadWake(ctx, chat, threadID, reason)
	return false
}
