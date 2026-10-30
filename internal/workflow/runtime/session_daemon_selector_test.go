package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The chat's pin (session_daemon_id) reaches an activity marked as a session
// selector, so tool routing can tell a pin that went stale from routing the
// workflow or a node configures. A node's own selector is never a session one.
func TestBuildRuntimeContextMarksTheSessionPin(t *testing.T) {
	e := &StepExecutor{workflowID: "w", chatID: "c", execContext: &ExecutionContext{
		DaemonSelector: &DaemonSelectorValue{ID: "pinned", Session: true},
	}}
	rtx := e.buildRuntimeContext(&reliantv1.Node{Id: "n", Type: "action"})
	require.NotNil(t, rtx.DaemonSelector)
	assert.Equal(t, "pinned", rtx.DaemonSelector.ID)
	assert.True(t, rtx.DaemonSelector.Session, "the session pin stays marked as one")

	configured := &StepExecutor{workflowID: "w", chatID: "c", execContext: &ExecutionContext{
		DaemonSelector: &DaemonSelectorValue{ID: "cicd"},
	}}
	rtx = configured.buildRuntimeContext(&reliantv1.Node{Id: "n", Type: "action"})
	require.NotNil(t, rtx.DaemonSelector)
	assert.False(t, rtx.DaemonSelector.Session, "a workflow-configured selector is not a pin")

	node := &reliantv1.Node{Id: "n", Type: "action", Daemon: &reliantv1.CelDaemonSelector{
		Value: &reliantv1.CelDaemonSelector_Literal{Literal: &reliantv1.DaemonSelectorProto{Id: "gpu"}},
	}}
	rtx = e.buildRuntimeContext(node)
	require.NotNil(t, rtx.DaemonSelector)
	assert.Equal(t, "gpu", rtx.DaemonSelector.ID)
	assert.False(t, rtx.DaemonSelector.Session, "a node's own selector overrides the pin and is not one")
}

// A run node's activity input keeps the session mark too (ExecuteRunStep
// lets only a session pin yield to the workspace owner), and decodes into the
// activity's selector with it.
func TestRunDaemonSelectorMarksTheSessionPin(t *testing.T) {
	run := &reliantv1.Node{Id: "n", Type: "run"}
	pinned := &StepExecutor{workflowID: "w", chatID: "c", execContext: &ExecutionContext{
		DaemonSelector: &DaemonSelectorValue{ID: "pinned", Session: true},
	}}
	var decoded types.DaemonSelector
	raw, err := json.Marshal(pinned.runDaemonSelector(run))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, types.DaemonSelector{ID: "pinned", Session: true}, decoded)

	configured := &StepExecutor{workflowID: "w", chatID: "c", execContext: &ExecutionContext{
		DaemonSelector: &DaemonSelectorValue{ID: "cicd"},
	}}
	assert.NotContains(t, configured.runDaemonSelector(run), "session", "a workflow-configured selector is not a pin")

	node := &reliantv1.Node{Id: "n", Type: "run", Daemon: &reliantv1.CelDaemonSelector{
		Value: &reliantv1.CelDaemonSelector_Literal{Literal: &reliantv1.DaemonSelectorProto{Id: "gpu"}},
	}}
	sel := pinned.runDaemonSelector(node)
	assert.Equal(t, "gpu", sel["id"])
	assert.NotContains(t, sel, "session", "a node's own selector overrides the pin and is not one")

	assert.Nil(t, (&StepExecutor{}).runDaemonSelector(run), "no selector anywhere")
}
