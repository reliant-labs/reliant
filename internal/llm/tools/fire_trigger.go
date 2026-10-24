// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

type FireTriggerParams struct {
	TriggerID string `json:"trigger_id" jsonschema:"required,description=ID of an existing trigger from list_triggers."`
}

const FireTriggerToolName = "fire_trigger"

const fireTriggerDescription = `Run an existing trigger once now, using its saved workflow, inputs, machine and connections. Never creates, enables or changes a trigger.

This is a manual fire: it bypasses enabled and overlap checks, so even a paused trigger may run once without being enabled. It can start real work with the trigger's saved privileges. Use only when the user asks to run or test that activation.

Returns fire_workflow_id: the asynchronous firing was accepted, NOT proof that a run launched or succeeded. Do not retry just because the run is not visible yet: each call starts another fire. Inspect list_trigger_events for this trigger, then get_run with the resulting chat_id.`

type fireTriggerTool struct {
	repo  runCallerStore
	firer TriggerFirer
}

func NewFireTriggerTool(repo runCallerStore, firer TriggerFirer) Tool {
	return NewToolWrapper[FireTriggerParams, ToolResponse](&fireTriggerTool{repo: repo, firer: firer})
}

func (f *fireTriggerTool) Name() string                                       { return FireTriggerToolName }
func (f *fireTriggerTool) Description() string                                { return fireTriggerDescription }
func (f *fireTriggerTool) RequiresPermission(FireTriggerParams) (bool, error) { return false, nil }

type FireTriggerResponseMetadata struct {
	TriggerID      string `json:"trigger_id"`
	FireWorkflowID string `json:"fire_workflow_id"`
}

func (f *fireTriggerTool) Execute(ctx *rctx.ToolContext, params FireTriggerParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(ctx, f.repo)
	if errResp != nil {
		return *errResp, nil
	}
	id := strings.TrimSpace(params.TriggerID)
	if id == "" {
		return NewTextErrorResponse("trigger_id is required"), nil
	}
	if f.firer == nil {
		return NewTextErrorResponse("Firing triggers is not available in this runtime."), nil
	}
	fireID, err := f.firer.FireTrigger(ctx.Context, caller.userID, id)
	if err != nil {
		return NewTextErrorResponse("Could not fire trigger: " + err.Error()), nil
	}
	return WithResponseMetadata(NewTextResponse(fmt.Sprintf("Manual fire accepted for trigger %s. fire_workflow_id: %s\nThis is asynchronous, not a completed run. Inspect list_trigger_events with trigger_id %s, then get_run with the launched chat_id. Do not fire again just to check status.", id, fireID, id)), FireTriggerResponseMetadata{TriggerID: id, FireWorkflowID: fireID}), nil
}
