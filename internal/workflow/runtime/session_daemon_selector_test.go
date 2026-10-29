package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
