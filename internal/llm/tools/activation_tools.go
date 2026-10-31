// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
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
	ActingIdentity  *string           `json:"acting_identity,omitempty" jsonschema:"description=Who GitHub writes speak as: app (the Reliant GitHub bot) or owner. Defaults to app on create and to unchanged on update; choose owner only to write under the owner's own account."`
}

// ActivationPreview is a validated, immutable approval description, never a grant.
type ActivationPreview struct {
	Title    string    `json:"title"`
	Hash     string    `json:"hash"`
	Revision time.Time `json:"revision,omitempty"`
	// CardRequired: the owner's automation-approvals setting needs a card for
	// this change. False means it applies without one (and is recorded);
	// execution decides again, so this is advice, never a grant.
	CardRequired bool `json:"card_required,omitempty"`
	// Reason says why, in one display-safe line.
	Reason string `json:"reason,omitempty"`
}

// ApprovalRequiredMessage is the refusal when a change needs a card and none
// was approved.
const ApprovalRequiredMessage = "This activation change widens its power and needs the owner's approval card in an interactive chat. It was not run."

// ActivationRefusal is a failure to prepare an activation whose Reason may be
// shown to the agent and the owner: a fixed classification that names at most
// fields, input names and declared names, never an input, preset or graph
// value. Err is the full cause, for server logs only. It unwraps to Err, so
// a classified error keeps its svcerr code.
type ActivationRefusal struct {
	Reason string
	Err    error
}

func (e *ActivationRefusal) Error() string { return e.Reason + ": " + e.Err.Error() }
func (e *ActivationRefusal) Unwrap() error { return e.Err }

// RefuseActivation classifies err under reason. A classification already on
// err is more specific than the caller's, so it is kept.
func RefuseActivation(reason string, err error) error {
	if err == nil {
		return nil
	}
	var refusal *ActivationRefusal
	if errors.As(err, &refusal) {
		return err
	}
	return &ActivationRefusal{Reason: reason, Err: err}
}

// ActivationRefusalReason is err's safe classification, or "" when it has none.
func ActivationRefusalReason(err error) string {
	var refusal *ActivationRefusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return ""
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
	return "Manage a declared workflow trigger activation (" + t.outer.name + "). Uses the app's validation. The owner's automation-approvals setting decides whether a change needs their approval card: by default only a change that widens the activation's power (a new connection or scope, a different acting identity, a new machine, new secrets, or a production deploy step) does, and pausing never does. A change that needs a card is refused in an unattended run. Use machine_id to pin or re-point to a machine from list_triggers/project context; never guess IDs. Every change is audit logged."
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
		return ActivationPreview{}, false, RefuseActivation("the calling chat could not be resolved", fmt.Errorf("%s", failure.Content))
	}
	if t.manager == nil {
		return ActivationPreview{}, false, RefuseActivation("activation management is unavailable on this server", errors.New("no activation manager"))
	}
	// Decode exactly as Run will: a model that stringified params (common
	// for an open object) is repaired here too, or the preview refuses a
	// call its execution would have accepted.
	input := normalizeModelInput(call.Input, t.ParamSchema())
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		input, err = ApplyBindings(t.name, input, bindings)
		if err != nil {
			return ActivationPreview{}, false, RefuseActivation("tool bindings could not be applied", err)
		}
	}
	var p ActivationParams
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		reason := "tool input is not a valid " + t.name + " call"
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			reason = fmt.Sprintf("tool input field %s must be a JSON %s", typeErr.Field, jsonKind(typeErr.Type))
		}
		return ActivationPreview{}, false, RefuseActivation(reason, err)
	}
	preview, err := t.manager.PrepareActivation(ctx.Context, caller.userID, t.name, p)
	if err != nil {
		return ActivationPreview{}, false, err
	}
	// The approval setting allows this change without a card: no card, and
	// an unattended run may make it. Execution decides again.
	if !preview.CardRequired {
		return preview, false, nil
	}
	if unattended {
		return ActivationPreview{}, false, RefuseActivation(automationpolicy.Refusal(), errors.New("unattended: "+preview.Reason))
	}
	return preview, true, nil
}

// jsonKind names the JSON type a Go value decodes from.
func jsonKind(t reflect.Type) string {
	if t == nil {
		return "value"
	}
	switch t.Kind() {
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Pointer:
		return jsonKind(t.Elem())
	default:
		return "number"
	}
}

func (t *activationTypedTool) Execute(ctx *rctx.ToolContext, p ActivationParams) (ToolResponse, error) {
	o := t.outer
	caller, failure := resolveRunCaller(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	exec := automationpolicy.From(ctx.Context)
	callID := ""
	if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
		callID = call.CurrentToolCallID
	}
	if o.manager == nil {
		return NewTextErrorResponse("Activation management is unavailable."), nil
	}
	// The manager decides whether this change needs an approved card (the
	// owner's automation-approvals setting) and refuses one that does
	// without a matching approval. An empty approval is a change the
	// preview judged card-free.
	actorCtx := automationpolicy.WithActor(ctx.Context, automationpolicy.Actor{ChatID: ctx.ChatID, RunID: ctx.Thread, ToolCallID: callID})
	summary, err := o.manager.ManageActivation(actorCtx, caller.userID, o.name, p, exec.Approvals[callID])
	if err != nil {
		if exec.Unattended && strings.Contains(err.Error(), automationpolicy.Refusal()) {
			return NewTextErrorResponse(automationpolicy.Refusal()), nil
		}
		if strings.Contains(err.Error(), ApprovalRequiredMessage) {
			return NewTextErrorResponse(ApprovalRequiredMessage), nil
		}
		return NewTextErrorResponse("Activation change refused or could not be completed. Inspect its current state and setup in the app before retrying; a change that widens the activation's power needs a new approval."), nil
	}
	return WithResponseMetadata(NewTextResponse(fmt.Sprintf("%s: %s (id %s), enabled=%t, machine=%s (id %s), project=%s", o.name, summary.Name, summary.ID, summary.Enabled, summary.DaemonName, summary.DaemonID, summary.ProjectID)), summary), nil
}
