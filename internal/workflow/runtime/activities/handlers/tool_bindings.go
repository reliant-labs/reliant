// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"log/slog"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"go.temporal.io/sdk/activity"
)

// leveledLogger is the narrow slice of logging this file needs, declared at
// the consumer so both Temporal's activity logger and *slog.Logger satisfy it
// without either having to know about the other.
type leveledLogger interface {
	Debug(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
}

// bindingLogger returns the activity logger when there is one, and slog
// otherwise.
//
// activity.GetLogger PANICS outside an activity context, and this code runs in
// both — the real worker, and any caller assembling a tool list without a
// Temporal harness. The rest of this package guards with
// `if !activity.IsActivity(ctx) { return }`, which drops the line entirely;
// that is the wrong trade here, because these warnings are the ONLY signal
// that a preference a human configured was silently ignored.
func bindingLogger(ctx context.Context) leveledLogger {
	if activity.IsActivity(ctx) {
		return activity.GetLogger(ctx)
	}
	return slog.Default()
}

// resolveToolBindingScopes gathers every configuration scope that can bind a
// tool's parameters for this call, in the order they layer:
//
//	tool default  →  global setting  →  workflow YAML
//
// The tool's own defaults are not a scope here — ToolWrapper folds
// DefaultBindings into whatever WithBindings receives, so each layer supplies
// only what it overrides and an unconfigured tool needs nothing from this
// function at all.
//
// Scopes.Preset is left unset because a preset is not a separate CHANNEL here,
// not because a preset cannot bind tool parameters — it can, and that is a
// primary use.
//
// A preset is an untyped bag of values applied to workflow INPUTS
// (preset.ApplyToInputs). The workflow reaches into that bag by declaring the
// path, either per parameter (`size: "{{inputs.image_size}}"`) or for the whole
// block (`tools: "{{inputs.tool_params}}"`, which lets a preset configure tools
// the workflow author never named). CEL resolves both before this activity
// runs, so a preset's bindings arrive already folded into the workflow scope
// with the preset's values in them.
//
// Populating Preset from the same ToolsConfig would therefore apply one value
// twice under two names. The field stays in toolbindings because it is the
// right model for a channel that binds tools DIRECTLY rather than through
// inputs — a per-run override chosen in the UI is the obvious candidate.
//
// A failed global read is logged and dropped rather than returned. Losing a
// preference degrades to the tool's default, which still produces a working
// call; failing the activity over it would take the whole turn down for a
// setting the user could equally never have written.
func (a *CallLLMActivity) resolveToolBindingScopes(ctx context.Context, userID string, toolsConfig *reliantv1.ToolsConfig) toolbindings.Scopes {
	scopes := toolbindings.Scopes{
		Workflow: workflowScopeBindings(toolsConfig),
	}

	global, err := toolbindings.LoadGlobal(ctx, a.repo, userID)
	if err != nil {
		// Partial results are still returned alongside the error — one
		// malformed row must not discard the rows that parsed.
		bindingLogger(ctx).Warn("[CallLLM] Some global tool bindings could not be read; those tools keep their defaults",
			"userID", userID, "error", err)
	}
	scopes.Global = global

	return scopes
}

// applyToolBindings binds the resolved parameters onto the tool list. Problems
// are logged, never fatal: a stale binding naming a parameter a tool no longer
// declares leaves that tool unbound and callable rather than failing the turn.
func applyToolBindings(ctx context.Context, toolsList []tools.Tool, scopes toolbindings.Scopes) []tools.Tool {
	bound, problems := toolbindings.Apply(toolsList, scopes)
	logger := bindingLogger(ctx)
	for _, problem := range problems {
		logger.Warn("[CallLLM] Tool binding not applied", "error", problem)
	}
	if names := scopes.ToolNames(); len(names) > 0 {
		logger.Debug("[CallLLM] Applied tool bindings", "tools", names)
	}
	return bound
}

// boundToolInput is the input a call is dispatched with: the model's own,
// with the bound parameters the turn's capability set recorded for the tool
// merged in (tools.ApplyBindings). When the call may not run it returns the
// reason instead, written for the model:
//   - it set a bound parameter to a value other than the bound one;
//   - a parameter bound by the run owner's global setting is no longer bound
//     by it, or the setting could not be read.
//
// A globally bound value is read here, from the setting, because the set
// records it by name only (tools.BoundParam.Global). A setting that cannot be
// read refuses the call rather than running it open: the model was not shown
// the parameter, so the call has no value of its own there.
//
// A batch with no recorded set (a run that predates it) binds nothing, as
// before.
func (a *ExecuteToolsActivity) boundToolInput(ctx context.Context, caps *tools.Capabilities, tec *toolExecutionContext) (string, string) {
	if !caps.Binds(tec.toolName) {
		return tec.toolInput, ""
	}
	var global tools.Bindings
	if caps.BindsFromGlobalSetting(tec.toolName) {
		byTool, err := toolbindings.LoadGlobal(ctx, a.repo, tec.chat.UserID)
		global = byTool[tec.toolName]
		if err != nil {
			bindingLogger(ctx).Warn("[ExecuteTools] Global tool bindings could not be read",
				"tool", tec.toolName, "userID", tec.chat.UserID, "error", err)
			if len(global) == 0 {
				return "", fmt.Sprintf("A setting that fixes a parameter of '%s' could not be read, so the call was not run.", tec.toolName)
			}
		}
	}
	bindings, err := caps.ExecutionBindings(tec.toolName, global)
	if err != nil {
		return "", err.Error()
	}
	input, err := tools.ApplyBindings(tec.toolName, tec.toolInput, bindings)
	if err != nil {
		return "", err.Error()
	}
	bindingLogger(ctx).Debug("[ExecuteTools] Applied bound parameters",
		"tool", tec.toolName, "toolCallID", tec.toolCallID, "params", bindings.Names())
	return input, ""
}

// workflowScopeBindings extracts the bindings a workflow's YAML declares for
// this node, which arrive on ToolsConfig.tools.
//
// The values are already CEL-resolved: EvaluateNodeConfig runs over the whole
// node before the activity is handed it, and ResolveCELFields walks
// map<string, google.protobuf.Value> fields, so a `{{inputs.x}}` written in
// the YAML is a concrete value here.
//
// A preset reaches this through the SAME path rather than a scope of its own —
// see presetScopeBindings.
func workflowScopeBindings(toolsConfig *reliantv1.ToolsConfig) toolbindings.ByTool {
	return toolbindings.FromProtoStructs(toolsConfig.GetTools())
}
