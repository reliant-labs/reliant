// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/idlereclaim"
)

// A sub-agent gets a scratch subdirectory of its own, named by its agent id,
// so siblings writing the same file name (pr-body.md) no longer overwrite each
// other. It is still told the chat's directory, where handoffs live.
func TestSubAgentScratchNote_GivesEachSubAgentItsOwnDirectory(t *testing.T) {
	t.Parallel()
	chat := &db.Chat{ID: "chat-123"}

	a := subAgentScratchNote(chat, "agent-a", 1, true)
	b := subAgentScratchNote(chat, "agent-b", 2, true)
	assert.Contains(t, a, "~/.reliant/scratch/chat-123/agent-a/")
	assert.Contains(t, b, "~/.reliant/scratch/chat-123/agent-b/")
	assert.NotContains(t, a, "agent-b")
	for _, note := range []string{a, b} {
		assert.Contains(t, note, "share its scratch directory, ~/.reliant/scratch/chat-123/", "the parent's directory stays reachable for handoffs")
		assert.Contains(t, note, "mkdir -p")
		assert.NotContains(t, note, "/tmp")
	}
}

// The root agent keeps the chat's directory as it was, and nothing is said
// without a chat, a thread or a working directory.
func TestSubAgentScratchNote_OnlyForSubAgents(t *testing.T) {
	t.Parallel()
	chat := &db.Chat{ID: "chat-123"}
	for name, note := range map[string]string{
		"root":           subAgentScratchNote(chat, "chat-123", 0, true),
		"no thread":      subAgentScratchNote(chat, "", 1, true),
		"no chat":        subAgentScratchNote(nil, "agent-a", 1, true),
		"no chat id":     subAgentScratchNote(&db.Chat{}, "agent-a", 1, true),
		"no working dir": subAgentScratchNote(chat, "agent-a", 1, false),
	} {
		assert.Empty(t, note, name)
	}
}

// The shared system block is identical for every agent of the chat (sibling
// agents share it in the provider's prompt cache); the per-agent line is a
// block of its own.
func TestScratchDirGuidance_IsTheSameForEveryAgent(t *testing.T) {
	t.Parallel()
	chat := &db.Chat{ID: "chat-123"}
	guidance := scratchDirGuidance(chat)
	assert.Contains(t, guidance, "~/.reliant/scratch/chat-123/ ")
	assert.False(t, strings.Contains(guidance, "agent_id"), "no per-agent text in the shared block")
}

// The guidance says that idle scratch is cleaned up, and how to keep a
// directory: the daemon's idle reclaim keeps one with this marker at its top.
func TestScratchDirGuidance_NamesTheLifecycleAndTheKeepMarker(t *testing.T) {
	t.Parallel()
	guidance := scratchDirGuidance(&db.Chat{ID: "chat-123"})
	assert.Contains(t, guidance, "after 2 days unused")
	assert.Contains(t, guidance, "after 7 days unused")
	assert.Contains(t, guidance, "`"+idlereclaim.KeepFile+"`")
}

// The shared guidance says the agent-id subdirectories belong to the chat's
// sub-agents and are not to be deleted: a root agent freeing disk once
// removed every subdirectory of its scratch directory, running sub-agents'
// briefings and logs included (agent reports 25075365, e60bb521).
func TestScratchDirGuidance_SaysSubAgentDirectoriesAreNotYoursToDelete(t *testing.T) {
	t.Parallel()
	guidance := scratchDirGuidance(&db.Chat{ID: "chat-123"})
	assert.Contains(t, guidance, "subdirectory named by its agent id")
	assert.Contains(t, guidance, "never delete a subdirectory you did not create")
}
