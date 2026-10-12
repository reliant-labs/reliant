// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPreemptable_TheBlockingWaiters(t *testing.T) {
	assert.True(t, IsPreemptable(ToolShellWait))
	assert.True(t, IsPreemptable(ToolSpawnStatus))
	assert.True(t, IsPreemptable(ToolWaitForMachine))
}

func TestIsPreemptable_EverythingElseIsNot(t *testing.T) {
	for _, name := range []string{ShellToolName, ToolShellOutput, ToolSpawnSend, ToolView, "mcp__serena__find_symbol", "no_such_tool", ""} {
		assert.False(t, IsPreemptable(name), name)
	}
}

// Preemption ends a call the model asked for before it finished. That is only
// harmless for a call with no side effects, so every preemptable tool must also
// be read-only — a write cut short halfway is exactly the loss the flag
// promises cannot happen.
func TestEveryPreemptableToolIsReadOnly(t *testing.T) {
	for _, def := range GetToolRegistry() {
		if !def.Preemptable {
			continue
		}
		assert.True(t, slices.Contains(def.Tags, TagReadOnly),
			"%s is preemptable but not TagReadOnly: preemption cuts a call short, which is only safe for one with no side effects", def.Name)
	}
}

func TestPreemptionFrom_RoundTrips(t *testing.T) {
	assert.Nil(t, PreemptionFrom(context.Background()))
	assert.Nil(t, PreemptionFrom(WithPreemption(context.Background(), nil)))

	preempt := make(chan struct{})
	assert.Equal(t, (<-chan struct{})(preempt), PreemptionFrom(WithPreemption(context.Background(), preempt)))
}
