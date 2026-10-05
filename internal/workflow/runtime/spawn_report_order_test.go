// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"slices"

	"github.com/stretchr/testify/require"
)

// A detached spawn must enqueue its mailbox report BEFORE marking the child
// workflow and the spawn tool call terminal: the reconciler sweep reads
// "terminal child + no report" as stranded, so the opposite order lets it
// write a placeholder that then collides with the real report. See
// docs/incidents/2026-10-04-spawn-report-collision.md.
func (s *SpawnBackgroundE2ESuite) TestBackground_ReportPrecedesTerminalStatus() {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "research something")}},
		{toolCalls: nil},
	})

	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput("chat-report-order"))
	require.True(s.T(), env.IsWorkflowCompleted())
	require.NoError(s.T(), env.GetWorkflowError())

	e.mu.Lock()
	events := slices.Clone(e.events)
	e.mu.Unlock()

	report := slices.Index(events, "report:tc1")
	childDone := slices.Index(events, "child-status:tc1:completed")
	toolDone := slices.Index(events, "tool-status:tc1:completed")
	require.NotEqual(s.T(), -1, report, "events: %v", events)
	require.NotEqual(s.T(), -1, childDone, "events: %v", events)
	require.NotEqual(s.T(), -1, toolDone, "events: %v", events)
	require.Less(s.T(), report, childDone, "report must precede child terminal status; events: %v", events)
	require.Less(s.T(), report, toolDone, "report must precede tool-call terminal status; events: %v", events)
}
