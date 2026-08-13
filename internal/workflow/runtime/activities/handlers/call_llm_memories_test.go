package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

func TestGetSystemPrompts_ContainsBasePrompt(t *testing.T) {
	activity := &CallLLMActivity{}
	prompts := activity.getSystemPrompts(nil, "/tmp/project", "", nil, nil, nil)
	require.NotEmpty(t, prompts)
	prompt := prompts[0]

	require.Contains(t, prompt, "You are Reliant")
}

func TestGetSystemPrompts_DoesNotContainMemories(t *testing.T) {
	activity := &CallLLMActivity{}
	cfg := &config.Config{
		GlobalMemoryMD:  "global memory",
		ProjectMemoryMD: "project memory",
	}

	prompts := activity.getSystemPrompts(nil, "/tmp/project", "", cfg, nil, nil)
	require.NotEmpty(t, prompts)
	prompt := prompts[0]

	require.NotContains(t, prompt, "global memory")
	require.NotContains(t, prompt, "project memory")
	require.NotContains(t, prompt, "User defined rules")
}

func TestFormatStoredMemories_EmptyWhenNoStoredMemories(t *testing.T) {
	require.Equal(t, "", formatStoredMemories(nil))
	require.Equal(t, "", formatStoredMemories(&config.Config{}))
}

func TestFormatStoredMemories_StandaloneFormat(t *testing.T) {
	cfg := &config.Config{
		GlobalMemoryMD:  "global memory",
		ProjectMemoryMD: "project memory",
	}
	result := formatStoredMemories(cfg)

	require.Contains(t, result, "# User defined rules, memories, and context")
	require.Contains(t, result, "## Global Context\n\nglobal memory")
	require.Contains(t, result, "## Project Context\n\nproject memory")
	// Should not start with newlines (standalone format, not appended)
	require.NotEqual(t, '\n', rune(result[0]))
}

func TestFormatStoredMemories_ProducesSystemMessage(t *testing.T) {
	cfg := &config.Config{
		GlobalMemoryMD: "test memory",
	}
	content := formatStoredMemories(cfg)
	require.NotEmpty(t, content)

	msg := message.Message{
		Role:  message.System,
		Parts: []message.ContentPart{message.TextContent{Text: content}},
	}
	require.Equal(t, message.System, msg.Role)
	require.Contains(t, msg.Content().Text, "test memory")
}

func TestFormatRepoMemoryMessages_Empty(t *testing.T) {
	require.Nil(t, formatRepoMemoryMessages(nil))
	require.Nil(t, formatRepoMemoryMessages(map[string]string{}))
	require.Nil(t, formatRepoMemoryMessages(map[string]string{"api": "", "web": "  "}))
}

func TestFormatRepoMemoryMessages_SortedByName(t *testing.T) {
	msgs := formatRepoMemoryMessages(map[string]string{
		"forge":         "forge context",
		"api":           "api context",
		"control-plane": "cp context",
	})
	require.Len(t, msgs, 3)

	// Verify sorted order: api, control-plane, forge
	require.Contains(t, msgs[0].Content().Text, "<system-memory repo=api>")
	require.Contains(t, msgs[0].Content().Text, "api context")
	require.Contains(t, msgs[1].Content().Text, "<system-memory repo=control-plane>")
	require.Contains(t, msgs[1].Content().Text, "cp context")
	require.Contains(t, msgs[2].Content().Text, "<system-memory repo=forge>")
	require.Contains(t, msgs[2].Content().Text, "forge context")

	// All should be system role
	for _, msg := range msgs {
		require.Equal(t, message.System, msg.Role)
	}
}

// A thread that does not know its working directory INVENTS one. Measured
// across two forge-one-shot runs: ten of fifteen spawned units issued reads
// against `/path/to/project/...` — a placeholder neither reliant nor forge
// ever emits — for eighteen File-not-found errors before recovering.
//
// A spawned unit starts with no history, so the system prompt is the only
// place the path appears. It must therefore read as the ANSWER to "where do
// I open this file", not as provenance.
func TestGetSystemPrompts_StatesWorkingDirectoryAsTheProjectRoot(t *testing.T) {
	activity := &CallLLMActivity{}

	t.Run("project path when no worktree", func(t *testing.T) {
		prompt := activity.getSystemPrompts(nil, "/srv/checkout/acme", "", nil, nil, nil)[0]

		require.Contains(t, prompt, "/srv/checkout/acme",
			"the absolute working directory must appear verbatim — it is the only place a spawned unit can learn it")
		require.Contains(t, prompt, "relative path",
			"the prompt must say what relative paths resolve against, or the model guesses a prefix")
		require.Contains(t, prompt, "/path/to/project",
			"naming the placeholder is what makes the instruction actionable — this is the exact string the runs produced")
	})

	t.Run("worktree path wins when the chat is bound to one", func(t *testing.T) {
		prompt := activity.getSystemPrompts(nil, "/srv/checkout/acme", "/srv/worktrees/feature-x", nil, nil, nil)[0]

		require.Contains(t, prompt, "/srv/worktrees/feature-x",
			"a chat bound to a worktree works in the worktree, and the prompt must name that path")
	})
}
