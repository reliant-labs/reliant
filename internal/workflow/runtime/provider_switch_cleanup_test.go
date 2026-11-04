// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Old calls can finish after a provider switch. Their cleanup acknowledgement
// belongs to the dispatch epoch, not whichever provider is current then.
func TestProviderSwitchPinCleanupKeyIncludesRoutingEpoch(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{chatProvider: "codex", providerUpdates: 1}
	executor := &StepExecutor{childTracker: tracker, execContext: &ExecutionContext{Thread: "agent"}}
	first := executor.providerPinsKey()
	tracker.providerUpdates++
	second := executor.providerPinsKey()
	require.NotEqual(t, first, second)
	tracker.providerPinCleanups = map[string]bool{first: true}
	require.False(t, tracker.providerPinCleanups[second], "an old in-flight completion cannot skip the new switch's cleanup")
}
