// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/forge/pkg/observe/temporalotel"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// These tests read the spans a run emits from an in-memory exporter, with the
// production tracing wired as the worker wires it: forge's Temporal tracing
// interceptor and the run-scope interceptor that opens node spans. They swap
// the global tracer provider and propagator, so none of them runs in
// parallel.

// recordRunSpans installs an in-memory tracer provider and the W3C
// propagator, and returns the recorder and the worker interceptors a test env
// runs with.
func recordRunSpans(t *testing.T) (*tracetest.SpanRecorder, []interceptor.WorkerInterceptor) {
	t.Helper()
	return recordRunSpansOwnedBy(t, nil)
}

// recordRunSpansOwnedBy is recordRunSpans with the run-scope interceptor
// answering each chat's owner from owners, as the worker's does.
func recordRunSpansOwnedBy(t *testing.T, owners *telemetry.ChatOwnerCache) (*tracetest.SpanRecorder, []interceptor.WorkerInterceptor) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	tracing, err := temporalotel.NewInterceptor(temporalotel.Options{})
	require.NoError(t, err)
	return rec, []interceptor.WorkerInterceptor{tracing, observability.NewRunScopeWorkerInterceptor(owners)}
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value.Emit()
	}
	return out
}

// spanTree indexes ended spans by id, for walking parents.
type spanTree map[trace.SpanID]sdktrace.ReadOnlySpan

func newSpanTree(spans []sdktrace.ReadOnlySpan) spanTree {
	tree := spanTree{}
	for _, s := range spans {
		tree[s.SpanContext().SpanID()] = s
	}
	return tree
}

func (tree spanTree) parent(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	return tree[s.Parent().SpanID()]
}

// ancestor is the nearest ancestor of s whose name has prefix, or nil.
func (tree spanTree) ancestor(s sdktrace.ReadOnlySpan, prefix string) sdktrace.ReadOnlySpan {
	for p := tree.parent(s); p != nil; p = tree.parent(p) {
		if len(p.Name()) >= len(prefix) && p.Name()[:len(prefix)] == prefix {
			return p
		}
	}
	return nil
}

func spansNamed(spans []sdktrace.ReadOnlySpan, prefix string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if len(s.Name()) >= len(prefix) && s.Name()[:len(prefix)] == prefix {
			out = append(out, s)
		}
	}
	return out
}

// startRunTelemetryWorkflow runs startRunTelemetry and returns what it
// decided, the way DynamicWorkflow calls it.
func startRunTelemetryWorkflow(ctx workflow.Context, input WorkflowInput) (*types.RunTelemetry, error) {
	execCtx := input.GetExecContext()
	ctx = startRunTelemetry(ctx, input, execCtx)
	if got := rootRunIDFrom(ctx); got != execCtx.Telemetry.RunID {
		return nil, errors.New("the context's root run id is not the run's")
	}
	return execCtx.Telemetry, nil
}

// The run id is the run's, not the Temporal execution's: a fresh run starts
// one, a continue-as-new successor resuming its predecessor keeps the one it
// carries, a restart from a checkpoint takes the checkpoint's, and a late
// wake's successor — a fresh run in all but mechanism — starts its own.
func TestRunTelemetry_RunIDSurvivesContinueAsNewAndResume(t *testing.T) {
	t.Parallel()
	carried := &types.RunTelemetry{WorkflowName: "agent", RunID: "run-carried", Preset: "general"}
	for name, tc := range map[string]struct {
		input      WorkflowInput
		wantRunID  string
		wantPreset string
	}{
		"fresh run": {
			input:     WorkflowInput{ChatID: "c", WorkflowName: "agent", ExecContext: &ExecutionContext{Thread: "t"}},
			wantRunID: "default-test-run-id",
		},
		"continue-as-new successor": {
			input: WorkflowInput{ChatID: "c", WorkflowName: "agent", Resume: &ResumeInput{NodeID: "agent_loop"},
				ExecContext: &ExecutionContext{Thread: "t", Telemetry: carried}},
			wantRunID: "run-carried", wantPreset: "general",
		},
		"restart from a checkpoint": {
			input: WorkflowInput{ChatID: "c", WorkflowName: "agent", Resume: &ResumeInput{NodeID: "agent_loop", RunID: "run-checkpointed"},
				ExecContext: &ExecutionContext{Thread: "t"}},
			wantRunID: "run-checkpointed",
		},
		"restart from a checkpoint that recorded none": {
			input:     WorkflowInput{ChatID: "c", WorkflowName: "agent", Resume: &ResumeInput{RecoveryOnly: true}, ExecContext: &ExecutionContext{Thread: "t"}},
			wantRunID: "default-test-run-id",
		},
		"late wake successor": {
			input:     WorkflowInput{ChatID: "c", WorkflowName: "agent", ExecContext: &ExecutionContext{Thread: "t", Telemetry: carried}},
			wantRunID: "default-test-run-id", wantPreset: "general",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// A reader outside the run (the reconciler) decides the same id
			// from the start input.
			assert.Equal(t, tc.wantRunID, RootRunID(tc.input, "default-test-run-id"))
			env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			env.RegisterWorkflow(startRunTelemetryWorkflow)
			env.ExecuteWorkflow(startRunTelemetryWorkflow, tc.input)
			require.NoError(t, env.GetWorkflowError())
			var got types.RunTelemetry
			require.NoError(t, env.GetWorkflowResult(&got))
			assert.Equal(t, tc.wantRunID, got.RunID)
			assert.Equal(t, tc.wantRunID, got.RootRunID, "a top-level run is its own root")
			assert.Empty(t, got.ParentRunID)
			assert.Equal(t, "agent", got.WorkflowName)
			assert.Equal(t, "manual", got.Trigger)
			assert.Equal(t, tc.wantPreset, got.Preset)
		})
	}
}

// telemetryThenContinueAsNew decides the run's telemetry, then continues as
// new exactly as the agent loop does past the history limit.
func telemetryThenContinueAsNew(ctx workflow.Context, input WorkflowInput) (*WorkflowResult, error) {
	execCtx := input.GetExecContext()
	ctx = startRunTelemetry(ctx, input, execCtx)
	if input.Resume != nil {
		return &WorkflowResult{}, nil // the successor ends here
	}
	return nil, newContinueAsNewError(ctx, input, "agent_loop", 3, nil, false)
}

// Across a real continue-as-new boundary the successor's workflow span names
// the same run as the predecessor's, and it is on the same trace.
func TestRunTelemetry_ContinueAsNewKeepsTheRunIDOnTheWorkflowSpan(t *testing.T) {
	rec, interceptors := recordRunSpans(t)

	first := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	first.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{Interceptors: interceptors}))
	first.RegisterWorkflow(telemetryThenContinueAsNew)
	first.ExecuteWorkflow(telemetryThenContinueAsNew, WorkflowInput{
		ChatID: "chat-can", WorkflowName: "builtin://agent", ExecContext: &ExecutionContext{Thread: "thread-can"},
	})
	var contErr *workflow.ContinueAsNewError
	require.True(t, errors.As(first.GetWorkflowError(), &contErr), "expected a continue-as-new, got %v", first.GetWorkflowError())
	var successor WorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &successor))
	require.NotNil(t, successor.ExecContext.Telemetry, "the run's telemetry crosses the boundary in the execution context")
	assert.Equal(t, "default-test-run-id", successor.ExecContext.Telemetry.RunID)
	require.NotNil(t, successor.Resume, "a resuming successor, so startRunTelemetry keeps the carried id")

	second := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	second.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{Interceptors: interceptors}))
	second.RegisterWorkflow(telemetryThenContinueAsNew)
	second.ExecuteWorkflow(telemetryThenContinueAsNew, successor)
	require.NoError(t, second.GetWorkflowError())

	runs := spansNamed(rec.Ended(), "RunWorkflow:")
	require.Len(t, runs, 2, "one workflow span per execution")
	ids := map[string]bool{}
	for _, s := range runs {
		a := spanAttrs(s)
		assert.Equal(t, telemetry.GenAIOpInvokeWorkflow, a[telemetry.AttrGenAIOperationName])
		assert.Equal(t, "agent", a[telemetry.AttrAIWorkflowName])
		assert.Equal(t, "builtin://agent", a[telemetry.AttrAIWorkflowRef])
		assert.Equal(t, "chat-can", a[telemetry.AttrGenAIConversationID])
		assert.Equal(t, a[telemetry.AttrAIRunID], a[telemetry.AttrAIRootRunID])
		ids[a[telemetry.AttrAIRunID]] = true
	}
	assert.Equal(t, map[string]bool{"default-test-run-id": true}, ids, "both executions are one run")
}

// A resume of an interrupted run is a new Temporal execution, so the run id
// that names it comes from the checkpoint the interrupted run wrote.
func TestRunTelemetry_CheckpointCarriesTheRunIDToARestart(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	require.NoError(t, store.SaveWorkflowCheckpoint(ctx, repo.ORM(), &core.WorkflowCheckpoint{
		WorkflowID: "wf-run-id", ChatID: "chat-run-id", NodeID: "agent_loop", RunID: "run-interrupted",
	}))
	resume := ResumeInputFromDurableState(ctx, withSpawnTree(repo), "chat-run-id", "wf-run-id")
	assert.Equal(t, "run-interrupted", resume.RunID)

	require.NoError(t, store.SaveWorkflowCheckpoint(ctx, repo.ORM(), &core.WorkflowCheckpoint{
		WorkflowID: "wf-run-id", ChatID: "chat-run-id", NodeID: "agent_loop", LoopIteration: 2, RunID: "run-interrupted",
	}))
	assert.Equal(t, "run-interrupted", ResumeInputFromDurableState(ctx, withSpawnTree(repo), "chat-run-id", "wf-run-id").RunID,
		"a later position of the same run keeps naming it")
}

// The span tree of a run with an agent turn that calls a tool and spawns a
// sub-agent, through the real DynamicWorkflow, StepExecutor and spawn path:
//
//	RunWorkflow:DynamicWorkflow (invoke_workflow, run R)
//	└ StartActivity:CallLLM └ RunActivity:CallLLM
//	  └ workflow.node call_llm
//	    └ invoke_agent general
//	      ├ chat m
//	      └ execute_tool grep   (opened by execute_tools, links its node)
//	… and the spawned agent's nodes and turns on the same trace, as run
//	  <child workflow id> whose parent and root are R.
//
// The activities are the harness's stubs; inside them the spans are opened
// with the helpers the real CallLLM and ExecuteTools use.
func TestRunTelemetry_SpanTree(t *testing.T) {
	owners := telemetry.NewChatOwnerOrgCache(func(context.Context, string) (telemetry.ChatOwner, error) {
		return telemetry.ChatOwner{UserID: "user-tree", OrgID: "org-tree"}, nil
	}, 0, 10)
	rec, interceptors := recordRunSpansOwnedBy(t, owners)
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{Interceptors: interceptors}))

	e := newSpawnE2EEnv(t, env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{
			{"id": "t-grep", "name": "grep", "input": `{"pattern":"x"}`},
			spawnToolCall("tc1", "look into it"),
		}},
	})
	e.onCallLLM = func(ctx context.Context, input types.ActivityInput, out map[string]interface{}) {
		ctx, turn := telemetry.StartAgentTurn(ctx)
		// The provider call, tagged as llm.StartGenAISpan tags it (that
		// package's own test pins it; it cannot be imported from here).
		_, call := otel.Tracer("test").Start(ctx, "chat m", trace.WithAttributes(append(
			[]attribute.KeyValue{attribute.String(telemetry.AttrGenAIOperationName, telemetry.GenAIOpChat)},
			telemetry.RunScopeFromContext(ctx).GenAIAttributes()...)...))
		call.End()
		out["trace_parent"] = turn.TraceParent()
		turn.End(nil)
	}
	turnParents := map[string]string{}
	e.onExecuteTools = func(ctx context.Context, input types.ActivityInput, _ map[string]interface{}) {
		require.NotNil(t, input.Runtime.Telemetry)
		for _, tc := range input.Node.GetExecuteTools().GetResolvedToolCalls() {
			turnParents[tc.GetId()] = input.Runtime.Telemetry.TurnTraceParent
			_, call := telemetry.StartToolCall(ctx, tc.GetName(), tc.GetId(), telemetry.ToolLocationDaemon, input.Runtime.Telemetry.TurnTraceParent)
			call.End(false, nil)
		}
	}
	env.RegisterWorkflow(DynamicWorkflow)
	e.execute("chat-tree")
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.NotEmpty(t, turnParents["t-grep"], "execute_tools was handed the turn that asked for its calls")

	spans := rec.Ended()
	tree := newSpanTree(spans)
	runs := spansNamed(spans, "RunWorkflow:")
	require.Len(t, runs, 1)
	root := runs[0]
	rootAttrs := spanAttrs(root)
	runID := rootAttrs[telemetry.AttrAIRunID]
	require.NotEmpty(t, runID)
	assert.Equal(t, telemetry.GenAIOpInvokeWorkflow, rootAttrs[telemetry.AttrGenAIOperationName])
	assert.Equal(t, "agent", rootAttrs[telemetry.AttrAIWorkflowName])
	// The run's own span names its org, which the workflow learns from its
	// definition load (nameRunOrg): tenant export drops a span without one.
	assert.Equal(t, "org-tree", rootAttrs[telemetry.AttrOrgID], "the run span carries org.id")
	traceID := root.SpanContext().TraceID()

	// Every node span names its run, its org and its chat as the
	// conversation, the save_message the workflow writes itself included.
	nodes := spansNamed(spans, "workflow.node ")
	require.NotEmpty(t, nodes)
	for _, node := range nodes {
		na := spanAttrs(node)
		assert.NotEmpty(t, na[telemetry.AttrAIRunID], node.Name())
		assert.Equal(t, "org-tree", na[telemetry.AttrOrgID], node.Name())
		assert.Equal(t, "chat-tree", na[telemetry.AttrGenAIConversationID], node.Name())
		assert.Equal(t, "chat-tree", na[telemetry.AttrChatID], node.Name())
	}

	var parentTurns, childTurns []sdktrace.ReadOnlySpan
	for _, turn := range spansNamed(spans, "invoke_agent") {
		assert.Equal(t, traceID, turn.SpanContext().TraceID(), "every turn is on the run's trace, the sub-agent's included")
		a := spanAttrs(turn)
		assert.Equal(t, telemetry.GenAIOpInvokeAgent, a[telemetry.AttrGenAIOperationName])
		assert.Equal(t, "chat-tree", a[telemetry.AttrGenAIConversationID])
		node := tree.parent(turn)
		require.NotNil(t, node, "a turn's parent is its node")
		assert.Equal(t, "workflow.node call_llm", node.Name())
		na := spanAttrs(node)
		assert.Equal(t, "call_llm", na[telemetry.AttrAINodeID])
		assert.Equal(t, "call_llm", na[telemetry.AttrAINodeType])
		assert.NotEmpty(t, na[telemetry.AttrAINodeIteration], "the agent loop's iteration")
		assert.Equal(t, a[telemetry.AttrAIRunID], na[telemetry.AttrAIRunID])
		assert.Equal(t, root, tree.ancestor(node, "RunWorkflow:"), "the node hangs off the workflow (through the activity spans)")
		chats := 0
		for _, s := range spans {
			if tree.parent(s) == turn && s.Name() == "chat m" {
				chats++
				ca := spanAttrs(s)
				assert.Equal(t, a[telemetry.AttrAIRunID], ca[telemetry.AttrAIRunID], "the LLM call names the run")
				assert.Equal(t, a[telemetry.AttrGenAIAgentID], ca[telemetry.AttrGenAIAgentID])
				assert.Equal(t, "call_llm", ca[telemetry.AttrAINodeID])
			}
		}
		assert.Equal(t, 1, chats, "the LLM call is the turn's child")
		if a[telemetry.AttrAIRunID] == runID {
			parentTurns = append(parentTurns, turn)
		} else {
			childTurns = append(childTurns, turn)
		}
	}
	require.NotEmpty(t, parentTurns)
	require.NotEmpty(t, childTurns, "the spawned sub-agent took turns")

	// The tool call is the asking turn's child, and links the execute_tools
	// node that ran it.
	tools := spansNamed(spans, "execute_tool grep")
	require.Len(t, tools, 1)
	tool := tools[0]
	ta := spanAttrs(tool)
	assert.Equal(t, telemetry.GenAIOpExecuteTool, ta[telemetry.AttrGenAIOperationName])
	assert.Equal(t, "grep", ta[telemetry.AttrGenAIToolName])
	assert.Equal(t, "t-grep", ta[telemetry.AttrGenAIToolCallID])
	assert.Equal(t, telemetry.ToolLocationDaemon, ta[telemetry.AttrAIToolLocation])
	assert.Equal(t, runID, ta[telemetry.AttrAIRunID])
	asking := tree.parent(tool)
	require.NotNil(t, asking)
	assert.Equal(t, parentTurns[0].SpanContext().SpanID(), asking.SpanContext().SpanID(), "the tool call is under the turn that asked for it")
	require.Len(t, tool.Links(), 1)
	linked := tree[tool.Links()[0].SpanContext.SpanID()]
	require.NotNil(t, linked)
	assert.Equal(t, "workflow.node execute_tools", linked.Name())

	// The sub-agent is a run of its own, named by its workflow id, whose
	// parent and root are the run that spawned it, under its preset.
	ca := spanAttrs(childTurns[0])
	assert.NotEqual(t, runID, ca[telemetry.AttrAIRunID])
	assert.Equal(t, DeterministicWorkflowID("default-test-workflow-id", "tc1"), ca[telemetry.AttrAIRunID])
	assert.Equal(t, runID, ca[telemetry.AttrAIParentRunID])
	assert.Equal(t, runID, ca[telemetry.AttrAIRootRunID])
	assert.Equal(t, "general", ca[telemetry.AttrGenAIAgentName])
	assert.Equal(t, "spawn", ca[telemetry.AttrAIWorkflowTrigger])
	assert.NotEqual(t, spanAttrs(parentTurns[0])[telemetry.AttrGenAIAgentID], ca[telemetry.AttrGenAIAgentID], "the sub-agent is its own agent: its thread")
}

// A run a trigger launched is the root of its own trace (launch.runTraceContext)
// and links its workflow span to the span that fired it, so two orgs' runs of
// one fanned-out event are two traces that each point back at that event. A
// run with no firing span — a person's start — links nothing.
func TestRunTelemetry_TriggeredRunLinksToItsFiringSpan(t *testing.T) {
	rec, interceptors := recordRunSpans(t)
	_, firing := otel.Tracer("test").Start(context.Background(), "trigger.fire integration")
	firing.End()
	traceParent := telemetry.TraceParentOf(firing.SpanContext())

	for _, input := range []WorkflowInput{
		{ChatID: "chat-org-a", WorkflowName: "agent", ExecContext: &ExecutionContext{Thread: "a"},
			Trigger: &TriggerInfo{Kind: string(core.TriggerEventKindIntegration), EventID: "evt-a", TraceParent: traceParent}},
		{ChatID: "chat-org-b", WorkflowName: "agent", ExecContext: &ExecutionContext{Thread: "b"},
			Trigger: &TriggerInfo{Kind: string(core.TriggerEventKindIntegration), EventID: "evt-b", TraceParent: traceParent}},
		{ChatID: "chat-manual", WorkflowName: "agent", ExecContext: &ExecutionContext{Thread: "m"},
			Trigger: &TriggerInfo{Kind: string(core.TriggerEventKindChatStart)}},
	} {
		env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
		env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{Interceptors: interceptors}))
		env.RegisterWorkflow(startRunTelemetryWorkflow)
		env.ExecuteWorkflow(startRunTelemetryWorkflow, input)
		require.NoError(t, env.GetWorkflowError())
	}

	runs := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range spansNamed(rec.Ended(), "RunWorkflow:") {
		runs[spanAttrs(s)[telemetry.AttrGenAIConversationID]] = s
	}
	require.Len(t, runs, 3)
	for _, chat := range []string{"chat-org-a", "chat-org-b"} {
		links := runs[chat].Links()
		require.Len(t, links, 1, "%s links to its firing span", chat)
		assert.Equal(t, firing.SpanContext().TraceID(), links[0].SpanContext.TraceID(), chat)
		assert.Equal(t, firing.SpanContext().SpanID(), links[0].SpanContext.SpanID(), chat)
		assert.NotEqual(t, firing.SpanContext().TraceID(), runs[chat].SpanContext().TraceID(), "%s is not in the firing trace", chat)
	}
	assert.NotEqual(t, runs["chat-org-a"].SpanContext().TraceID(), runs["chat-org-b"].SpanContext().TraceID())
	assert.Empty(t, runs["chat-manual"].Links(), "a person's start links nothing")

	// Each names the trigger_events row it was launched for, as its
	// trigger.fire span does, so the two join by attribute too.
	assert.Equal(t, "evt-a", spanAttrs(runs["chat-org-a"])[telemetry.AttrTriggerEventID])
	assert.Equal(t, "evt-b", spanAttrs(runs["chat-org-b"])[telemetry.AttrTriggerEventID])
	assert.NotContains(t, spanAttrs(runs["chat-manual"]), attribute.Key(telemetry.AttrTriggerEventID), "no event, no attribute")
}
