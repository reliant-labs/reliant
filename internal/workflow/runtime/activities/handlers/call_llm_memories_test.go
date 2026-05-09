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
