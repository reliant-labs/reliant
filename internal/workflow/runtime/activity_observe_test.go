// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.temporal.io/sdk/worker"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/telemetry"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfobserve "github.com/reliant-labs/reliant/internal/workflow/observe"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// Rule 3 at the wrapper: a node's observe block is evaluated and recorded by
// the worker that executes the node, and nothing it does fails the node.
// ============================================================================

// observeClockAt is the stored rows' created_at in these tests: fixture data,
// never the wall clock.
var observeClockAt = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// recordingObservationWriter is an ObservationWriter that keeps what it was
// given, and fails or panics when told to.
type recordingObservationWriter struct {
	mu      sync.Mutex
	rows    []*db.NodeObservation
	failErr error
	panics  bool
	outcome db.NodeObservationWrite
}

func (w *recordingObservationWriter) RecordNodeObservation(_ context.Context, obs *db.NodeObservation, maxPerRun int) (db.NodeObservationWrite, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.panics {
		panic("the store blew up")
	}
	if maxPerRun != wfobserve.MaxRowsPerRun {
		return 0, errors.New("unexpected cap")
	}
	if w.failErr != nil {
		return 0, w.failErr
	}
	w.rows = append(w.rows, obs)
	return w.outcome, nil
}

func (w *recordingObservationWriter) stored() []*db.NodeObservation {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*db.NodeObservation(nil), w.rows...)
}

type observeHarness struct {
	*wrapperHarness
	store  *recordingObservationWriter
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

// newObserveHarness runs CallLLM (returning thinkingCallLLMOutput) through a
// wrapper whose activity context carries a recording node span and the run
// scope, with the test's metrics reader, store and clock.
func newObserveHarness(t *testing.T, fn func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error), configure ...func(*ActivityRegistry)) *observeHarness {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	store := &recordingObservationWriter{}
	if fn == nil {
		fn = func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
			return thinkingCallLLMOutput(), nil
		}
	}
	var suite temporaltest.WorkflowTestSuite
	h := &wrapperHarness{
		env:    suite.NewTestActivityEnvironment(),
		repo:   &wrapperTestRepo{},
		writer: &recordingMessageWriter{},
	}
	h.env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	ctx := telemetry.WithRunScope(context.Background(), telemetry.RunScope{WorkflowName: "agent", OrgID: "org-1"})
	ctx, span := tp.Tracer("test").Start(ctx, telemetry.SpanNameNode+" call_llm")
	t.Cleanup(func() { span.End() })
	h.env.SetWorkerOptions(worker.Options{BackgroundActivityContext: ctx})

	registry := NewActivityRegistry(ActivityRegistryDeps{Store: h.repo, Workflows: h.repo})
	registry.SetMessageWriter(h.writer)
	registry.SetObservationWriter(store)
	registry.observeRecorder = wfobserve.NewRecorder(mp.Meter(telemetry.ObserveMeterName))
	registry.observeClock = func() time.Time { return observeClockAt }
	for _, c := range configure {
		c(registry)
	}
	registerWrapped(h.env, registry, "CallLLM", fn)
	return &observeHarness{wrapperHarness: h, store: store, spans: spans, reader: reader}
}

// nodeSpanAttributes ends the node span and returns its attributes.
func (h *observeHarness) nodeSpan(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range h.spans.Started() {
		if s.Name() == telemetry.SpanNameNode+" call_llm" {
			return s
		}
	}
	t.Fatal("no node span")
	return nil
}

func (h *observeHarness) metric(t *testing.T, name string) map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, h.reader.Collect(context.Background(), &rm))
	out := map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[float64]:
				for _, p := range d.DataPoints {
					out[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Value
				}
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					out[p.Attributes.Encoded(attribute.DefaultEncoder())] = float64(p.Value)
				}
			}
		}
	}
	return out
}

func observeInput(block *reliantv1.NodeObserve) types.ActivityInput {
	in := callLLMInput(saveRequest(agentAssistantSave(), nil))
	in.Runtime.Telemetry = &types.RunTelemetry{RunID: "run-1", WorkflowName: "agent"}
	in.Runtime.Observe = &types.ObserveRequest{
		Block:    block,
		Inputs:   map[string]interface{}{"tier": "HIGH"},
		Workflow: model.WorkflowContext{ID: "wf-1", Name: "agent", ChatID: "chat-1"},
		NodeID:   "call_llm",
		NodeType: model.NodeTypeCallLLM,
		NodePath: "agent_loop.call_llm",
	}
	return in
}

func decisionBlock() *reliantv1.NodeObserve {
	return &reliantv1.NodeObserve{
		Attributes: map[string]string{
			"model": "{{ output.model }}",
			"text":  "{{ output.response_text }}",
			"tier":  "{{ inputs.tier }}",
		},
		Events: []*reliantv1.ObserveEvent{{
			Name: "decision",
			Attributes: map[string]string{
				"name":  "turn",
				"value": "{{ size(output.tool_calls) > 0 ? 'tools' : 'answer' }}",
			},
		}},
		Metrics: []*reliantv1.ObserveMetric{{
			Name:       "agent.turns",
			Kind:       model.ObserveKindCounter,
			Attributes: map[string]string{"value": "{{ size(output.tool_calls) > 0 ? 'tools' : 'answer' }}"},
		}},
	}
}

func TestWrapperObserve_RecordsTheRowTheSpanAndTheMetric(t *testing.T) {
	t.Parallel()
	h := newObserveHarness(t, nil)

	out, err := h.run(t, "CallLLM", observeInput(decisionBlock()))
	require.NoError(t, err)
	assert.Equal(t, "Looking at it.", out["response_text"], "the activity's result goes back unchanged")
	_, hasThinking := out["thinking"]
	assert.False(t, hasThinking, "message-only fields are still stripped after observing")

	rows := h.store.stored()
	require.Len(t, rows, 1)
	row := rows[0]
	assert.NotEmpty(t, row.ID)
	assert.Equal(t, "chat-1", row.ChatID)
	assert.Equal(t, "wf-1", row.WorkflowID)
	assert.Equal(t, "run-1", row.RunID)
	assert.Equal(t, "call_llm", row.StepID)
	assert.Equal(t, "agent_loop.call_llm", row.NodePath)
	require.NotNil(t, row.LoopNodeID)
	assert.Equal(t, "agent_loop", *row.LoopNodeID)
	require.NotNil(t, row.LoopIteration)
	assert.Equal(t, int64(2), *row.LoopIteration)
	assert.Equal(t, int32(1), row.Attempt)
	assert.Equal(t, db.NodeObservationSourceWorkflow, row.Source)
	assert.Equal(t, observeClockAt, row.CreatedAt)
	assert.JSONEq(t, `{"model":"claude-test","text":"Looking at it.","tier":"HIGH"}`, row.Attributes)
	assert.JSONEq(t, `[{"name":"decision","attributes":{"name":"turn","value":"tools"}}]`, row.Events)
	assert.JSONEq(t, `[]`, row.Errors)
	assert.False(t, row.Truncated)

	// The node span: codes as they are, free text withheld.
	span := h.nodeSpan(t)
	got := map[string]string{}
	for _, kv := range span.Attributes() {
		got[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, "claude-test", got["reliant.observe.model"])
	assert.Equal(t, "[withheld:14b]", got["reliant.observe.text"])
	assert.Equal(t, "HIGH", got["reliant.observe.tier"])
	var names []string
	for _, ev := range span.Events() {
		names = append(names, ev.Name)
	}
	assert.Contains(t, names, "decision")

	assert.Equal(t, map[string]float64{
		"forge.ai.node.id=call_llm,forge.ai.node.type=call_llm,forge.ai.workflow.name=agent,value=tools": 1,
	}, h.metric(t, "reliant.observed.agent.turns"))
}

// Nothing the observe block does can fail the node: an expression that fails,
// a block that runs out of budget, a store that refuses the row, and a store
// that panics all leave the activity's result as it was.
func TestWrapperObserve_NeverFailsTheNode(t *testing.T) {
	t.Parallel()
	slow := func(r *ActivityRegistry) {
		tick := 0
		r.observeEvaluator = wfobserve.Evaluator{Now: func() time.Time {
			tick++
			return observeClockAt.Add(time.Duration(tick) * time.Second)
		}}
	}
	cases := map[string]struct {
		block     *reliantv1.NodeObserve
		configure func(*ActivityRegistry)
		store     func(*recordingObservationWriter)
		wantRows  int
		wantError string // a substring of the stored row's errors
		wantKind  string // the telemetry.MetricObserveErrors kind counted
	}{
		"an expression that fails": {
			block:     &reliantv1.NodeObserve{Attributes: map[string]string{"x": "{{ output.no_such_field.y }}", "model": "{{ output.model }}"}},
			wantRows:  1,
			wantError: "attributes.x",
			wantKind:  telemetry.ObserveErrorEval,
		},
		"a slow block": {
			block:     decisionBlock(),
			configure: slow,
			wantRows:  1,
			wantError: "budget",
			wantKind:  telemetry.ObserveErrorBudget,
		},
		"a store that fails": {
			block:    decisionBlock(),
			store:    func(w *recordingObservationWriter) { w.failErr = errors.New("connection reset by peer") },
			wantKind: telemetry.ObserveErrorWrite,
		},
		"a store that panics": {
			block:    decisionBlock(),
			store:    func(w *recordingObservationWriter) { w.panics = true },
			wantKind: telemetry.ObserveErrorPanic,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var configure []func(*ActivityRegistry)
			if tc.configure != nil {
				configure = append(configure, tc.configure)
			}
			h := newObserveHarness(t, nil, configure...)
			if tc.store != nil {
				tc.store(h.store)
			}

			out, err := h.run(t, "CallLLM", observeInput(tc.block))
			require.NoError(t, err, "observability must never fail the node")
			assert.Equal(t, "Looking at it.", out["response_text"])
			assert.Len(t, h.writer.messages(), 1, "the node's message is written as before")

			rows := h.store.stored()
			require.Len(t, rows, tc.wantRows)
			if tc.wantError != "" {
				var errs []string
				require.NoError(t, json.Unmarshal([]byte(rows[0].Errors), &errs))
				require.NotEmpty(t, errs)
				assert.Contains(t, errs[0], tc.wantError)
			}
			assert.Equal(t, map[string]float64{"kind=" + tc.wantKind: 1}, h.metric(t, telemetry.MetricObserveErrors))
		})
	}
}

// The block is observed only once its node's message is saved: a save that
// fails fails the activity (so Temporal retries it) before anything is
// observed, so the retry is the only attempt that records.
func TestWrapperObserve_AFailedSaveObservesNothing(t *testing.T) {
	t.Parallel()
	h := newObserveHarness(t, nil, withMessageWriteRetry(10*time.Millisecond, time.Millisecond))
	h.writer.failures = 1 << 30
	h.writer.failErr = errors.New("connection reset by peer")

	_, err := h.run(t, "CallLLM", observeInput(decisionBlock()))
	require.Error(t, err)
	assert.Empty(t, h.store.stored(), "an attempt whose save failed must not record an observation")
	for _, kv := range h.nodeSpan(t).Attributes() {
		assert.NotContains(t, string(kv.Key), telemetry.AttrObservePrefix)
	}
}

func TestWrapperObserve_NoRequestRecordsNothing(t *testing.T) {
	t.Parallel()
	h := newObserveHarness(t, nil)
	_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
	require.NoError(t, err)
	assert.Empty(t, h.store.stored())
}

// A run at its cap of stored rows still emits the node's telemetry; the row
// is counted as dropped.
func TestWrapperObserve_ARunAtItsRowCapStillEmits(t *testing.T) {
	t.Parallel()
	h := newObserveHarness(t, nil)
	h.store.outcome = db.NodeObservationCapped
	_, err := h.run(t, "CallLLM", observeInput(decisionBlock()))
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"reason=row_cap": 1}, h.metric(t, telemetry.MetricObserveDropped))
	assert.NotEmpty(t, h.metric(t, "reliant.observed.agent.turns"))
}

func TestObserveRequestFrom_RunStepFlatMap(t *testing.T) {
	t.Parallel()
	req := &types.ObserveRequest{
		Block:  &reliantv1.NodeObserve{Attributes: map[string]string{"exit": "{{ output.exit_code }}"}},
		NodeID: "lint",
	}
	input := map[string]interface{}{"step_id": "lint", types.RunStepObserveKey: req}
	got, err := observeRequestFrom(input)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "{{ output.exit_code }}", got.Block.GetAttributes()["exit"])
	assert.Equal(t, "lint", got.NodeID)

	none, err := observeRequestFrom(map[string]interface{}{"step_id": "lint"})
	require.NoError(t, err)
	assert.Nil(t, none)
}
