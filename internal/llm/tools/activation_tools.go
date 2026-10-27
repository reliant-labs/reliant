// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/rctx"
)

const (
	ToolCreateTriggerActivation = "create_trigger_activation"
	ToolUpdateTriggerActivation = "update_trigger_activation"
	ToolEnableTrigger           = "enable_trigger"
	ToolPauseTrigger            = "pause_trigger"
)

func IsActivationManagementTool(name string) bool {
	switch name {
	case ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger, ToolPauseTrigger:
		return true
	}
	return false
}

type ActivationParams struct {
	TriggerID       string            `json:"trigger_id,omitempty" jsonschema:"description=Existing activation ID. Required except for create."`
	Name            string            `json:"name,omitempty" jsonschema:"description=Activation display name. Required on create; omitted update fields are kept."`
	Workflow        string            `json:"workflow,omitempty" jsonschema:"description=Workflow slug or builtin:// ref. Required on create."`
	WorkflowTrigger string            `json:"workflow_trigger,omitempty" jsonschema:"description=Declared trigger name. Required on create."`
	ProjectID       string            `json:"project_id,omitempty" jsonschema:"description=Project ID. Required on create; may re-point on update."`
	MachineID       *string           `json:"machine_id,omitempty" jsonschema:"description=Daemon ID of the machine to pin. Explicit empty string requires no_machine true."`
	NoMachine       *bool             `json:"no_machine,omitempty" jsonschema:"description=Explicitly run without a machine."`
	ConnectionID    *string           `json:"connection_id,omitempty" jsonschema:"description=Connection ID; empty selects the default connection."`
	Params          map[string]any    `json:"params,omitempty" jsonschema:"description=Typed workflow inputs; replaces the params map when supplied."`
	Presets         map[string]string `json:"presets,omitempty" jsonschema:"description=Workflow preset assignments."`
	Message         *string           `json:"message,omitempty" jsonschema:"description=Seed prompt; omitted uses declaration prompt on create."`
	ActingIdentity  *string           `json:"acting_identity,omitempty" jsonschema:"description=owner or app. Defaults to owner on create."`
}

// ActivationPreview is a validated, immutable approval description, never a grant.
type ActivationPreview struct {
	Title    string    `json:"title"`
	Hash     string    `json:"hash"`
	Revision time.Time `json:"revision,omitempty"`
}

// ActivationManager adapts the app's service; no second validation implementation.
type ActivationManager interface {
	PrepareActivation(context.Context, string, string, ActivationParams) (ActivationPreview, error)
	ManageActivation(context.Context, string, string, ActivationParams, string) (TriggerSummary, error)
}

// AutomationApprovalTool is consumed by the workflow approval activity.
type AutomationApprovalTool interface {
	PrepareAutomation(*rctx.ToolContext, ToolCall, bool) (ActivationPreview, bool, error)
}

type activationTool struct {
	Tool
	name    string
	repo    runCallerStore
	manager ActivationManager
}

// Preserve the approval hook when a workflow/global setting binds parameters.
func (t *activationTool) Bindings() Bindings { return t.Tool.(BindableTool).Bindings() }
func (t *activationTool) WithBindings(bindings Bindings) (Tool, error) {
	bound, err := t.Tool.(BindableTool).WithBindings(bindings)
	if err != nil {
		return t, err
	}
	clone := *t
	clone.Tool = bound
	return &clone, nil
}

type activationTypedTool struct{ outer *activationTool }

func (t *activationTypedTool) Name() string { return t.outer.name }
func (t *activationTypedTool) Description() string {
	return "Manage a declared workflow trigger activation (" + t.outer.name + "). Uses the app's validation. Interactive calls require a user approval card. Unattended calls are refused. No change happens until approved. Use machine_id to pin or re-point to a machine from list_triggers/project context; never guess IDs. Changes are audit logged."
}
func (t *activationTypedTool) RequiresPermission(ActivationParams) (bool, error) { return true, nil }
func NewActivationTool(repo runCallerStore, manager ActivationManager, name string) Tool {
	t := &activationTool{name: name, repo: repo, manager: manager}
	t.Tool = NewToolWrapper[ActivationParams, ToolResponse](&activationTypedTool{outer: t})
	return t
}
func (t *activationTool) PrepareAutomation(ctx *rctx.ToolContext, call ToolCall, unattended bool) (ActivationPreview, bool, error) {
	caller, failure := resolveRunCaller(ctx, t.repo)
	if failure != nil {
		return ActivationPreview{}, false, fmt.Errorf("%s", failure.Content)
	}
	if unattended {
		return ActivationPreview{}, false, fmt.Errorf("%s", automationpolicy.Refusal())
	}
	if t.manager == nil {
		return ActivationPreview{}, false, fmt.Errorf("activation management is unavailable")
	}
	input := call.Input
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		input, err = ApplyBindings(t.name, input, bindings)
		if err != nil {
			return ActivationPreview{}, false, err
		}
	}
	var p ActivationParams
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		return ActivationPreview{}, false, err
	}
	preview, err := t.manager.PrepareActivation(ctx.Context, caller.userID, t.name, p)
	return preview, true, err
}
func (t *activationTypedTool) Execute(ctx *rctx.ToolContext, p ActivationParams) (ToolResponse, error) {
	o := t.outer
	caller, failure := resolveRunCaller(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	exec := automationpolicy.From(ctx.Context)
	if exec.Unattended {
		return NewTextErrorResponse(automationpolicy.Refusal()), nil
	}
	callID := ""
	if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		callID = call.CurrentToolCallID
	}
	approved := exec.Approvals[callID]
	if approved == "" {
		return NewTextErrorResponse("This activation change requires an approval card in an interactive chat. It was not run."), nil
	}
	if o.manager == nil {
		return NewTextErrorResponse("Activation management is unavailable."), nil
	}
	actorCtx := automationpolicy.WithActor(ctx.Context, automationpolicy.Actor{ChatID: ctx.ChatID, RunID: ctx.Thread, ToolCallID: callID})
	summary, err := o.manager.ManageActivation(actorCtx, caller.userID, o.name, p, approved)
	if err != nil {
		return NewTextErrorResponse("Activation change refused or could not be completed. Inspect its current state and setup in the app before retrying; a changed configuration requires a new approval."), nil
	}
	return WithResponseMetadata(NewTextResponse(fmt.Sprintf("%s: %s (id %s), enabled=%t, machine=%s (id %s), project=%s", o.name, summary.Name, summary.ID, summary.Enabled, summary.DaemonName, summary.DaemonID, summary.ProjectID)), summary), nil
}
