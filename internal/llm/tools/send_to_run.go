// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type SendToRunParams struct {
	RunID   string `json:"run_id" jsonschema:"required,description=The run id (chat id), from start_run or list_runs."`
	Message string `json:"message" jsonschema:"required,description=The message to deliver to the run."`
	Thread  string `json:"thread,omitempty" jsonschema:"description=Optional. Which agent of the run the message is for: a thread id of the run, or the node name of an agent running in it (e.g. implement). Omit it to reach the run's one running agent; when several run in parallel the call is refused with the list, so name one."`
	Subject string `json:"subject,omitempty" jsonschema:"description=Optional one-line summary of the message (at most 100 characters). The target chat shows the message collapsed to it; without one it shows the message's first line."`
}

const (
	SendToRunToolName    = "send_to_run"
	sendToRunDescription = `Send a message to another top-level run that is still going, as if the user had typed it into that chat.

ONLY LIVE RUNS: a run that is running or paused. If the run has finished, or has not started yet, this delivers nothing and says so (delivered: false) — it never starts or restarts a run. Use start_run for new work.

A PAUSED run is resumed by the message, exactly as if the user had typed into the paused chat. If you do not want that, do not send it.

THE RECEIPT IS HONEST: delivered means the message was saved to the run's thread and the run was nudged to look. It does NOT mean the run has read it or acted on it. Check with get_run.

WHO READS IT: the message goes to the agent of the run that is running now (an agent chat's own agent, or a workflow's one running agent step). If several agents of the run are running in parallel, say which with thread (a thread id, or the node name of the agent, as get_run or the refusal lists them); the run never guesses. If no agent is running in it right now (a workflow between its agent steps), nothing is delivered and it says so.

You cannot message the run you are executing in, and you can only message runs the user owns. To message a sub-agent you spawned, use spawn_send.`
)

type sendToRunTool struct {
	repo      runCallerStore
	messenger RunMessenger
}

func NewSendToRunTool(repo runCallerStore, messenger RunMessenger) Tool {
	return NewToolWrapper[SendToRunParams, ToolResponse](&sendToRunTool{repo: repo, messenger: messenger})
}

func (s *sendToRunTool) Name() string        { return SendToRunToolName }
func (s *sendToRunTool) Description() string { return sendToRunDescription }

func (s *sendToRunTool) RequiresPermission(params SendToRunParams) (bool, error) {
	return false, nil
}

// SendToRunResponseMetadata is the machine-readable half of send_to_run.
type SendToRunResponseMetadata struct {
	RunID     string `json:"run_id"`
	Delivered bool   `json:"delivered"`
	State     string `json:"state"`
	MessageID string `json:"message_id,omitempty"`
}

func (s *sendToRunTool) Execute(rctx *rctx.ToolContext, params SendToRunParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, s.repo)
	if errResp != nil {
		return *errResp, nil
	}
	if strings.TrimSpace(params.Message) == "" {
		return NewTextErrorResponse("message is required"), nil
	}
	subject := ""
	if strings.TrimSpace(params.Subject) != "" {
		var problem string
		if subject, problem = normalizeSpawnSendSubject(params.Subject); problem != "" {
			return NewTextErrorResponse(problem), nil
		}
	}
	if errResp := rejectSelfTarget(rctx, params.RunID, "message"); errResp != nil {
		return *errResp, nil
	}
	chat, errResp := resolveOwnedRun(rctx, s.repo, caller, params.RunID)
	if errResp != nil {
		return *errResp, nil
	}
	if s.messenger == nil {
		return NewTextErrorResponse("Messaging a run is not available in this runtime."), nil
	}

	state := runStateLabel(chat)
	meta := SendToRunResponseMetadata{RunID: chat.ID, State: state}

	// Liveness is decided here from the root run's state, the same predicate
	// RunService.SignalRun uses, so a finished run is reported rather than
	// handed to the messenger. The messenger re-checks, because the run can
	// finish between this read and its write.
	//
	// PENDING is live to Live() (its first turn is still ahead of it) but is
	// refused here: delivering to a chat that has not started would have to
	// start it, and starting is start_run's job, recorded as a launch event.
	if !chat.RootStatus.Live() || state == "pending" {
		return WithResponseMetadata(NewTextResponse(fmt.Sprintf(
			"Not delivered: run %s is %s, not live, so nothing was sent and nothing was started. "+
				"send_to_run only reaches a run that is running or paused; use start_run for new work.", chat.ID, state)), meta), nil
	}

	// The message is the calling run's, never its user's: the target chat
	// shows it as from this run (by its main thread), and so does its model.
	from := RunSender{ThreadID: caller.chat.MainThreadID(), Subject: subject}
	delivery, err := s.messenger.DeliverToRun(rctx.Context, caller.userID, chat.ID, strings.TrimSpace(params.Thread), params.Message, from)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Could not deliver to run %s: %v", chat.ID, err)), nil
	}
	if !delivery.Delivered && delivery.NotDeliveredReason == errreason.RunNoListeningAgent {
		return WithResponseMetadata(NewTextResponse(fmt.Sprintf(
			"Not delivered: no agent is running in run %s right now (it is %s, between its agent steps), so nothing was sent. "+
				"Nothing was queued for later; check its progress with get_run.", chat.ID, state)), meta), nil
	}
	if !delivery.Delivered {
		return WithResponseMetadata(NewTextResponse(fmt.Sprintf(
			"Not delivered: run %s stopped being live before the message could be saved, so nothing was sent and nothing was started.", chat.ID)), meta), nil
	}

	meta.Delivered = true
	meta.MessageID = delivery.MessageID
	note := ""
	if state == "paused" {
		note = " The run was paused, and the message resumed it."
	}
	return WithResponseMetadata(NewTextResponse(fmt.Sprintf(
		"Delivered to run %s (%s): the message is saved to its thread and the run was nudged to look. "+
			"It has NOT necessarily read it yet — check with get_run.%s", chat.ID, state, note)), meta), nil
}
