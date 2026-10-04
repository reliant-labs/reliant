// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type ControlRunParams struct {
	RunID  string `json:"run_id" jsonschema:"required,description=The run id (chat id), from start_run or list_runs."`
	Action string `json:"action" jsonschema:"required,enum=pause,enum=resume,enum=cancel,description=pause parks the run so it can be resumed. resume continues a paused or interrupted run. cancel hard-stops it for good."`
}

const (
	ControlRunToolName    = "control_run"
	controlRunDescription = `Pause, resume or cancel another top-level run the user owns.

- pause: park the run at its next step boundary. It stays alive and resumable.
- resume: continue a paused (or interrupted) run from where it stopped.
- cancel: hard-stop the run. This is terminal — a cancelled run is not resumed; sending it a message starts a fresh run in the same chat.

THE RECEIPT IS HONEST: pause and cancel REQUEST the stop. A run in the middle of a long tool call stops when that call returns. Confirm with get_run.

You cannot control the run you are executing in, and you can only touch runs the user owns. To stop a sub-agent you spawned, use spawn_stop.`
)

type controlRunTool struct {
	repo      db.Repository
	lifecycle RunLifecycle
}

func NewControlRunTool(repo db.Repository, lifecycle RunLifecycle) Tool {
	return NewToolWrapper[ControlRunParams, ToolResponse](&controlRunTool{repo: repo, lifecycle: lifecycle})
}

func (c *controlRunTool) Name() string        { return ControlRunToolName }
func (c *controlRunTool) Description() string { return controlRunDescription }

func (c *controlRunTool) RequiresPermission(params ControlRunParams) (bool, error) {
	return false, nil
}

func (c *controlRunTool) Execute(rctx *rctx.ToolContext, params ControlRunParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, c.repo)
	if errResp != nil {
		return *errResp, nil
	}

	action := strings.ToLower(strings.TrimSpace(params.Action))
	switch action {
	case "pause", "resume", "cancel":
	default:
		return NewTextErrorResponse(fmt.Sprintf("action %q is not one of pause, resume, cancel", params.Action)), nil
	}

	// The self check precedes the ownership lookup on purpose: it needs no
	// database read, and its message is the helpful one.
	if errResp := rejectSelfTarget(rctx, params.RunID, action); errResp != nil {
		return *errResp, nil
	}
	chat, errResp := resolveOwnedRun(rctx, c.repo, caller, params.RunID)
	if errResp != nil {
		return *errResp, nil
	}
	if c.lifecycle == nil {
		return NewTextErrorResponse("Controlling a run is not available in this runtime."), nil
	}

	state := runStateLabel(chat)
	switch action {
	case "pause":
		switch state {
		case "paused":
			return NewTextResponse(fmt.Sprintf("Run %s is already paused.", chat.ID)), nil
		case "completed", "failed", "cancelled":
			return NewTextErrorResponse(fmt.Sprintf("Run %s is %s, so there is nothing to pause.", chat.ID, state)), nil
		}
		if err := c.lifecycle.PauseRun(rctx.Context, caller.userID, chat.ID); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Could not pause run %s: %v", chat.ID, err)), nil
		}
		return NewTextResponse(fmt.Sprintf(
			"Pause REQUESTED for run %s. It parks at its next step boundary, so a run inside a long tool call keeps going until that call returns. "+
				"Confirm with get_run.", chat.ID)), nil

	case "resume":
		switch state {
		case "running":
			return NewTextResponse(fmt.Sprintf("Run %s is already running.", chat.ID)), nil
		case "pending":
			return NewTextErrorResponse(fmt.Sprintf("Run %s has not started, so there is nothing to resume.", chat.ID)), nil
		case "completed", "cancelled":
			return NewTextErrorResponse(fmt.Sprintf(
				"Run %s is %s and cannot be resumed. send_to_run does not restart it either; start a new run with start_run.", chat.ID, state)), nil
		}
		result, err := c.lifecycle.ResumeRun(rctx.Context, caller.userID, chat.ID)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Could not resume run %s: %v", chat.ID, err)), nil
		}
		if !result.Resumed {
			return NewTextErrorResponse(fmt.Sprintf("Run %s could not be resumed: %s", chat.ID, result.Detail)), nil
		}
		return NewTextResponse(fmt.Sprintf("Run %s is resumed and executing again. Confirm with get_run.", chat.ID)), nil

	default: // cancel
		switch state {
		case "completed", "failed", "cancelled":
			return NewTextResponse(fmt.Sprintf("Run %s has already finished (%s); there was nothing to cancel.", chat.ID, state)), nil
		}
		if err := c.lifecycle.CancelRun(rctx.Context, caller.userID, chat.ID); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Could not cancel run %s: %v", chat.ID, err)), nil
		}
		return NewTextResponse(fmt.Sprintf(
			"Cancel REQUESTED for run %s. The run is hard-stopped and will not resume. Confirm with get_run.", chat.ID)), nil
	}
}
