// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// HistoryLimitRestartMessage is what the user is told when their chat exceeded
// Temporal's per-execution history limit and was restarted from its last
// checkpoint.
//
// It says what happened, that the conversation is intact, and what to expect —
// because the alternative (what happens today) is a chat that silently stops
// responding and a "send a message" that produces one reply and dies again.
const HistoryLimitRestartMessage = "Wow this is a long workflow! This conversation grew long enough to exceed our workflow engine's limit, so it was restarted from its last checkpoint. Your full message history is intact and the assistant continues from where it left off. Any pending or exeucting tasks might need to be redone. Any prior, completed work is safe."

// notifyHistoryLimitRestart tells the user their chat hit the engine's history
// limit and was restarted.
//
// Temporal TERMINATES a run that exceeds the limit — it does not fail in our
// code — so no error path of ours runs and nothing is emitted. Observed on a
// real chat: the run died at 51,201 events, the UI showed no reason, and the
// chat simply appeared frozen. This is the only place that can explain it.
//
// Best-effort: a failed notification must never block the restart that actually
// recovers the chat.
func (s *Service) notifyHistoryLimitRestart(ctx context.Context, chatID, workflowID, thread string) {
	errorID := uuid.New().String()
	errorData := map[string]interface{}{
		"update_type":   "error",
		"id":            errorID,
		"chat_id":       chatID,
		"activity_type": "history_limit_restart",
		"activity_id":   workflowID,
		"error_message": HistoryLimitRestartMessage,
		"error_summary": HistoryLimitRestartMessage,
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"workflow_id":   workflowID,
	}
	if thread != "" {
		errorData["thread"] = thread
	}

	payload, err := json.Marshal(errorData)
	if err != nil {
		logging.WarnContext(ctx, "Failed to marshal history-limit notice", "chatID", chatID, "error", err)
		return
	}
	if err := s.deps.Repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeError, errorID, string(payload)); err != nil {
		logging.WarnContext(ctx, "Failed to emit history-limit notice", "chatID", chatID, "error", err)
	}
}

// saveIncomingMessages atomically persists the system and user messages a send
// contributes to a thread. It deliberately does not inspect or drain the agent
// mailbox: call_llm is the sole mailbox deliverer, immediately before reading
// thread history, regardless of whether this send starts or resumes the run.
func (s *Service) saveIncomingMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	thread, workflowID string,
	systemMessages []*reliantv1.InputMessage,
	userContent string,
	hasUserContent bool,
) (string, error) {
	// A send the client named is saved once. When this message is already
	// there — in the transcript or queued in a mailbox — an earlier attempt of
	// this same send wrote it, along with the notes that went with it, and the
	// message the caller is owed is that one.
	clientID := canonicalClientMessageID(req.Msg.GetClientMessageId())
	hasMessage := hasUserContent || len(req.Msg.Attachments) > 0
	var savedMessageID string
	err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		savedMessageID = ""
		if clientID != "" && hasMessage {
			existingID, err := s.savedSendID(txCtx, req.Msg.ChatId, clientID)
			if err != nil {
				return err
			}
			if existingID != "" {
				savedMessageID = existingID
				return nil
			}
		}

		for _, sysMsg := range systemMessages {
			if _, err := s.deps.Repo.SaveMessageToThread(txCtx, req.Msg.ChatId, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle)); err != nil {
				return fmt.Errorf("failed to save system message: %w", err)
			}
		}

		if hasMessage {
			if clientID == "" {
				savedMsg, err := s.deps.Repo.SaveMessageToThread(txCtx, req.Msg.ChatId, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
				if err != nil {
					return fmt.Errorf("failed to save message: %w", err)
				}
				savedMessageID = savedMsg.ID
				return nil
			}
			// The id is the row's id, so of two repeats racing past the check
			// above exactly one inserts. The other rolls back the notes it
			// wrote and answers with the winner's message.
			savedMsg, created, err := s.deps.Repo.SaveMessageToThreadOnce(txCtx, req.Msg.ChatId, thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil, clientID)
			if err != nil {
				return fmt.Errorf("failed to save message: %w", err)
			}
			if !created {
				return errSendAlreadySaved
			}
			savedMessageID = savedMsg.ID
		}
		return nil
	})
	if errors.Is(err, errSendAlreadySaved) {
		return s.savedSendID(ctx, req.Msg.ChatId, clientID)
	}
	if err != nil {
		return "", err
	}
	return savedMessageID, nil
}

// liveRunStateUpdate builds the input update a send carries to the chat's run
// and validates it, returning a refusal for the caller to return as-is. Nil
// when the send carries no params and no changed presets.
//
// The update is exactly the keys the send sent (normalized), plus the params of
// a preset only when the send CHANGED the chat's presets (newPresets): a key the
// client did not send is unchanged to the run, not "fill from preset and schema
// default". It is validated as the run would hold it afterwards — the chat's
// presets, its selection and the update over them.
//
// Every caller runs it BEFORE the send writes anything, because a refused send
// has to be one that did not happen. Prod chat 66a045ce (2026-10-10) answered
// "continue" on a paused run and was refused for its model only after the
// message was saved: the transcript gained a turn no run would read, the
// client was told the send failed, and the retry saved the message a second
// time. #685 stopped refusing that model (one no connected provider can serve
// now moves to one that can), but what is still refused — an unknown model, a
// malformed selector, a user with no provider, a missing required input — was
// refused the same way, after the write, on every path.
//
// The send-time model fallback (fallBackFromUnservableModels) runs after the
// write, not here: it posts a notice, and validation does not depend on it —
// a model it would move is not a validation error.
//
// chat.SelectedPresets must already hold this send's presets (see
// applyRequestPresets), so the update is validated against the presets the run
// will actually get.
func (s *Service) liveRunStateUpdate(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName string,
	req *connect.Request[reliantv1.SendMessageRequest],
	newPresets map[string]string,
) (map[string]interface{}, error) {
	if len(req.Msg.WorkflowParams) == 0 && len(newPresets) == 0 {
		return nil, nil
	}
	stateUpdate := s.launcher().BuildStateUpdateForActiveWorkflow(ctx, userID, chat, workflowName, newPresets, req.Msg.WorkflowParams)
	if validationErrors := s.launcher().ValidateStateUpdate(ctx, userID, chat, workflowName, chat.SelectedPresets, stateUpdate); len(validationErrors) > 0 {
		return nil, inputValidationError(validationErrors)
	}
	return stateUpdate, nil
}

// inputValidationError is SendMessage's refusal for inputs that failed
// validation: InvalidArgument, naming every failure.
func inputValidationError(validationErrors []error) error {
	errMsgs := make([]string, len(validationErrors))
	for i, e := range validationErrors {
		errMsgs[i] = e.Error()
	}
	return svcerr.InvalidArgument(fmt.Sprintf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
}

// applyRequestPresets makes a send's preset selection the chat's, in memory
// only, and reports whether it did. The caller persists it with
// persistChatPresets once the send has passed validation.
func applyRequestPresets(chat *db.Chat, requestPresets map[string]string) bool {
	if len(requestPresets) == 0 {
		return false
	}
	chat.SelectedPresets = requestPresets
	return true
}

// persistChatPresets writes the chat's preset selection. Best-effort: the
// presets also travel in the run's inputs, so a failed write costs only the
// selection shown next time.
func (s *Service) persistChatPresets(ctx context.Context, chat *db.Chat) {
	chat.UpdatedAt = time.Now().UTC()
	if err := s.deps.Repo.UpdateChat(ctx, chat); err != nil {
		logging.ErrorContext(ctx, "Failed to update chat presets", "error", err, "chatID", chat.ID)
	}
}

// SendMessage sends messages to a chat and continues workflow
func (s *Service) SendMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
) (*connect.Response[reliantv1.SendMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Extract user and system messages from input
	userContent, systemMessages, hasUserContent := extractMessagesFromInput(req.Msg.Messages)

	// Require at least one user message or attachments
	if !hasUserContent && len(req.Msg.Attachments) == 0 {
		return nil, svcerr.InvalidArgument("at least one user message or attachment is required")
	}

	clientID, err := parseClientMessageID(req.Msg.GetClientMessageId())
	if err != nil {
		return nil, err
	}

	// Get chat with ownership check (defense-in-depth via single query)
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// A repeat of a send whose message has already been read is answered
	// before anything else happens. The machine wake and the queued-for-machine
	// bookkeeping below are about THIS send reaching a run, and a repeat of a
	// finished one must not disturb state that belongs to a later send. Safe
	// without the run-control lock because "read" never un-happens; every
	// other outcome is decided under the lock, below.
	if early, err := s.findPriorSend(ctx, chat, clientID, userContent, req.Msg.Attachments); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check for an earlier send"))
	} else if early != nil && early.settled {
		return replayResponse(chat, early), nil
	}

	s.wakeDaemonForAttendedTurn(ctx, userID, chat)

	if chat.Activity != nil && reliantv1.ChatActivity(*chat.Activity) == reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON {
		// A live run is waiting for the machine this send just woke. Its
		// marker still describes it — clearing it would show the run as
		// thinking until its next check, which can be minutes away — so ask
		// the run to check now instead: it clears the marker itself once the
		// machine is up. A run held mid-turn does not listen, and its next
		// tool call settles the marker the same way.
		s.nudgeMachineWait(ctx, chat)
	} else if err := s.deps.Repo.SetChatDaemonBlocked(ctx, chat.ID, false); err != nil {
		// An attended send wakes the machine, so a daemon-pending marker left
		// by an earlier run no longer describes it. The next tool call re-sets
		// it if the machine is still unreachable.
		logging.WarnContext(ctx, "Failed to clear daemon-pending marker on send", "error", err, "chatID", chat.ID)
	}
	// A message queued for the machine is read by whichever run this send
	// starts, resumes or wakes — the run reads the thread, where it already
	// is — so it is no longer waiting on the machine to be delivered. If that
	// run's machine is still down, its preflight queues both again.
	// The user acting also ends delivery's attempt count and any stop: what
	// failed before says nothing about this send.
	if _, err := s.deps.Repo.SettleQueuedDelivery(ctx, chat.ID); err != nil {
		logging.WarnContext(ctx, "Failed to clear queued-for-machine marker on send", "error", err, "chatID", chat.ID)
	}

	// Note: Previously checked if workflow completed and thread is closed.
	// Removed to allow restarting workflows - SendMessage will start a new workflow
	// for completed/failed/cancelled workflows (see status switch below).

	if err := launch.ValidateWorkflowParamStructure(req.Msg.WorkflowParams); err != nil {
		return nil, err
	}

	// Resume-at-position state, populated when the prior run for this chat was
	// interrupted (failed/terminated/lost) rather than completed or
	// user-cancelled. Decision table for the new-run start below:
	//   failed / terminated / wedged  -> reset-and-replay (precise) when Temporal
	//                                     has replayable history; else coarse
	//                                     resume at checkpointed position
	//   completed                     -> fresh start
	//   user-cancelled                -> fresh start (thread history only)
	var resumeInput *v2.ResumeInput
	var resumeThread string
	// Set when a resume branch already persisted the incoming messages (e.g. the
	// reset-and-replay path saves them before reset, then falls back to coarse
	// restart) so the new-run flow below does not double-save them.
	var resumeMessagesSaved bool
	var resumePresavedMessageID string
	// Set when the run this message was saved to closed before it could be
	// woken: the fresh run started for the message serves the thread it was
	// saved to.
	var lateRunThread string

	// The status read below decides between resuming a paused run and waking a
	// running one, and that decision is only right if no pause or resume is
	// half-applied while it is made and delivered. PauseChat signals Temporal
	// before it writes the paused status; a send that read the row in that gap
	// saw "running", only rang the thread-wake doorbell, and left the run
	// paused with the message unread (chat 264b5697, stuck 4.5 minutes). So
	// the read and everything it routes to run inside the run-control critical
	// section. See runs.Service.LockRunControl.
	releaseRunControl := s.runs.LockRunControl(ctx, req.Msg.ChatId)
	defer releaseRunControl()

	// Decided under the lock, so an attempt of this same send that is still
	// finishing has committed by now. The chat is read again for the same
	// reason: that attempt may have started the run.
	prior, replay, err := s.priorSendOrReplay(ctx, chat, clientID, userContent, req.Msg.Attachments)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return replay, nil
	}
	if prior != nil {
		if fresh, err := s.getChatForUser(ctx, req.Msg.ChatId, userID); err == nil {
			chat = fresh
		}
	}

	// Check workflow status to decide: resume paused, send to running, or start
	// new. The transaction below only reads; what keeps the decision from going
	// stale is the run-control lock held above, not the transaction.
	if workflowID := chat.MainThreadID(); workflowID != "" {
		var existingWorkflow *db.Workflow

		err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
			wf, err := s.deps.Repo.GetWorkflow(txCtx, workflowID)
			if err != nil {
				return err
			}
			existingWorkflow = wf
			return nil
		})

		if err == nil && existingWorkflow != nil {
			// Use Temporal as source of truth for workflow status
			// For running workflows, verify Temporal agrees
			if existingWorkflow.Status == db.Active() {
				temporalState, temporalErr := s.runs.State(ctx, workflowID)
				if temporalErr != nil {
					// Temporal query failed - this is an error we should surface
					return nil, svcerr.WrapOr(temporalErr, svcerr.Unavailable("failed to check workflow status"))
				}

				if !temporalState.Exists {
					// Ghost workflow detected - Temporal lost the workflow but DB says running
					// RESURRECT: Start a fresh Temporal execution with the same workflow ID
					// This allows the conversation to continue seamlessly
					logging.InfoContext(ctx, "[Ghost Recovery] Resurrecting ghost workflow",
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
						"dbStatus", existingWorkflow.Status,
						"workflowName", existingWorkflow.WorkflowName,
					)

					return s.resurrectGhostWorkflow(ctx, req, chat, existingWorkflow, workflowID, userID)
				} else if !temporalState.IsRunning {
					if existingWorkflow.Status == db.Paused() {
						// Paused workflow whose Temporal execution timed out or completed.
						// Keep status as paused — the paused handler uses PauseService.ResumeWorkflow
						// which handles reset-based resume for expired executions.
						logging.InfoContext(ctx, "Paused workflow Temporal execution not running - will attempt reset-based resume",
							"workflowID", workflowID,
							"chatID", req.Msg.ChatId,
							"temporalStatus", temporalState.Status,
						)
					} else {
						// Running workflow that stopped — reconcile status
						s.runs.Reconcile(ctx, workflowID, existingWorkflow.Status, temporalState.Status)
						existingWorkflow.Status = temporalState.Status
					}
				}
			}

			switch existingWorkflow.Status {
			case db.Pending():
				// Never started (a branch awaiting its first send). The first
				// send is StartChat's, which records the launch exactly once;
				// letting SendMessage start it would bypass that.
				return nil, svcerr.WithReason(svcerr.FailedPrecondition("chat has not started; call StartChat"), errreason.ChatNotStarted)

			case db.Paused():
				// Refuse bad params before anything is written; see
				// liveRunStateUpdate.
				newPresets := changedPresets(chat.SelectedPresets, req.Msg.SelectedPresets)
				presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)
				stateUpdate, err := s.liveRunStateUpdate(ctx, userID, chat, existingWorkflow.WorkflowName, req, newPresets)
				if err != nil {
					return nil, err
				}
				if presetsChanged {
					s.persistChatPresets(ctx, chat)
				}

				// Determine target thread - use workflow's thread from DB (not root workflow ID)
				// to handle forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save the messages in one transaction. The mailbox is not
				// touched: a paused run delivers anything queued for the thread
				// at its next call_llm, once it resumes.
				//
				// The workflow-status flip is deliberately NOT in here.
				// Postgres and Temporal cannot commit together, so the only
				// question is which order fails safely, and "mark running, then
				// signal" is the unsafe one: if the DB write fails, this
				// returned an error before ever reaching ResumeWorkflow below
				// and the run stayed parked with nobody coming for it — visibly
				// "active" while actually halted, and invisible to the
				// reconciler, whose progress watchdog skips paused rows by
				// design. That is exactly what a serialization failure did to
				// chat 80978aca: paused 20:52, resume aborted 20:55, stuck
				// until a later message happened to retry it at 21:29.
				//
				// Signalling first inverts the failure: the run really is
				// awake, and the status is corrected by whichever of three
				// writers gets there — the best-effort write below, the
				// workflow's own "started"/Resumed notification when it wakes
				// (see the retry-exhaustion paths in loop_executor.go and
				// inline_workflow_executor.go), or the reconciler. A stale
				// "paused" row on a running workflow is self-healing; a
				// never-signalled workflow is not.
				var savedMessageID string
				err = s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
					var saveErr error
					savedMessageID, saveErr = s.saveIncomingMessages(txCtx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
					return saveErr
				})
				if err != nil {
					// The message itself failed to save — there is nothing to
					// resume the workflow FOR, so this really is fatal.
					return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save messages"))
				}

				// Get RunID from chat (workflow struct doesn't have it)
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				outcome, resumeErr := s.resumePausedRun(ctx, userID, chat, existingWorkflow.WorkflowName, targetThread, stateUpdate, req.Msg.WorkflowParams)
				if resumeErr != nil {
					// The resume is the step that actually restarts the run.
					// Swallowing this failure and returning
					// workflow_status=running below is what makes a chat look
					// active while it is halted: the message is saved, the UI
					// shows work in progress, and nothing is executing. Report
					// it instead — the user's message is already durable, so a
					// retry resumes from exactly here.
					return nil, svcerr.WrapOr(resumeErr, svcerr.Internal("failed to resume paused workflow"))
				}
				if outcome.Kind != runs.OutcomeResumed {
					logging.WarnContext(ctx, "Paused workflow could not be resumed in place during SendMessage - resuming at position with a new run",
						"workflowID", workflowID, "chatID", req.Msg.ChatId)
					// The paused run was lost (infra failure, not user
					// intent) — start a new run that resumes at position.
					resumeInput = v2.ResumeInputFromDurableState(ctx, s.deps.Repo, req.Msg.ChatId, workflowID)
					resumeThread = existingWorkflow.Thread
					// Fall through to start a new workflow below
					break
				}

				if outcome.RunID != "" {
					runID = outcome.RunID
				}

				// Wake the target thread, for the same reason the running
				// branch does. Resuming is NOT sufficient on its own:
				// broadcastResume clears the pause gate, but a thread parked
				// in awaitLiveDetachedSpawns is blocked on a different Await
				// whose predicate never looks at the pause epoch. That is
				// exactly what happened on chat 7da3935c — the 21:37:46
				// resume woke the spawn's loop and left the root thread
				// parked with the user's message unread.
				_ = s.notifyThreadWake(ctx, chat, targetThread, threadwake.ReasonUserMessage)

				// Return with updated workflow_status so frontend knows we resumed
				workflowStatus := fmt.Sprintf("%d", db.Active())
				go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      savedMessageID,
				}), nil

			case db.Active():
				// The earlier attempt of this send wrote the message to the
				// transcript and the run it started or resumed is the one that
				// is live now. It has the message; queueing it again would
				// hand the agent the same words twice.
				if prior != nil && prior.history != nil {
					return replayResponse(chat, prior), nil
				}

				// Refuse bad params before anything is written or queued; see
				// liveRunStateUpdate.
				newPresets := changedPresets(chat.SelectedPresets, req.Msg.SelectedPresets)
				presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)
				stateUpdate, err := s.liveRunStateUpdate(ctx, userID, chat, existingWorkflow.WorkflowName, req, newPresets)
				if err != nil {
					return nil, err
				}
				if presetsChanged {
					s.persistChatPresets(ctx, chat)
				}

				// Use workflow's thread from DB (not root workflow ID) to handle
				// forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save system messages first
				for _, sysMsg := range systemMessages {
					_, err := s.deps.Repo.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
					if err != nil {
						return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save system message"))
					}
				}

				// Queue the user message for the thread's next turn rather than
				// writing it into history now. A running thread may be mid-turn,
				// and its reply is saved only when its stream ends: a message
				// written meanwhile takes the earlier seq and sits BEFORE the
				// reply that never saw it. History then ends with the assistant,
				// and the turn the wake buys yields instead of answering — the
				// message is in the transcript and never answered. The mailbox is
				// the one delivery point that lands input at a turn boundary, in
				// order (call_llm drains it immediately before it reads history),
				// so a message to a running thread goes there, exactly as the
				// composer's queue does.
				var queuedMsg *db.AgentMessage
				if hasUserContent || len(req.Msg.Attachments) > 0 {
					queuedMsg = &db.AgentMessage{
						// The client's own id for the message, so the copy it
						// is already showing and the queued row are one item.
						ID:           queuedMessageID(clientID),
						FromThreadID: chat.MainThreadID(),
						ChatID:       req.Msg.ChatId,
						ToThreadID:   targetThread,
						Kind:         core.AgentMessageKindHumanMessage,
						Body:         userContent,
						Attachments:  req.Msg.Attachments,
						Status:       core.AgentMessageStatusQueued,
						CreatedAt:    time.Now(),
					}
					// Queued once: an attempt of this send that already queued
					// the message left its row, and this one finishes around it
					// (the wake below is what the first may not have reached).
					queued, _, err := s.enqueueHumanMessage(ctx, queuedMsg, clientID != "")
					if err != nil {
						return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save message"))
					}
					queuedMsg = queued
				}

				// Signal workflow with the param/preset input update validated
				// above, after the send-time model policy (#685) has moved any
				// model no connected provider can serve.
				if len(stateUpdate) > 0 {
					modelMoves := s.fallBackFromUnservableModels(ctx, userID, req.Msg.ChatId, chat.ProjectID, existingWorkflow.WorkflowName, workflowID, targetThread, stateUpdate, func(input string) bool {
						return s.checkParamsActuallyChanged(ctx, workflowID, map[string]interface{}{input: stateUpdate[input]})
					})
					s.refreshLiveChatProvider(ctx, userID, chat, existingWorkflow.WorkflowName, stateUpdate)
					_, _ = s.recordSelection(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.WorkflowParams, stateUpdate, modelMoves, 0)

					// Only add "params changed" message if params actually changed from current workflow state
					if s.checkParamsActuallyChanged(ctx, workflowID, stateUpdate) {
						hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
						_, err := s.deps.Repo.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
						if err != nil {
							logging.ErrorContext(ctx, "Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
							// Non-fatal, continue to signal
						}
					}

					// The current run (empty run id), not the chat row's: that
					// one is stale after a continue-as-new.
					if err := s.deps.Temporal.SignalWorkflow(ctx, workflowID, "", "update_workflow_state", stateUpdate); err != nil {
						logging.WarnContext(ctx, "Failed to signal workflow with param updates", "error", err, "workflowID", workflowID)
					}
				}

				// Get RunID from chat
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				// Wake the target thread. The wake is the run's only record
				// that this message exists, because a running workflow is not
				// necessarily a turn that will read it:
				//   - a thread that has fanned work out to background spawns
				//     is parked in awaitLiveDetachedSpawns, and takes no turn
				//     until something wakes it (chat 7da3935c: the root thread
				//     ignored the user while its one live spawn ran);
				//   - a thread on its LAST turn has already checked its
				//     mailbox, and without the wake would end the run with
				//     this message queued. The loop-exit gate and the
				//     end-of-run check both read the wake and give it a turn
				//     (late_wake.go).
				//
				// And the run may have closed since the status read above. A
				// wake that reaches no run starts one for the message — so it
				// is never left for the user's next send to discover.
				if queuedMsg != nil && s.wakeLiveRun(ctx, chat, targetThread, threadwake.ReasonMailbox) {
					logging.InfoContext(ctx, "Run finished before a message to it could wake it; starting a run for the message",
						"chatID", req.Msg.ChatId, "workflowID", workflowID, "threadID", targetThread)
					resumeMessagesSaved = true
					resumePresavedMessageID = queuedMsg.ID
					lateRunThread = targetThread
					break
				}

				workflowStatus := fmt.Sprintf("%d", db.Active())
				// The queued row's ID: the message enters history when the
				// thread's next turn drains it.
				var messageID string
				if queuedMsg != nil {
					messageID = queuedMsg.ID
				}
				go s.trackMessageSent(ctx, userID, chat, messageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      messageID,
					Queued:         queuedMsg != nil,
				}), nil

			// The EXPIRED resume branch that used to sit here is gone with the
			// status it matched: nothing ever wrote EXPIRED (Temporal
			// TIMED_OUT is recorded as a failure), so this arm was
			// unreachable. A timed-out run now takes the FAILED arm below,
			// which is where it was already being routed in practice.

			case db.Failed():
				// An earlier attempt of this very send already restarted the
				// run: the row says failed until that run's first activity
				// writes "started", and Inspect below would call the live run
				// "stuck" and refuse the repeat.
				if s.runStartedForPriorSend(ctx, prior, workflowID) {
					return replayResponse(chat, prior), nil
				}

				// Stuck (database says failed while Temporal says running)
				// cannot be restarted - user must branch to continue.
				inspection, inspectErr := s.runs.Inspect(ctx, req.Msg.ChatId)
				if inspectErr != nil {
					return nil, svcerr.WrapOr(inspectErr, svcerr.Unavailable("failed to check workflow status"))
				}
				if inspection.Stuck {
					return nil, svcerr.FailedPrecondition("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message")
				}

				// Both recoveries below save the message before they know
				// whether they can serve it, so bad params are refused here,
				// first; see liveRunStateUpdate. The run they restart gets the
				// same inputs, so the same check applies (in memory only: the
				// new-run path below persists the presets once it starts).
				newPresets := changedPresets(chat.SelectedPresets, req.Msg.SelectedPresets)
				applyRequestPresets(chat, req.Msg.SelectedPresets)
				stateUpdate, err := s.liveRunStateUpdate(ctx, userID, chat, activeWorkflowNameForResume(chat, existingWorkflow), req, newPresets)
				if err != nil {
					return nil, err
				}

				// What a reset-and-replay hands the replayed run, before it
				// wakes. Replay rebuilds the run from the inputs its history
				// recorded, so without this a model the user just picked —
				// or a pin no connected provider can serve, which the send
				// moves (resumeInputUpdate) — never reaches it. The coarse
				// restart below builds its own inputs from this send.
				var inputUpdate map[string]interface{}
				var modelNotices, modelMoves []launch.ModelSubstitution
				if inspection.Recoverable {
					// The failed run is closed, so it is never queried for its
					// inputs (Temporal would replay its whole history to
					// answer — rtemporal.QueryOpenRun). What it was running on
					// is rebuilt from the chat's presets and stored selection.
					recorded := s.inputsAfterClosedRun(ctx, chat, activeWorkflowNameForResume(chat, existingWorkflow))
					inputUpdate, modelNotices, modelMoves = s.resumeInputUpdate(ctx, userID, req.Msg.ChatId, chat.ProjectID, existingWorkflow.WorkflowName, workflowID,
						s.underSelection(ctx, chat.ID, activeWorkflowNameForResume(chat, existingWorkflow), stateUpdate), recorded)
					inputUpdate = s.withProviderPolicy(ctx, userID, chat, recorded, inputUpdate)
					inputUpdate = s.refreshLiveChatProvider(ctx, userID, chat, existingWorkflow.WorkflowName, inputUpdate)
				}

				// A run that died parked on an unanswered ask_question wakes on
				// signal.question.<id>, not signal.resume. Deliver the user's plain
				// message as the (marked) question answer via reset-and-replay so it
				// resumes PRECISELY (the rebuilt run re-parks on the same question
				// channel and receives it) instead of coarse-restarting the loop.
				//
				// Only the question of the thread the message is FOR: in a chat
				// with several agents each can be parked on its own question,
				// and a message to the main thread is no answer to a spawn's.
				// A run whose question is on another thread takes the plain
				// reset-and-replay below, which re-parks that thread on its
				// still-pending question for the user to answer there.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}
				var pendingQuestion *db.Question
				if inspection.Recoverable {
					if byThread, err := s.deps.Repo.PendingQuestionsByThread(ctx, req.Msg.ChatId); err == nil {
						pendingQuestion = byThread[targetThread]
					}
				}
				if pendingQuestion != nil && pendingQuestion.TemporalWorkflowID != "" && hasUserContent {
					resp, resumed, presavedID := s.resumeFailedQuestionWorkflow(ctx, req, chat, existingWorkflow, pendingQuestion, targetThread, userID, userContent, systemMessages, inputUpdate)
					if resumed {
						s.postModelFallbackNotices(ctx, req.Msg.ChatId, workflowID, targetThread, modelNotices)
						_, _ = s.recordSelection(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.WorkflowParams, inputUpdate, modelMoves, 0)
						return resp, nil
					}
					// Guard-exhausted / not reset-resumable: the question is already
					// resolved and messages saved — fall through to coarse restart.
					resumeMessagesSaved = true
					resumePresavedMessageID = presavedID
					resumeInput = v2.ResumeInputFromDurableState(ctx, s.deps.Repo, req.Msg.ChatId, workflowID)
					resumeThread = existingWorkflow.Thread
					break
				}

				// Failed/terminated/wedged run with a CLOSED Temporal execution
				// that still has replayable history: prefer RESET-AND-REPLAY. The
				// new run replays the recorded history, which rebuilds the entire
				// (possibly nested) engine stack — so a run that died
				// mid-nested-get-it-right resumes at the SAME review iteration
				// with reviewer feedback intact, instead of the coarse
				// flat-checkpoint restart that can only re-enter a TOP-LEVEL node
				// and restarts the nested loop at iteration 0. We fall back to the
				// coarse restart only when Temporal has nothing to replay (ghost),
				// the bounded guard has given up (deterministic failure), or the run
				// was parked on an unanswered question (handled above).
				if inspection.Recoverable && pendingQuestion == nil {
					targetThread := existingWorkflow.Thread
					if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
						targetThread = *req.Msg.TargetThread
					}
					// Persist messages BEFORE reset so the resumed run reads
					// them, mailbox first: the interrupted run never reached
					// another drain boundary, so whatever was queued for this
					// thread still belongs ahead of the new message.
					presavedID, saveErr := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
					if saveErr != nil {
						return nil, svcerr.WrapOr(saveErr, svcerr.Internal("failed to save messages"))
					}
					resumeMessagesSaved = true
					resumePresavedMessageID = presavedID

					outcome, resumeErr := s.runs.ResumeInterrupted(ctx, req.Msg.ChatId, inputUpdate)
					if resumeErr != nil {
						return nil, svcerr.WrapOr(resumeErr, svcerr.Internal("failed to resume the interrupted run"))
					}
					if outcome.Kind == runs.OutcomeResumed {
						s.postModelFallbackNotices(ctx, req.Msg.ChatId, workflowID, targetThread, modelNotices)
						_, _ = s.recordSelection(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.WorkflowParams, inputUpdate, modelMoves, 0)
						workflowStatus := fmt.Sprintf("%d", db.Active())
						go s.trackMessageSent(ctx, userID, chat, presavedID, targetThread, userContent, len(req.Msg.Attachments))
						return connect.NewResponse(&reliantv1.SendMessageResponse{
							ChatId:         req.Msg.ChatId,
							WorkflowId:     workflowID,
							RunId:          outcome.RunID,
							Status:         "processing",
							WorkflowStatus: &workflowStatus,
							MessageId:      presavedID,
						}), nil
					}
					if outcome.HistoryLimitExceeded {
						// Resetting cannot help a run at the history cap, so
						// this goes to the coarse fresh restart — and the user
						// is told, because otherwise the chat simply stops with
						// no explanation and "send a message" appears to do
						// nothing.
						s.notifyHistoryLimitRestart(ctx, req.Msg.ChatId, workflowID, targetThread)
					}
					// Messages already saved; fall through to the coarse restart.
				}

				// Coarse resume-at-position: the new run enters directly at the
				// checkpointed node instead of graph entry, so entry routers never
				// re-classify the user's "continue" message. Used when there is no
				// replayable history (ghost) or the reset guard gave up.
				resumeInput = v2.ResumeInputFromDurableState(ctx, s.deps.Repo, req.Msg.ChatId, workflowID)
				resumeThread = existingWorkflow.Thread
				logging.InfoContext(ctx, "Interrupted workflow detected - new run will resume at position",
					"chatID", req.Msg.ChatId,
					"workflowID", workflowID,
					"checkpointNode", resumeInput.NodeID,
					"loopIteration", resumeInput.LoopIteration,
				)

			case db.Cancelled(), db.Completed():
				// User-cancelled or completed: start a fresh workflow at graph
				// entry (thread history is still the conversation context).
				// Fall through to normal flow.
				//
				// Unless an earlier attempt of this very send already started
				// it: the row says "completed" until the new run's first
				// activity writes "started", so a repeat that lands in that
				// window would start a second run over the first (the start
				// terminates whatever holds the workflow id).
				if s.runStartedForPriorSend(ctx, prior, workflowID) {
					return replayResponse(chat, prior), nil
				}
			}
		}
	}

	// Workflow name is always set on chat (required at creation)
	workflowName := *chat.WorkflowName

	// A chat with no root workflow has never started; that first send is
	// StartChat's.
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition("chat has not started; call StartChat"), errreason.ChatNotStarted)
	}

	// A started chat's workflow is fixed. Switching is only possible while a
	// chat is pending, and StartChat owns that.
	if req.Msg.Workflow != nil && *req.Msg.Workflow != "" && *req.Msg.Workflow != workflowName {
		return nil, svcerr.FailedPrecondition("cannot change workflow after chat has started - use Branch to create a new chat with a different workflow")
	}

	// The request's presets replace the chat's. Applied in memory here, and
	// persisted only once the inputs below have passed validation.
	presetsChanged := applyRequestPresets(chat, req.Msg.SelectedPresets)

	workflowOptions := s.newRunOptions(workflowID)

	// Merge presets: chat presets are base, request presets override
	effectivePresets := make(map[string]string)
	for k, v := range chat.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}
	for k, v := range req.Msg.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}

	// Build workflow inputs: presets, then the chat's stored selection, then
	// this send's params. A send that carries no params still runs the model
	// and mode the chat was set to, from any device.
	initialData := s.launcher().BuildChatWorkflowInputs(ctx, userID, chat, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Validate workflow inputs before starting — and before the messages are
	// saved below. This catches missing required inputs, unknown models and a
	// user with no provider early (400), and a refused send must leave nothing
	// behind; see liveRunStateUpdate.
	if validationErrors := s.launcher().ValidateWorkflowInputs(ctx, userID, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		return nil, inputValidationError(validationErrors)
	}
	if presetsChanged {
		s.persistChatPresets(ctx, chat)
	}

	// Determine target thread. Resume runs continue the interrupted run's
	// thread (which may be a forked/child thread) so history stays continuous.
	targetThread := workflowID
	if lateRunThread != "" {
		targetThread = lateRunThread
	}
	threadMode := model.ThreadModeNew
	if resumeInput != nil {
		threadMode = model.ThreadModeInherit
		if resumeThread != "" {
			targetThread = resumeThread
		}
	}
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// Build execution context for the workflow
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Thread:       targetThread,
		ThreadMode:   threadMode,
	}
	if jwt, ok := auth.GetUserJWT(userID); ok {
		execContext.UserJWT = jwt
	}

	// Save messages BEFORE starting workflow for consistency: system messages,
	// then the new user message. Anything still queued in this thread's
	// mailbox is not touched here — the new run's first call_llm delivers it,
	// immediately before it reads history.
	//
	// A resume branch (reset-and-replay fallback) may have already persisted the
	// messages before its reset attempt, and so has a send whose run closed
	// before it could be woken (lateRunThread) — skip re-saving so they aren't
	// doubled.
	var savedMessageID string
	var greenfieldProbe bool
	if resumeMessagesSaved {
		savedMessageID = resumePresavedMessageID
	} else {
		// A chat whose first turn this still is may be opening on a directory
		// with no code, where the stack is undecided. The run finds out and
		// seeds the guidance before its first LLM call; this only decides it
		// is a first turn, and must do so before the message below is saved.
		// A resume continues a conversation, so it never is one.
		greenfieldProbe = resumeInput == nil && s.launcher().WantsGreenfieldProbe(ctx, chat)

		saved, err := s.saveIncomingMessages(ctx, req, targetThread, workflowID, systemMessages, userContent, hasUserContent)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save messages"))
		}
		savedMessageID = saved
	}

	// A move is announced once: the chat's selection records the moved model,
	// so a composer still sending the original pin does not repeat the notice.
	modelMoves := s.fallBackFromUnservableModels(ctx, userID, req.Msg.ChatId, chat.ProjectID, workflowName, workflowID, targetThread, initialData,
		s.selectionStoredModelIsCurrent(ctx, req.Msg.ChatId, initialData))
	s.refreshChatProvider(ctx, userID, chat, workflowName, initialData)
	_, _ = s.recordSelection(ctx, userID, chat, workflowName, req.Msg.WorkflowParams, initialData, modelMoves, 0)

	// Inject session daemon if set on chat
	s.chatPinner().Pin(ctx, chat)
	launch.InjectSessionDaemonID(initialData, chat)

	// A message queued to a run that closed before its wake is still in the
	// mailbox, and the closed run left the thread terminal — the shape the
	// reconciler's orphaned-mailbox sweep marks undelivered. Revive the thread
	// BEFORE the run starts, so the sweep can never take the row first; the
	// run's first call_llm delivers it.
	if lateRunThread != "" {
		if _, err := s.deps.Repo.ReviveThread(ctx, lateRunThread); err != nil {
			logging.WarnContext(ctx, "Could not revive the thread a late message was queued to; starting its run anyway",
				"error", err, "chatID", req.Msg.ChatId, "threadID", lateRunThread)
		}
	}

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Trigger:      launch.LoadChatTrigger(ctx, s.deps.Repo, req.Msg.ChatId),
		Resume:       resumeWholeChatInput(resumeInput),

		GreenfieldProbe: greenfieldProbe,
	}

	workflowRun, err := s.deps.Temporal.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start workflow"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)

	workflowStatus := fmt.Sprintf("%d", db.Active())
	go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		RunId:          runID,
		Status:         "processing",
		WorkflowStatus: &workflowStatus,
		MessageId:      savedMessageID,
		// A run started for a message queued to a run that closed first: the
		// row is still queued, and this run's first turn drains it.
		Queued: lateRunThread != "",
	}), nil
}
