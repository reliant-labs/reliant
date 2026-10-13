// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A stopped sub-agent, and the model the user switched it to, outlive a
// continue-as-new: a message after the boundary still resumes it, on that
// model.
func TestStoppedSubAgentAndItsParamsCrossAContinueAsNew(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc1", ParentThread: "root", ChildThread: "agent",
		handoff: SpawnHandoff{ToolCallID: "tc1", ParentThread: "root", ChildThread: "agent", Preset: "implementer"},
	})
	tracker.recordStoppedSpawn("tc1")
	tracker.completeDetachedSpawn("tc1", "root")
	tracker.recordThreadInputOverride("agent", map[string]interface{}{"model": "gpt-5@openai"})

	successor := &ChildWorkflowTracker{}
	successor.seedStoppedSpawns(tracker.stoppedSpawnHandoffs())
	successor.seedThreadInputOverrides(tracker.threadInputOverridesHandoff())

	stopped := successor.stoppedSpawnFor("agent")
	require.NotNil(t, stopped, "the successor must still know the agent can be resumed")
	require.Equal(t, "implementer", stopped.handoff.Preset)
	require.Equal(t, "root", stopped.ParentThread)

	inputs := map[string]interface{}{"model": "claude-opus@anthropic"}
	owned := map[string]bool{}
	successor.applyThreadInputOverrides("agent", inputs, owned)
	require.Equal(t, "gpt-5@openai", inputs["model"], "the user's choice wins over what the thread started with")
	require.True(t, owned["model"], "and is the thread's own, so a global update does not undo it")
}

// Only an agent that stopped on a failure is remembered as resumable: a live
// spawn that completes is never recorded, and recording needs the live entry.
func TestOnlyALiveSpawnCanBeRecordedStopped(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	tracker.recordStoppedSpawn("never-registered")
	require.Nil(t, tracker.stoppedSpawnFor(""))
	require.Empty(t, tracker.stoppedSpawnHandoffs())
}
