// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A run's identity on its spans, for AI monitoring (internal/telemetry,
// ai_attrs.go). The workflow decides it — which run, which workflow, which
// agent — and hands it to every activity in RuntimeContext.Telemetry; the
// worker's spans read it from there. No span is opened here: workflow code is
// replayed, so the spans are all opened worker-side.
//
// The run id (forge.ai.run.id) is what the run explorer groups a run's spans
// by, so it must outlast the Temporal run id:
//
//   - a fresh run (a new message, a late wake's successor) is a run of its
//     own: its id is its Temporal run id;
//   - a continue-as-new successor that resumes its predecessor carries the id
//     in its execution context;
//   - a resume of an interrupted run (a new Temporal execution the API starts)
//     reads it from the checkpoint the interrupted run left (ResumeInput.RunID);
//   - a spawned sub-agent, which runs inline in its parent's execution, is a
//     run of its own: its id is its workflow id, stable across every relaunch,
//     and it names its parent's and the root's.

// rootRunIDKey holds the root run's id in the workflow context.
type rootRunIDKey struct{}

// startRunTelemetry decides the root run's telemetry, records it on execCtx,
// keeps the root run id in the returned context (rootRunIDFrom) and names the
// run on the workflow's own span (RunWorkflow:DynamicWorkflow). Deterministic:
// it reads only the input and workflow info, and issues no command.
func startRunTelemetry(ctx workflow.Context, input WorkflowInput, execCtx *ExecutionContext) workflow.Context {
	info := workflow.GetInfo(ctx)
	runID := info.WorkflowExecution.RunID
	if input.Resume != nil {
		if prev := execCtx.Telemetry; prev != nil && prev.RunID != "" {
			runID = prev.RunID
		} else if input.Resume.RunID != "" {
			runID = input.Resume.RunID
		}
	}
	trigger := "manual"
	if input.Trigger != nil && input.Trigger.Kind != "" {
		trigger = input.Trigger.Kind
	}
	rt := &types.RunTelemetry{
		WorkflowName: workflowDisplayName(input.WorkflowName),
		WorkflowRef:  input.WorkflowName,
		Trigger:      trigger,
		RunID:        runID,
		RootRunID:    runID,
	}
	if prev := execCtx.Telemetry; prev != nil {
		rt.Preset = prev.Preset
	}
	execCtx.Telemetry = rt

	// The workflow's span exists on every execution, replays included (the
	// tracing interceptor opens it as the workflow starts); only the last one
	// is exported. Attributes are not commands, so setting them is replay-safe.
	if span, ok := temporalotel.SpanFromWorkflowContext(ctx); ok {
		scope := telemetry.RunScope{
			ChatID:       input.ChatID,
			ThreadID:     execCtx.Thread,
			WorkflowID:   info.WorkflowExecution.ID,
			WorkflowName: rt.WorkflowName,
			WorkflowRef:  rt.WorkflowRef,
			Trigger:      rt.Trigger,
			RunID:        rt.RunID,
			RootRunID:    rt.RootRunID,
		}
		span.SetAttributes(attribute.String(telemetry.AttrGenAIOperationName, telemetry.GenAIOpInvokeWorkflow))
		span.SetAttributes(scope.SpanAttributes()...)
		if input.ChatID != "" {
			span.SetAttributes(attribute.String(telemetry.AttrGenAIConversationID, input.ChatID))
		}
	}
	return workflow.WithValue(ctx, rootRunIDKey{}, runID)
}

// runTelemetryKey holds, for a save_message the workflow writes itself, the
// telemetry of the run whose node it saves (withRunTelemetry).
type runTelemetryKey struct{}

// withRunTelemetry is ctx carrying rt for the save_message activities
// executeSaveMessageInline schedules, which are built from the resolved
// message rather than from a node's runtime context.
func withRunTelemetry(ctx workflow.Context, rt *types.RunTelemetry) workflow.Context {
	if rt == nil {
		return ctx
	}
	return workflow.WithValue(ctx, runTelemetryKey{}, rt)
}

// runTelemetryFrom is a copy of the telemetry withRunTelemetry put on ctx, nil
// when none.
func runTelemetryFrom(ctx workflow.Context) *types.RunTelemetry {
	rt, _ := ctx.Value(runTelemetryKey{}).(*types.RunTelemetry)
	if rt == nil {
		return nil
	}
	cp := *rt
	return &cp
}

// rootRunIDFrom is the root run's id, "" in a context startRunTelemetry did
// not set up.
func rootRunIDFrom(ctx workflow.Context) string {
	id, _ := ctx.Value(rootRunIDKey{}).(string)
	return id
}

// spawnRunTelemetry is a spawned sub-agent's telemetry: a run of its own,
// named by its workflow id, whose parent is the run of parentWorkflowID — the
// root's when the root spawned it, else the sub-agent (itself a run named by
// its workflow id) that did.
func spawnRunTelemetry(ctx workflow.Context, childWorkflowID, parentWorkflowID, workflowName, preset string) *types.RunTelemetry {
	root := rootRunIDFrom(ctx)
	parent := parentWorkflowID
	if parentWorkflowID == workflow.GetInfo(ctx).WorkflowExecution.ID {
		parent = root
	}
	return &types.RunTelemetry{
		WorkflowName: workflowDisplayName(workflowName),
		WorkflowRef:  workflowName,
		Trigger:      "spawn",
		RunID:        childWorkflowID,
		ParentRunID:  parent,
		RootRunID:    root,
		Preset:       preset,
	}
}

// workflowDisplayName is a workflow reference as the name a dashboard groups
// by: "builtin://agent" and "agent" are both "agent", a project workflow's
// path is its file's name without the extension.
func workflowDisplayName(ref string) string {
	name := ref
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
	if name == "" {
		return ref
	}
	return name
}

// withPreset is rt naming preset as its agent's, as a copy: a context's
// telemetry is shared with the contexts derived from it.
func withPreset(rt *types.RunTelemetry, preset string) *types.RunTelemetry {
	if rt == nil || preset == "" || rt.Preset == preset {
		return rt
	}
	cp := *rt
	cp.Preset = preset
	return &cp
}

// runtimeTelemetry is the telemetry an activity of this executor carries: a
// copy of its context's, so per-node fields (TurnTraceParent) never leak into
// the context.
func (e *StepExecutor) runtimeTelemetry() *types.RunTelemetry {
	if e.execContext == nil || e.execContext.Telemetry == nil {
		return nil
	}
	cp := *e.execContext.Telemetry
	return &cp
}

// upstreamTurnTraceParent is the traceparent of the call_llm turn that
// produced an execute_tools batch, read from that turn's recorded output the
// way upstreamToolCapabilities reads its capabilities: the node the
// tool_calls expression names, else whichever call_llm output in this scope
// carried one of the calls. "" when no turn recorded one.
func (e *StepExecutor) upstreamTurnTraceParent(node *reliantv1.Node, resolved []*reliantv1.ToolCallMsg) string {
	if source := extractToolCallsSourceNode(model.CelStringRaw(model.GetExecuteToolsArgs(node).GetToolCalls())); source != "" {
		if tp := traceParentFromNodeOutput(e.nodeOutputs[source]); tp != "" {
			return tp
		}
	}
	if e.workflow == nil || len(resolved) == 0 {
		return ""
	}
	ids := make(map[string]bool, len(resolved))
	for _, tc := range resolved {
		ids[tc.GetId()] = true
	}
	for _, candidate := range e.workflow.GetNodes() {
		if model.NodeType(candidate) != model.NodeTypeCallLLM {
			continue
		}
		output, ok := e.nodeOutputs[model.NodeID(candidate)].(map[string]interface{})
		if !ok || !outputCarriesToolCall(output, ids) {
			continue
		}
		if tp := traceParentFromNodeOutput(output); tp != "" {
			return tp
		}
	}
	return ""
}

func traceParentFromNodeOutput(output interface{}) string {
	outputMap, ok := output.(map[string]interface{})
	if !ok {
		return ""
	}
	tp, _ := outputMap["trace_parent"].(string)
	return tp
}
