// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestOneLineError(t *testing.T) {
	t.Parallel()

	// What a sub-agent's exhausted call_llm actually arrives as: Temporal's
	// activity frame around the provider's own error.
	wrapped := errors.New(`activity error (type: CallLLM, scheduledEventID: 117, startedEventID: 118, identity: worker-1): ` +
		`POST "https://api.individual.githubcopilot.com/v1/messages": 400 Bad Request (type: ProviderError, retryable: false)`)

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"strips Temporal bookkeeping", wrapped, `POST "https://api.individual.githubcopilot.com/v1/messages": 400 Bad Request`},
		{"prefers the recognised summary", errors.New(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
			"The AI provider is currently overloaded (Overloaded)"},
		{"keeps only the first line", errors.New("upstream said no\n<html>a whole error page</html>"), "upstream said no"},
		{"nil is still a line", nil, "unknown error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, oneLineError(tt.err))
		})
	}

	t.Run("bounds a huge cause without splitting a rune", func(t *testing.T) {
		got := oneLineError(errors.New(strings.Repeat("é", 2*maxReportedErrorRunes)))
		assert.True(t, utf8.ValidString(got))
		assert.Equal(t, maxReportedErrorRunes+1, utf8.RuneCountInString(got), "the cut plus an ellipsis")
	})
}

// A spawn carried across continue-as-new (or a coarse restart) is relaunched
// through prepareSpawnRelaunch, not prepareSpawnInline. It is the same
// sub-agent, so its exhausted step must still end it alone rather than pause
// the successor run.
func TestSpawnRelaunchStaysASubAgent(t *testing.T) {
	t.Parallel()

	prep := prepareSpawnRelaunch(
		&spawnChildWorkflowConfig{childThread: "agent-X", toolCallID: "tc-1", childWorkflowID: "child-wf", isResumption: true},
		SpawnHandoff{ToolCallID: "tc-1", ParentThread: "parent-thread", ChildThread: "agent-X", ParentWorkflowID: "wf-root"},
		"chat-1", "/project", map[string]interface{}{},
		func(string) *PauseController { return &PauseController{} },
	)

	assert.True(t, prep.pauseCtrl.SubAgent)
}

func TestSpawnFailureReport(t *testing.T) {
	t.Parallel()

	exhausted := &subAgentFailedError{StepID: "call_llm", Reason: "Rate limited by the AI provider", Err: errors.New("429")}
	report := spawnFailureReport("implementer", "agent-7", fmt.Errorf("loop agent_loop failed: %w", exhausted))

	assert.Contains(t, report, "<system>Use agent_id: agent-7 to resume this agent where it stopped.</system>")
	assert.Contains(t, report, `Agent "implementer" failed: Rate limited by the AI provider. It stopped at its call_llm step.`,
		"the exhaustion's own one-line reason wins over the wrapped chain")
	assert.Contains(t, report, `spawn(agent_id="agent-7", prompt=`)
	assert.Contains(t, report, "nothing else in this chat was paused")

	untitled := spawnFailureReport("", "agent-8", errors.New("could not start: no workflow"))
	assert.Contains(t, untitled, `Agent "agent-8" failed: could not start: no workflow.`, "an untitled agent is named by its id")
}
