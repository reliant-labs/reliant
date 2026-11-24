// Copyright (c) 2026 Reliant Labs
package rundiagnostics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func storedObservation(step string) *core.NodeObservation {
	loop, iteration := "sweep", int64(2)
	return &core.NodeObservation{
		ID: "o-" + step, ChatID: "chat", WorkflowID: "wf", RunID: "run", StepID: step, NodePath: "sweep." + step,
		LoopNodeID: &loop, LoopIteration: &iteration, Attempt: 2, Source: core.NodeObservationSourceWorkflow,
		Attributes: `{"trigger":"schedule","count":14,"held":true}`,
		Events:     `[{"name":"decision","attributes":{"name":"cut","value":"no_cut","reason":"main_unchanged"}},{"name":"tick"}]`,
		Errors:     `["events[1].attributes.x: no such key: missing"]`,
		Truncated:  true,
		CreatedAt:  time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
	}
}

func TestObservationProjectsTheStoredRow(t *testing.T) {
	t.Parallel()
	o := Observation(storedObservation("plan"))
	assert.Equal(t, "plan", o.GetNodeId())
	assert.Equal(t, "sweep.plan", o.GetNodePath())
	assert.Equal(t, "wf", o.GetWorkflowId())
	assert.Equal(t, "sweep", o.GetLoopNodeId())
	assert.Equal(t, int32(2), o.GetLoopIteration())
	assert.Equal(t, int32(2), o.GetAttempt())
	assert.Equal(t, "workflow", o.GetSource())
	assert.True(t, o.GetTruncated())
	assert.Equal(t, "2026-11-24T12:00:00Z", o.GetCreatedAt())
	assert.Equal(t, map[string]interface{}{"trigger": "schedule", "count": float64(14), "held": true}, o.GetAttributes().AsMap())
	require.Len(t, o.GetEvents(), 2)
	assert.Equal(t, "decision", o.GetEvents()[0].GetName())
	assert.Equal(t, map[string]interface{}{"name": "cut", "value": "no_cut", "reason": "main_unchanged"}, o.GetEvents()[0].GetAttributes().AsMap())
	assert.Equal(t, "tick", o.GetEvents()[1].GetName())
	assert.Nil(t, o.GetEvents()[1].GetAttributes())
	assert.Equal(t, []string{"events[1].attributes.x: no such key: missing"}, o.GetErrors())
}

// A row whose JSON does not read back still says which node it was, and that
// something could not be read, instead of failing the run's page.
func TestObservationOfADamagedRowKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	row := storedObservation("plan")
	row.Attributes, row.Events, row.Errors = `{not json`, `[`, `nope`
	o := Observation(row)
	assert.Equal(t, "plan", o.GetNodeId())
	assert.Nil(t, o.GetAttributes())
	assert.Empty(t, o.GetEvents())
	assert.Equal(t, []string{
		"the recorded errors could not be read",
		"the recorded attributes could not be read",
		"the recorded events could not be read",
	}, o.GetErrors())
}

func TestObservationsStopAtTheirBudget(t *testing.T) {
	t.Parallel()
	rows := []*core.NodeObservation{storedObservation("a"), nil, storedObservation("b"), storedObservation("c")}
	one := len(rows[0].Attributes) + len(rows[0].Events) + len(rows[0].Errors)

	all, truncated := Observations(rows, 3*one)
	assert.False(t, truncated)
	assert.Len(t, all, 3, "a nil row is skipped")

	some, truncated := Observations(rows, 2*one+1)
	assert.True(t, truncated)
	require.Len(t, some, 2)
	assert.Equal(t, "b", some[1].GetNodeId(), "in the order given (newest first from the store)")
}
