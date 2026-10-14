// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/celeval"
)

// =============================================================================
// CEL EVAL TOOL
// =============================================================================

type CELEvalParams struct {
	Expression     string         `json:"expression" jsonschema:"required,description=The CEL expression or {{ }} template to evaluate. A trigger filter or node condition is raw CEL (no braces)\\; every other workflow field is a template."`
	Kind           string         `json:"kind,omitempty" jsonschema:"enum=expression,enum=template,description=How to read it. Default: a template when it has a {{ outside a string literal\\, otherwise raw CEL."`
	Scope          string         `json:"scope,omitempty" jsonschema:"enum=run,enum=trigger,description=What it can see. 'trigger' (a trigger's filter\\, inputs mapping or prompt) sees only the trigger; 'run' (default\\, a node's fields) also sees inputs and nodes."`
	Integration    string         `json:"integration,omitempty" jsonschema:"description=Generate trigger.payload from the payload this integration declares for 'event' (e.g. 'github'). Use with event."`
	Event          string         `json:"event,omitempty" jsonschema:"description=The integration event whose declared payload to generate (e.g. 'issues.opened')."`
	TriggerPayload map[string]any `json:"trigger_payload,omitempty" jsonschema:"description=trigger.payload. With integration+event it is merged over the generated sample\\, so supply only the fields the expression depends on."`
	TriggerSender  map[string]any `json:"trigger_sender,omitempty" jsonschema:"description=trigger.sender: kind\\, id\\, display_name\\, verified."`
	TriggerKind    string         `json:"trigger_kind,omitempty" jsonschema:"description=trigger.kind: webhook\\, integration\\, schedule or workflow_event. Default: integration when an integration is named\\, else webhook."`
	Inputs         map[string]any `json:"inputs,omitempty" jsonschema:"description=Workflow inputs (inputs.*). Not available in scope 'trigger'."`
	Nodes          map[string]any `json:"nodes,omitempty" jsonschema:"description=Node outputs\\, keyed by node id (nodes.<id>.*). Not available in scope 'trigger'."`
}

type celEvalTool struct{}

const (
	CELEvalToolName        = "cel_eval"
	celEvalToolDescription = `Evaluates a CEL expression or {{ }} template against a context you supply, exactly as a workflow would, and returns the value and its type, or a precise error.

WHEN TO USE:
- To test a trigger filter, an input mapping or a condition in ONE call, instead of activating the trigger and waiting for an event
- To check what a template produces, and what type (a pure {{ }} keeps its type: a number stays a number)
- To find out why an expression fails (the error says whether it is syntax, compile or evaluation, and where)

CONTEXT:
- integration + event: trigger.payload is GENERATED from the payload that integration declares for the event (the same schema workflow validation checks filters against). Supply trigger_payload to override just the fields your expression depends on; it is merged over the sample.
- trigger_payload alone: a webhook or other source you describe yourself.
- inputs / nodes: values for inputs.* and nodes.<id>.* (scope run).
- With no expression, returns the trigger the event delivers, so you can see what there is to write against.

RETURNS:
JSON: value, type, kind (expression|template), and findings (reads the declared payload rules out, which a loose sample can hide: a filter that passes here can still fail on a real event if it reads a field the payload never has). On failure, error: phase (syntax|compile|eval|request), message, line, column, hint.

Evaluation is bounded (CEL cost limit) and has no side effects.`
)

func NewCELEvalTool() Tool {
	return NewToolWrapper(&celEvalTool{})
}

func (t *celEvalTool) Name() string        { return CELEvalToolName }
func (t *celEvalTool) Description() string { return celEvalToolDescription }

func (t *celEvalTool) RequiresPermission(args CELEvalParams) (bool, error) { return false, nil }

func (t *celEvalTool) Execute(rctx *rctx.ToolContext, args CELEvalParams) (ToolResponse, error) {
	res := celeval.Evaluate(celeval.Request{
		Expression:     args.Expression,
		Kind:           celeval.Kind(strings.TrimSpace(args.Kind)),
		Scope:          scopeFromTool(args.Scope),
		Integration:    strings.TrimSpace(args.Integration),
		Event:          strings.TrimSpace(args.Event),
		TriggerKind:    strings.TrimSpace(args.TriggerKind),
		TriggerPayload: args.TriggerPayload,
		TriggerSender:  args.TriggerSender,
		Inputs:         args.Inputs,
		Nodes:          args.Nodes,
	})
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("the result is not representable as JSON: %v", err)), nil
	}
	// A failed evaluation is the answer the author asked for, not a tool
	// failure: return it as a normal result so it is read, not retried.
	return NewTextResponse(string(out)), nil
}

func scopeFromTool(s string) celeval.Scope {
	if strings.EqualFold(strings.TrimSpace(s), "trigger") {
		return celeval.ScopeTrigger
	}
	return celeval.ScopeRun
}
