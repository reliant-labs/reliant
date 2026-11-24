// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// fixedEval evaluates on a clock that never moves, so no test but the budget
// test depends on how long evaluation takes on the host running it.
var fixedEval = Evaluator{Now: func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }}

// planScope is rc-train's plan node, finished with no cut (design section
// 13.1), in a loop's second iteration.
func planScope() Scope {
	return Scope{
		Output: map[string]interface{}{
			"cut":     false,
			"hold":    "main_unchanged",
			"trigger": "schedule",
			"detail":  "main has not moved since the last candidate was cut",
			"lines":   []interface{}{"HOTPATH a/b#1 merged ok", "HOTPATH a/b#2 queued wait"},
		},
		Inputs:   map[string]interface{}{"dry_run": false},
		Workflow: &model.WorkflowContext{ID: "wf-1", Name: "rc-train"},
		Node:     model.ObservedNode{ID: "plan", Type: model.NodeTypeCompute, Path: "plan", Attempt: 1, DurationMs: 42},
	}
}

func TestEvaluate_DecisionEventAttributesAndMetric(t *testing.T) {
	t.Parallel()
	block := &reliantv1.NodeObserve{
		Attributes: map[string]string{
			"trigger":  "{{ output.trigger }}",
			"duration": "{{ node.duration_ms }}",
			"dry_run":  "{{ inputs.dry_run }}",
		},
		Events: []*reliantv1.ObserveEvent{{
			Name: "decision",
			Attributes: map[string]string{
				"name":   "cut",
				"value":  "{{ output.cut ? 'cut' : 'no_cut' }}",
				"reason": "{{ output.cut ? output.trigger : output.hold }}",
			},
		}},
		Metrics: []*reliantv1.ObserveMetric{{
			Name:       "rc_train.ticks",
			Kind:       model.ObserveKindCounter,
			Attributes: map[string]string{"decision": "{{ output.cut ? 'cut' : 'no_cut' }}"},
		}},
	}
	res := fixedEval.Evaluate(block, planScope())

	assert.Empty(t, res.Errors)
	assert.False(t, res.Truncated)
	assert.Equal(t, map[string]interface{}{"trigger": "schedule", "duration": int64(42), "dry_run": false}, res.Attributes)
	require.Len(t, res.Events, 1)
	assert.Equal(t, Event{Name: "decision", Attributes: map[string]interface{}{
		"name": "cut", "value": "no_cut", "reason": "main_unchanged",
	}}, res.Events[0])
	require.Len(t, res.Metrics, 1)
	assert.Equal(t, MetricPoint{
		Name: "rc_train.ticks", Kind: model.ObserveKindCounter, Value: 1,
		Attributes: map[string]interface{}{"decision": "no_cut"},
	}, res.Metrics[0])
	assert.True(t, res.HasRecord())
}

// One expression that fails drops its own key, event or metric and nothing
// else, and says which and why in an error the author can read.
func TestEvaluate_AFailingExpressionDropsOnlyItsOwnKey(t *testing.T) {
	t.Parallel()
	block := &reliantv1.NodeObserve{
		Attributes: map[string]string{
			"broken": "{{ output.no_such_field.x }}",
			"fine":   "{{ output.hold }}",
		},
		Events: []*reliantv1.ObserveEvent{
			{Name: "bad_when", When: "output.no_such.x == 1"},
			{Name: "bad_each", Each: "{{ output.hold }}"},
			{Name: "kept"},
		},
		Metrics: []*reliantv1.ObserveMetric{
			{Name: "not_a_number", Kind: model.ObserveKindHistogram, Value: "{{ output.hold }}"},
			{Name: "negative", Kind: model.ObserveKindCounter, Value: "{{ -1 }}"},
			{Name: "ok", Kind: model.ObserveKindGauge, Value: "{{ 2.5 }}"},
		},
	}
	res := fixedEval.Evaluate(block, planScope())

	assert.Equal(t, map[string]interface{}{"fine": "main_unchanged"}, res.Attributes)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "kept", res.Events[0].Name)
	require.Len(t, res.Metrics, 1)
	assert.Equal(t, "ok", res.Metrics[0].Name)
	assert.InDelta(t, 2.5, res.Metrics[0].Value, 0)

	paths := make([]string, 0, len(res.Errors))
	for _, e := range res.Errors {
		assert.Equal(t, telemetry.ObserveErrorEval, e.Kind)
		assert.LessOrEqual(t, len(e.Message), MaxErrorBytes+32, "an error message is bounded")
		paths = append(paths, strings.SplitN(e.Message, ":", 2)[0])
	}
	assert.ElementsMatch(t, []string{
		"attributes.broken", "events[0].when", "events[1].each", "metrics[0].value", "metrics[1].value",
	}, paths)
}

// A block that runs past its budget stops where it is: what was evaluated is
// kept, the rest is skipped and said so, once. The clock is the test's.
func TestEvaluate_ASlowBlockIsCutAtItsBudget(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	tick := 0
	clock := func() time.Time {
		tick++
		return start.Add(time.Duration(tick-1) * 30 * time.Millisecond)
	}
	block := &reliantv1.NodeObserve{
		Attributes: map[string]string{"a": "{{ 1 }}", "b": "{{ 2 }}", "c": "{{ 3 }}"},
		Events:     []*reliantv1.ObserveEvent{{Name: "never"}},
	}
	res := Evaluator{Now: clock}.Evaluate(block, planScope())

	assert.Equal(t, map[string]interface{}{"a": int64(1)}, res.Attributes, "the first expression ran inside the budget")
	assert.True(t, res.Truncated)
	require.Len(t, res.Errors, 1, "the budget is reported once")
	assert.Equal(t, telemetry.ObserveErrorBudget, res.Errors[0].Kind)
	// An event with no expressions costs nothing, so it is still emitted.
	require.Len(t, res.Events, 1)
}

func TestEvaluate_WhenAndEach(t *testing.T) {
	t.Parallel()
	items := make([]interface{}, MaxEachItems+5)
	for i := range items {
		items[i] = fmt.Sprintf("HOTPATH a/b#%d merged ok", i)
	}
	s := planScope()
	s.Output.(map[string]interface{})["many"] = items
	block := &reliantv1.NodeObserve{
		Events: []*reliantv1.ObserveEvent{
			{Name: "skipped", When: "output.cut"},
			{Name: "land", Each: "{{ output.lines }}", Attributes: map[string]string{
				"value": "{{ item.split(' ')[2] }}",
				"pr":    "{{ item.split(' ')[1] }}",
			}},
		},
	}
	res := fixedEval.Evaluate(block, s)
	require.Len(t, res.Events, 2)
	assert.Equal(t, Event{Name: "land", Attributes: map[string]interface{}{"value": "merged", "pr": "a/b#1"}}, res.Events[0])
	assert.Equal(t, Event{Name: "land", Attributes: map[string]interface{}{"value": "queued", "pr": "a/b#2"}}, res.Events[1])
	assert.False(t, res.Truncated)

	// each: emits at most MaxEachItems events, and the node at most
	// model.ObserveMaxEvents.
	capped := fixedEval.Evaluate(&reliantv1.NodeObserve{Events: []*reliantv1.ObserveEvent{{Name: "land", Each: "{{ output.many }}"}}}, s)
	assert.Len(t, capped.Events, model.ObserveMaxEvents)
	assert.True(t, capped.Truncated)
}

// A node's effective block is the workflow's defaults merged with its own, so
// it can hold more than one block may declare: the caps hold it to the same
// counts.
func TestEvaluate_CapsTheMergedBlock(t *testing.T) {
	t.Parallel()
	attrs := map[string]string{}
	for i := 0; i < model.ObserveMaxAttributes+3; i++ {
		attrs[fmt.Sprintf("k%02d", i)] = "{{ 1 }}"
	}
	var metrics []*reliantv1.ObserveMetric
	for i := 0; i < model.ObserveMaxMetrics+2; i++ {
		metrics = append(metrics, &reliantv1.ObserveMetric{Name: fmt.Sprintf("m%d", i), Kind: model.ObserveKindCounter})
	}
	res := fixedEval.Evaluate(&reliantv1.NodeObserve{Attributes: attrs, Metrics: metrics}, planScope())
	assert.Len(t, res.Attributes, model.ObserveMaxAttributes)
	assert.Contains(t, res.Attributes, "k00", "keys are kept in order")
	assert.NotContains(t, res.Attributes, fmt.Sprintf("k%02d", model.ObserveMaxAttributes))
	assert.Len(t, res.Metrics, model.ObserveMaxMetrics)
	assert.True(t, res.Truncated)
}

// What is stored is redacted (credential shapes masked) and bounded, and a
// structured value is stored as its JSON.
func TestStorable_RedactsAndBounds(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("A", 36)
	got, ok := Storable("pushed with " + token).(string)
	require.True(t, ok)
	assert.NotContains(t, got, token, "a credential shape must be masked before it is stored")

	long, ok := Storable(strings.Repeat("é", MaxStoredStringBytes)).(string)
	require.True(t, ok)
	assert.Less(t, len(long), MaxStoredStringBytes+40)
	assert.Contains(t, long, "more bytes]")
	assert.True(t, strings.HasPrefix(long, "é"), "cut on a rune boundary")

	assert.Equal(t, `{"a":[1,2]}`, Storable(map[string]interface{}{"a": []interface{}{1, 2}}))
	assert.Equal(t, int64(3), Storable(3))
	assert.Equal(t, true, Storable(true))
	assert.Nil(t, Storable(nil))
}

func TestEvaluate_NilBlockIsNothing(t *testing.T) {
	t.Parallel()
	res := fixedEval.Evaluate(nil, planScope())
	assert.False(t, res.HasRecord())
	assert.Empty(t, res.Metrics)
}

// A metrics-only block that evaluated cleanly stores nothing; an error is
// worth a row, so the author can see it.
func TestResult_HasRecord(t *testing.T) {
	t.Parallel()
	assert.False(t, Result{Metrics: []MetricPoint{{Name: "x"}}}.HasRecord())
	assert.True(t, Result{Errors: []Error{{Kind: telemetry.ObserveErrorEval}}}.HasRecord())
}
