// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type SendToRunParams struct {
	RunID   string `json:"run_id" jsonschema:"required,description=The run id (chat id), from start_run or list_runs."`
	Message string `json:"message" jsonschema:"required,description=The message to deliver to the run."`
}

const (
	SendToRunToolName    = "send_to_run"
	sendToRunDescription = `Send a message to another top-level run that is still going, as if the user had typed it into that chat.

ONLY LIVE RUNS: a run that is running or paused. If the run has finished, or has not started yet, this delivers nothing and says so (delivered: false) — it never starts or restarts a run. Use start_run for new work.

A PAUSED run is resumed by the message, exactly as if the user had typed into the paused chat. If you do not want that, do not send it.

THE RECEIPT IS HONEST: delivered means the message was saved to the run's thread and the run was nudged to look. It does NOT mean the run has read it or acted on it. Check with get_run.

You cannot message the run you are executing in, and you can only message runs the user owns. To message a sub-agent you spawned, use spawn_send.`
)

type sendToRunTool struct {
	repo      db.Repository
	messenger RunMessenger
}

func NewSendToRunTool(repo db.Repository, messenger RunMessenger) Tool {
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

	delivery, err := s.messenger.DeliverToRun(rctx.Context, caller.userID, chat.ID, params.Message)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Could not deliver to run %s: %v", chat.ID, err)), nil
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
