package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGetSystemPrompts_ContainsBasePrompt(t *testing.T) {
	activity := &CallLLMActivity{}
	prompts := activity.getSystemPrompts(nil, "/tmp/project", "", nil, nil, "", nil, "", nil)
	require.NotEmpty(t, prompts)
	prompt := prompts[0]

	require.Contains(t, prompt, "You are Reliant")
}

func TestGetSystemPrompts_UsesStoredMemoriesOverFilesystemFallback(t *testing.T) {
	activity := &CallLLMActivity{}
	cfg := &config.Config{
		GlobalMemoryMD:  "global memory",
		ProjectMemoryMD: "project memory",
	}

	prompts := activity.getSystemPrompts(nil, "/tmp/project", "", cfg, nil, "", nil, "", nil)
	require.NotEmpty(t, prompts)
	prompt := prompts[0]

	require.Contains(t, prompt, "## Global Context\n\nglobal memory")
	require.Contains(t, prompt, "## Project Context\n\nproject memory")
}

func TestFormatStoredMemories_EmptyWhenNoStoredMemories(t *testing.T) {
	require.Equal(t, "", formatStoredMemories(nil))
	require.Equal(t, "", formatStoredMemories(&config.Config{}))
}
