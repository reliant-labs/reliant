// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/telemetry"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfobserve "github.com/reliant-labs/reliant/internal/workflow/observe"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// Nodes the workflow completes itself are observed by the
// RecordNodeObservation local activity (workflow_observe.go), once, and a
// recording that fails never changes what the run does.
// ============================================================================

// observeCapture is a RecordNodeObservation that keeps what the workflow
// handed it, and fails or panics when told to.
type observeCapture struct {
	mu     sync.Mutex
	inputs []types.RecordNodeObservationInput
	err    error
	panics bool
}

func (c *observeCapture) record(_ context.Context, in types.RecordNodeObservationInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, in)
	if c.panics {
		panic("the recorder blew up")
	}
	return c.err
}

func (c *observeCapture) recorded() []types.RecordNodeObservationInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]types.RecordNodeObservationInput(nil), c.inputs...)
}

// paths lists the recorded nodes as "<node_path>@<loop_iteration>", sorted.
func (c *observeCapture) paths() []string {
	var out []string
	for _, in := range c.recorded() {
		p := in.NodePath
		if in.LoopNodeID != "" {
			p += "@" + string(rune('0'+in.LoopIteration))
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (c *observeCapture) one(t *testing.T, path string) types.RecordNodeObservationInput {
	t.Helper()
	var found []types.RecordNodeObservationInput
	for _, in := range c.recorded() {
		if in.NodePath == path {
			found = append(found, in)
		}
	}
	require.Len(t, found, 1, "want exactly one recording of %s", path)
	return found[0]
}

// workflowNodesYAML has one of each node kind the workflow completes itself.
// The workflow's own observe default applies to every top-level node; the
// loop body and the inline child are other graphs, which it does not reach:
// the body's tick declares its own, the child's inner declares none.
const workflowNodesYAML = `
name: observe-nodes
entry: [plan, probe]
observe:
  attributes:
    scope: top
outputs:
  child_result: "{{ nodes.child.result }}"
  iterations: "{{ nodes.rounds._iterations }}"
nodes:
  - id: plan
    type: compute
    outputs:
      cut: "{{ false }}"
      hold: main_unchanged
    observe:
      events:
        - name: decision
          attributes:
            name: cut
            value: "{{ output.cut ? 'cut' : 'no_cut' }}"
            reason: "{{ output.hold }}"
  - id: probe
    type: compute
    outputs:
      ok: "{{ true }}"
  - id: both
    type: join
  - id: rounds
    type: loop
    while: iter.iteration < 2
    inline:
      entry: [tick]
      nodes:
        - id: tick
          type: compute
          outputs:
            i: "{{ iter.iteration }}"
          observe:
            attributes:
              i: "{{ output.i }}"
  - id: child
    type: workflow
    inline:
      name: child
      entry: [inner]
      outputs:
        result: "{{ nodes.inner.v }}"
      nodes:
        - id: inner
          type: compute
          outputs:
            v: done
edges:
  - from: plan
    default: both
  - from: probe
    default: both
  - from: both
    default: rounds
  - from: rounds
    default: child
`

// runObserved runs source as the run's workflow with capture registered as
// RecordNodeObservation (nil: not registered at all), and returns its result.
func runObserved(t *testing.T, chatID, source string, capture *observeCapture, configure ...func(*testsuite.TestWorkflowEnvironment)) WorkflowResult {
	t.Helper()
	var suite SpawnBackgroundE2ESuite
	env := suite.NewTestWorkflowEnvironment()
	newSpawnE2EEnv(t, env, nil)
	registerAmbientWorkflow(t, env, source)
	if capture != nil {
		env.RegisterActivityWithOptions(capture.record, activity.RegisterOptions{Name: types.RecordNodeObservationActivityName})
	}
	for _, c := range configure {
		c(env)
	}
	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput(chatID))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	return result
}

func TestWorkflowObserve_RecordsEveryNodeTheWorkflowCompletes(t *testing.T) {
	t.Parallel()
	capture := &observeCapture{}
	result := runObserved(t, "chat-observe-nodes", workflowNodesYAML, capture)
	assert.Equal(t, "done", result.Outputs["child_result"])

	// Every top-level node (the workflow's default), each tick (its own),
	// never the child's inner (no block of its own; defaults do not reach
	// into another graph).
	iterations := 0
	for _, in := range capture.recorded() {
		if in.StepID == "tick" {
			iterations++
		}
	}
	require.Positive(t, iterations)
	want := []string{"both", "child", "plan", "probe", "rounds"}
	for i := 0; i < iterations; i++ {
		want = append(want, "rounds.tick@"+string(rune('0'+i)))
	}
	sort.Strings(want)
	assert.Equal(t, want, capture.paths())

	plan := capture.one(t, "plan")
	assert.Equal(t, model.NodeTypeCompute, plan.NodeType)
	assert.Equal(t, "chat-observe-nodes", plan.ChatID)
	assert.NotEmpty(t, plan.WorkflowID)
	assert.Equal(t, int32(1), plan.Attempt, "the workflow completed the node once: one row, whatever retries the recording")
	assert.Equal(t, false, plan.Output["cut"])
	assert.Equal(t, "main_unchanged", plan.Output["hold"])
	require.NotNil(t, plan.Observe)
	require.NotNil(t, plan.Observe.Block)
	assert.Equal(t, "top", plan.Observe.Block.GetAttributes()["scope"], "the workflow's default is merged in")
	require.Len(t, plan.Observe.Block.GetEvents(), 1)
	assert.Equal(t, "plan", plan.Observe.NodeID)
	assert.Equal(t, model.NodeTypeCompute, plan.Observe.NodeType)

	join := capture.one(t, "both")
	assert.Equal(t, model.NodeTypeJoin, join.NodeType)
	var sources []interface{}
	for _, src := range join.Output["_sources"].([]map[string]interface{}) {
		sources = append(sources, src["id"])
	}
	assert.ElementsMatch(t, []interface{}{"plan", "probe"}, sources, "the join's own output")

	loop := capture.one(t, "rounds")
	assert.Equal(t, model.NodeTypeLoop, loop.NodeType)
	assert.EqualValues(t, iterations, loop.Output["_iterations"])
	assert.Empty(t, loop.LoopNodeID, "the loop node itself is not inside its own loop")

	for _, in := range capture.recorded() {
		if in.StepID != "tick" {
			continue
		}
		assert.Equal(t, "rounds", in.LoopNodeID)
		require.NotNil(t, in.Observe.Iter, "a node in a loop body sees iter")
		assert.Equal(t, in.LoopIteration, in.Observe.Iter.Iteration)
		assert.Empty(t, in.Observe.Block.GetAttributes()["scope"], "the top-level default does not reach the loop body")
	}

	child := capture.one(t, "child")
	assert.Equal(t, model.NodeTypeWorkflow, child.NodeType)
	assert.Equal(t, "done", child.Output["result"], "the output the graph sees as nodes.child")
}

// A run that began before the release replays with no marker: no node is
// recorded and the run does exactly what it did.
func TestWorkflowObserve_BeforeTheGate_RecordsNothing(t *testing.T) {
	t.Parallel()
	capture := &observeCapture{}
	result := runObserved(t, "chat-observe-ungated", workflowNodesYAML, capture, func(env *testsuite.TestWorkflowEnvironment) {
		env.OnGetVersion(observeWorkflowNodesChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	})
	assert.Empty(t, capture.recorded())
	assert.Equal(t, "done", result.Outputs["child_result"])
}

// Nothing the recording does can fail the node: a recording that errors,
// that panics, or that is not registered on the worker at all leaves the
// run's result as it was.
func TestWorkflowObserve_ARecordingThatFailsNeverFailsTheNode(t *testing.T) {
	t.Parallel()
	baseline := runObserved(t, "chat-observe-baseline", workflowNodesYAML, &observeCapture{})
	for name, capture := range map[string]*observeCapture{
		"errors":         {err: errors.New("the store is down")},
		"panics":         {panics: true},
		"not registered": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := runObserved(t, "chat-observe-"+name, workflowNodesYAML, capture)
			assert.Equal(t, baseline.Outputs, result.Outputs)
			if capture != nil {
				assert.NotEmpty(t, capture.recorded(), "the recording was attempted")
			}
		})
	}
}

// A router completes in the workflow too: its own output (the selection) is
// recorded, and the node it routed to, an activity, is not recorded here.
func TestWorkflowObserve_RecordsARouter(t *testing.T) {
	t.Parallel()
	capture := &observeCapture{}
	runObserved(t, "chat-observe-router", `
name: observe-router
entry: [classify]
nodes:
  - id: classify
    type: router
    model: mock-model
    nodes:
      - id: draft
        description: approved draft
    observe:
      events:
        - name: decision
          attributes:
            name: route
            value: "{{ output.selected_node }}"
  - id: draft
    type: call_llm
    observe:
      attributes:
        turn: answer
`, capture, func(env *testsuite.TestWorkflowEnvironment) {
		env.RegisterActivityWithOptions(func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			if input.Node.GetCallLlm().GetResponseTool() != nil {
				return map[string]interface{}{"response_text": `{"selected_node":"draft","reasoning":"approved"}`}, nil
			}
			return map[string]interface{}{"tool_calls": []interface{}{}}, nil
		}, activity.RegisterOptions{Name: "CallLLM"})
	})

	got := capture.recorded()
	require.Len(t, got, 1, "the router only: draft is an activity, observed by its wrapper")
	assert.Equal(t, "classify", got[0].StepID)
	assert.Equal(t, model.NodeTypeRouter, got[0].NodeType)
	assert.Equal(t, "draft", got[0].Output["selected_node"])
}

// A node without an observe block, and a workflow without defaults, never
// asks for the gate: no marker, no command.
func TestWorkflowObserve_NoBlockNoCommand(t *testing.T) {
	t.Parallel()
	capture := &observeCapture{}
	asked := false
	runObserved(t, "chat-observe-none", `
name: observe-none
entry: [plan]
nodes:
  - id: plan
    type: compute
    outputs:
      cut: "{{ true }}"
`, capture, func(env *testsuite.TestWorkflowEnvironment) {
		env.OnGetVersion(observeWorkflowNodesChangeID, workflow.DefaultVersion, 1).
			Run(func(mock.Arguments) { asked = true }).Return(workflow.Version(1))
	})
	assert.Empty(t, capture.recorded())
	assert.False(t, asked, "the gate is asked only at a node with a block")
}

// ----------------------------------------------------------------------------
// The StepExecutor path: who observes an activity-backed node
// ----------------------------------------------------------------------------

// newObservedDelegationHarness is the save-delegation harness with the real
// observation path on both sites: the wrapped activities observe through the
// registry, and RecordNodeObservation is the registry's own local activity.
func newObservedDelegationHarness(t *testing.T, executeTools ...func(context.Context, types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error)) (*delegationHarness, *recordingObservationWriter) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	h := &delegationHarness{env: suite.NewTestWorkflowEnvironment(), writer: &recordingMessageWriter{}}
	h.env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	registerFinalizeCapture(h.env)

	store := &recordingObservationWriter{}
	registry := NewActivityRegistry(ActivityRegistryDeps{Store: &wrapperTestRepo{}, Workflows: &wrapperTestRepo{}})
	registry.SetMessageWriter(h.writer)
	registry.SetObservationWriter(store)
	registry.observeRecorder = wfobserve.NewRecorder(sdkmetric.NewMeterProvider().Meter(telemetry.ObserveMeterName))
	registry.observeClock = func() time.Time { return observeClockAt }
	registerWrapped(h.env, registry, "CallLLM",
		func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
			return thinkingCallLLMOutput(), nil
		})
	tools := func(_ context.Context, in types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
		var results []*reliantv1.ToolResultMsg
		for _, tc := range in.Node.GetExecuteTools().GetResolvedToolCalls() {
			results = append(results, &reliantv1.ToolResultMsg{ToolCallId: tc.GetId(), Name: tc.GetName(), Content: "file contents"})
		}
		return &reliantv1.ExecuteToolsOutput{ToolResults: results}, nil
	}
	if len(executeTools) > 0 {
		tools = executeTools[0]
	}
	registerWrapped(h.env, registry, "ExecuteTools", tools)
	h.env.RegisterActivityWithOptions(registry.recordNodeObservation,
		activity.RegisterOptions{Name: types.RecordNodeObservationActivityName})
	return h, store
}

func observedTools(block *reliantv1.NodeObserve) *reliantv1.Node {
	return &reliantv1.Node{
		Id:      "execute_tools",
		Type:    model.NodeTypeExecuteTools,
		Observe: block,
		Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
			ToolCalls: celExpr("{{nodes.call_llm.tool_calls}}"),
		}},
	}
}

func resultsBlock() *reliantv1.NodeObserve {
	return &reliantv1.NodeObserve{Attributes: map[string]string{"results": "{{ size(output.tool_results) }}"}}
}

// An activity that succeeded observes its own node; the workflow does not
// observe it again.
func TestWorkflowObserve_AnActivityNodeIsObservedOnceByItsWrapper(t *testing.T) {
	t.Parallel()
	h, store := newObservedDelegationHarness(t)
	h.env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
		executor := newDelegationExecutor(ctx, map[string]interface{}{})
		llm := callLLMTestNode("call_llm")
		llm.Observe = &reliantv1.NodeObserve{Attributes: map[string]string{"model": "{{ output.model }}"}}
		return runStep(executor, llm).Error
	})
	require.NoError(t, h.env.GetWorkflowError())
	rows := store.stored()
	require.Len(t, rows, 1, "observed once, by the activity")
	assert.JSONEq(t, `{"model":"claude-test"}`, rows[0].Attributes)
}

// A batch with an ask_user call is assembled by the workflow, so the workflow
// observes it, over the merged output; a batch the activity returned whole is
// observed by the activity. Each exactly once.
func TestWorkflowObserve_ABatchTheWorkflowAssemblesIsObservedByTheWorkflow(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		calls       []interface{}
		wantResults string
	}{
		"assembled by the workflow": {
			calls: []interface{}{
				map[string]interface{}{"id": "tc1", "name": "view", "input": `{"file_path":"a.go"}`},
				map[string]interface{}{"id": "tc2", "name": "ask_user", "input": `{"questions":[]}`},
			},
			wantResults: `{"results":2}`,
		},
		"returned by the activity": {
			calls: []interface{}{
				map[string]interface{}{"id": "tc1", "name": "view", "input": `{"file_path":"a.go"}`},
			},
			wantResults: `{"results":1}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, store := newObservedDelegationHarness(t)
			h.env.ExecuteWorkflow(func(ctx workflow.Context) error {
				executor := newDelegationExecutor(ctx, map[string]interface{}{InputKeyUnattended: true})
				executor.nodeOutputs["call_llm"] = map[string]interface{}{"tool_calls": tc.calls}
				return runStep(executor, observedTools(resultsBlock())).Error
			})
			require.NoError(t, h.env.GetWorkflowError())
			rows := store.stored()
			require.Len(t, rows, 1, "observed once, by whoever produced the node's output")
			assert.JSONEq(t, tc.wantResults, rows[0].Attributes, "over the node's whole output")
			assert.Equal(t, "execute_tools", rows[0].StepID)
			assert.Equal(t, int32(1), rows[0].Attempt)
		})
	}
}

// A tolerated failure of an activity that failed is observed by the workflow,
// over the failure's outputs: the activity observed nothing.
func TestWorkflowObserve_AToleratedFailureIsObservedByTheWorkflow(t *testing.T) {
	t.Parallel()
	h, store := newObservedDelegationHarness(t,
		func(context.Context, types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
			return nil, temporal.NewNonRetryableApplicationError("tools refused", "ToolsRefused", nil)
		})
	h.env.ExecuteWorkflow(func(ctx workflow.Context) error {
		executor := newDelegationExecutor(ctx, map[string]interface{}{InputKeyUnattended: true})
		executor.nodeOutputs["call_llm"] = map[string]interface{}{"tool_calls": []interface{}{
			map[string]interface{}{"id": "tc1", "name": "view", "input": `{"file_path":"a.go"}`},
		}}
		node := observedTools(&reliantv1.NodeObserve{Events: []*reliantv1.ObserveEvent{{
			Name:       "decision",
			Attributes: map[string]string{"name": "tools", "value": "{{ output.?failed.orValue(false) ? 'failed' : 'ran' }}"},
		}}})
		node.TolerateFailure = true
		ev := runStep(executor, node)
		if ev.Error != nil {
			return ev.Error
		}
		if ev.Data["failed"] != true {
			return errors.New("want the tolerated failure's outputs")
		}
		return nil
	})
	require.NoError(t, h.env.GetWorkflowError())
	rows := store.stored()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `[{"name":"decision","attributes":{"name":"tools","value":"failed"}}]`, rows[0].Events)
}

// ----------------------------------------------------------------------------
// The local activity itself
// ----------------------------------------------------------------------------

func recordInput() types.RecordNodeObservationInput {
	return types.RecordNodeObservationInput{
		ChatID:        "chat-1",
		WorkflowID:    "wf-1",
		StepID:        "plan",
		NodeType:      model.NodeTypeCompute,
		NodePath:      "attempt.plan",
		LoopNodeID:    "attempt",
		LoopIteration: 2,
		Telemetry:     &types.RunTelemetry{RunID: "run-child", RootRunID: "run-root", WorkflowName: "rc-train"},
		Attempt:       1,
		DurationMs:    7,
		Observe: &types.ObserveRequest{
			Block: &reliantv1.NodeObserve{
				Attributes: map[string]string{"took": "{{ node.duration_ms }}"},
				Events: []*reliantv1.ObserveEvent{{
					Name: "decision",
					Attributes: map[string]string{
						"name":   "cut",
						"value":  "{{ output.cut ? 'cut' : 'no_cut' }}",
						"reason": "{{ output.hold }}",
					},
				}},
				Metrics: []*reliantv1.ObserveMetric{{
					Name:       "rc_train.ticks",
					Kind:       model.ObserveKindCounter,
					Attributes: map[string]string{"decision": "{{ output.cut ? 'cut' : 'no_cut' }}"},
				}},
			},
			Workflow: model.WorkflowContext{ID: "wf-1", Name: "rc-train", ChatID: "chat-1"},
			NodeID:   "plan",
			NodeType: model.NodeTypeCompute,
			NodePath: "attempt.plan",
		},
		Output: map[string]interface{}{"cut": false, "hold": "main_unchanged"},
	}
}

// recordHarness runs the registry's RecordNodeObservation as a local
// activity, the way the workflow does, on a worker with the production
// run-scope interceptor: the interceptor opens the node's span for it.
type recordHarness struct {
	env    *testsuite.TestWorkflowEnvironment
	store  *recordingObservationWriter
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newRecordHarness(t *testing.T, store *recordingObservationWriter) *recordHarness {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	// The interceptor's node span comes from the global provider
	// (telemetry.StartNodeSpan): this test owns it while it runs.
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	registry := NewActivityRegistry(ActivityRegistryDeps{Store: &wrapperTestRepo{}, Workflows: &wrapperTestRepo{}})
	if store != nil {
		registry.SetObservationWriter(store)
	}
	registry.observeRecorder = wfobserve.NewRecorder(mp.Meter(telemetry.ObserveMeterName))
	registry.observeClock = func() time.Time { return observeClockAt }

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{observability.NewRunScopeWorkerInterceptor(nil)},
	})
	env.RegisterActivityWithOptions(registry.recordNodeObservation,
		activity.RegisterOptions{Name: types.RecordNodeObservationActivityName})
	return &recordHarness{env: env, store: store, spans: spans, reader: reader}
}

func (h *recordHarness) run(t *testing.T, in types.RecordNodeObservationInput) error {
	t.Helper()
	h.env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: time.Minute})
		return workflow.ExecuteLocalActivity(ctx, types.RecordNodeObservationActivityName, in).Get(ctx, nil)
	})
	require.True(t, h.env.IsWorkflowCompleted())
	return h.env.GetWorkflowError()
}

// The design left this to verify: Temporal hands worker interceptors to local
// activities, so the run-scope interceptor opens the workflow.node span for
// the recording, and the observation lands on it.
func TestRecordNodeObservation_TheRunScopeInterceptorOpensTheNodeSpan(t *testing.T) {
	store := &recordingObservationWriter{}
	h := newRecordHarness(t, store)
	require.NoError(t, h.run(t, recordInput()))

	var span sdktrace.ReadOnlySpan
	for _, s := range h.spans.Ended() {
		if s.Name() == telemetry.SpanNameNode+" plan" {
			span = s
		}
	}
	require.NotNil(t, span, "the interceptor opened the node span for the local activity")
	got := map[string]string{}
	for _, kv := range span.Attributes() {
		got[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, "true", got[telemetry.AttrNodeRecorded], "the span says it timed the recording, not the node")
	assert.Equal(t, "7", got["reliant.observe.took"], "node.duration_ms is the node's own")
	assert.Equal(t, "chat-1", got[telemetry.AttrGenAIConversationID])
	assert.Equal(t, "plan", got[telemetry.AttrAINodeID])
	var events []string
	for _, ev := range span.Events() {
		events = append(events, ev.Name)
	}
	assert.Contains(t, events, "decision")
}

func TestRecordNodeObservation_StoresTheRowAndEmitsTheMetric(t *testing.T) {
	store := &recordingObservationWriter{}
	h := newRecordHarness(t, store)
	require.NoError(t, h.run(t, recordInput()))

	rows := store.stored()
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "chat-1", row.ChatID)
	assert.Equal(t, "wf-1", row.WorkflowID)
	assert.Equal(t, "run-root", row.RunID, "the root run, as an activity node's row")
	assert.Equal(t, "plan", row.StepID)
	assert.Equal(t, "attempt.plan", row.NodePath)
	require.NotNil(t, row.LoopNodeID)
	assert.Equal(t, "attempt", *row.LoopNodeID)
	require.NotNil(t, row.LoopIteration)
	assert.Equal(t, int64(2), *row.LoopIteration)
	assert.Equal(t, int32(1), row.Attempt)
	assert.Equal(t, db.NodeObservationSourceWorkflow, row.Source)
	assert.Equal(t, observeClockAt, row.CreatedAt)
	assert.JSONEq(t, `{"took":7}`, row.Attributes)
	assert.JSONEq(t, `[{"name":"decision","attributes":{"name":"cut","value":"no_cut","reason":"main_unchanged"}}]`, row.Events)

	oh := &observeHarness{reader: h.reader}
	assert.Equal(t, map[string]float64{
		"decision=no_cut,forge.ai.node.id=plan,forge.ai.node.type=compute,forge.ai.workflow.name=rc-train": 1,
	}, oh.metric(t, "reliant.observed.rc_train.ticks"))
}

// The local activity never returns an error: a store that fails or panics,
// no store, and a block whose expressions fail all end it cleanly.
func TestRecordNodeObservation_NeverFails(t *testing.T) {
	failing := recordInput()
	failing.Observe.Block.Attributes["broken"] = "{{ output.no_such_key.x }}"
	for name, tc := range map[string]struct {
		store *recordingObservationWriter
		in    types.RecordNodeObservationInput
	}{
		"store fails":      {store: &recordingObservationWriter{failErr: errors.New("db down")}, in: recordInput()},
		"store panics":     {store: &recordingObservationWriter{panics: true}, in: recordInput()},
		"no store":         {in: recordInput()},
		"expression fails": {store: &recordingObservationWriter{}, in: failing},
		"no request":       {store: &recordingObservationWriter{}, in: types.RecordNodeObservationInput{StepID: "plan", NodeType: model.NodeTypeCompute}},
		"skipped node":     {store: &recordingObservationWriter{}, in: skipped(recordInput())},
	} {
		t.Run(name, func(t *testing.T) {
			h := newRecordHarness(t, tc.store)
			assert.NoError(t, h.run(t, tc.in))
		})
	}
}

func skipped(in types.RecordNodeObservationInput) types.RecordNodeObservationInput {
	in.Output = map[string]interface{}{model.SkippedOutputField: true}
	return in
}
