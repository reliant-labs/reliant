// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStore returns a fresh LoadedToolsStore so tests do not interfere with
// the global singleton. The per-chat APIs are identical.
func newTestStore() *LoadedToolsStore {
	return &LoadedToolsStore{
		tools:       make(map[string]map[string]bool),
		permissions: make(map[string]string),
	}
}

func TestLoadedToolsStore_AddAndGet(t *testing.T) {
	s := newTestStore()
	chatID := "chat-add-get"

	assert.True(t, s.Add(chatID, "write"), "first add should return true")

	got := s.Get(chatID)
	assert.Equal(t, []string{"write"}, got)
}

func TestLoadedToolsStore_Has(t *testing.T) {
	s := newTestStore()
	chatID := "chat-has"

	assert.False(t, s.Has(chatID, "edit"), "Has must be false before Add")
	s.Add(chatID, "edit")
	assert.True(t, s.Has(chatID, "edit"), "Has must be true after Add")
	assert.False(t, s.Has(chatID, "some_other_tool"))
}

func TestLoadedToolsStore_MultipleChatsIsolated(t *testing.T) {
	s := newTestStore()
	chatA := "chat-A"
	chatB := "chat-B"

	s.Add(chatA, "write")
	s.Add(chatA, "edit")
	s.Add(chatB, "fetch")

	assert.Equal(t, []string{"edit", "write"}, s.Get(chatA),
		"chat A should see only its own tools (sorted)")
	assert.Equal(t, []string{"fetch"}, s.Get(chatB),
		"chat B should see only its own tools")

	assert.True(t, s.Has(chatA, "write"))
	assert.False(t, s.Has(chatB, "write"), "tools must not leak across chats")
	assert.True(t, s.Has(chatB, "fetch"))
	assert.False(t, s.Has(chatA, "fetch"))
}

func TestLoadedToolsStore_Clear(t *testing.T) {
	s := newTestStore()
	chatID := "chat-clear"

	s.Add(chatID, "write")
	s.Add(chatID, "edit")
	assert.Len(t, s.Get(chatID), 2)

	s.Clear(chatID)
	assert.Nil(t, s.Get(chatID), "Get should return nil for a cleared chat")
	assert.False(t, s.Has(chatID, "write"))
	assert.False(t, s.Has(chatID, "edit"))
}

func TestLoadedToolsStore_ClearDoesNotAffectOtherChats(t *testing.T) {
	s := newTestStore()
	chatA := "chat-keep"
	chatB := "chat-clear"

	s.Add(chatA, "write")
	s.Add(chatB, "edit")

	s.Clear(chatB)

	assert.Equal(t, []string{"write"}, s.Get(chatA), "other chats must be untouched")
	assert.Nil(t, s.Get(chatB))
}

func TestLoadedToolsStore_AddDuplicate_NoError(t *testing.T) {
	s := newTestStore()
	chatID := "chat-dup"

	assert.True(t, s.Add(chatID, "write"), "first Add should return true (newly added)")
	assert.False(t, s.Add(chatID, "write"), "second Add should return false (already present)")

	// Idempotent: still exactly one entry.
	assert.Equal(t, []string{"write"}, s.Get(chatID))
}

func TestLoadedToolsStore_SetAndGetPermission(t *testing.T) {
	s := newTestStore()
	chatID := "chat-perm"

	s.SetPermission(chatID, PermissionMutating)
	assert.Equal(t, PermissionMutating, s.GetPermission(chatID))

	// Overwrite
	s.SetPermission(chatID, PermissionReadOnly)
	assert.Equal(t, PermissionReadOnly, s.GetPermission(chatID))
}

func TestLoadedToolsStore_GetPermission_DefaultOrchestrator(t *testing.T) {
	s := newTestStore()

	// No permission ever set -> backward-compatible default of orchestrator.
	assert.Equal(t, PermissionOrchestrator, s.GetPermission("unknown-chat"),
		"unset permission should default to orchestrator for backward compat")
}

func TestLoadedToolsStore_ClearRemovesPermission(t *testing.T) {
	s := newTestStore()
	chatID := "chat-clear-perm"

	s.SetPermission(chatID, PermissionReadOnly)
	s.Add(chatID, "view")
	assert.Equal(t, PermissionReadOnly, s.GetPermission(chatID))

	s.Clear(chatID)

	// Back to the backward-compat default.
	assert.Equal(t, PermissionOrchestrator, s.GetPermission(chatID),
		"Clear must remove stored permission, falling back to default")
	assert.Nil(t, s.Get(chatID))
}

func TestLoadedToolsStore_AvailableMCPTools_SetGetClear(t *testing.T) {
	s := newTestStore() // note: newTestStore does not init availableMCP; setter must lazily init
	chatID := "chat-mcp"

	// Unset -> nil.
	assert.Nil(t, s.GetAvailableMCPTools(chatID))

	infos := []MCPToolInfo{
		{Name: "mcp__chrome-devtools__take_screenshot", Description: "Capture a screenshot"},
		{Name: "mcp__chrome-devtools__navigate_page", Description: "Navigate to a URL"},
	}
	s.SetAvailableMCPTools(chatID, infos)
	assert.Equal(t, infos, s.GetAvailableMCPTools(chatID))

	// Empty slice clears the entry.
	s.SetAvailableMCPTools(chatID, nil)
	assert.Nil(t, s.GetAvailableMCPTools(chatID))

	// Clear also removes recorded MCP tools.
	s.SetAvailableMCPTools(chatID, infos)
	s.Clear(chatID)
	assert.Nil(t, s.GetAvailableMCPTools(chatID))
}

func TestSearchTools_SurfacesConnectedMCPTool(t *testing.T) {
	mcpTools := []MCPToolInfo{
		{Name: "mcp__chrome-devtools__take_screenshot", Description: "Capture a screenshot of the current page"},
		{Name: "mcp__chrome-devtools__navigate_page", Description: "Navigate the browser to a URL"},
	}

	// Keyword present in the MCP tool NAME.
	results := SearchTools("screenshot", PermissionReadOnly, mcpTools)
	require.True(t, containsToolResult(results, "mcp__chrome-devtools__take_screenshot"),
		"expected screenshot MCP tool surfaced by name keyword")
	for _, r := range results {
		if r.Name == "mcp__chrome-devtools__take_screenshot" {
			assert.True(t, r.PermissionAllowed, "connected MCP tools should report as available")
			assert.Contains(t, r.Tags, TagMCP)
		}
	}

	// Keyword present only in the DESCRIPTION ("browser").
	results = SearchTools("browser", PermissionReadOnly, mcpTools)
	assert.True(t, containsToolResult(results, "mcp__chrome-devtools__navigate_page"),
		"expected MCP tool surfaced via description keyword")

	// Non-matching keyword surfaces no MCP tools.
	results = SearchTools("zzz_no_match_zzz", PermissionReadOnly, mcpTools)
	for _, r := range results {
		assert.NotContains(t, r.Name, "mcp__", "no MCP tool should match a non-matching keyword")
	}

	// Built-in registry search still works with no MCP tools supplied.
	results = SearchTools("workflow", PermissionOrchestrator, nil)
	assert.NotEmpty(t, results, "registry search must still function without MCP tools")
}

func containsToolResult(results []ToolSearchResult, name string) bool {
	for _, r := range results {
		if r.Name == name {
			return true
		}
	}
	return false
}

func TestLoadedToolsStore_ConcurrentAccess(t *testing.T) {
	s := newTestStore()

	const goroutines = 50
	const perGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			chatID := fmt.Sprintf("chat-%d", gid%5) // 5 chats shared across goroutines
			for i := 0; i < perGoroutine; i++ {
				toolName := fmt.Sprintf("tool-%d", i)
				s.Add(chatID, toolName)
				_ = s.Has(chatID, toolName)
				_ = s.Get(chatID)
				s.SetPermission(chatID, PermissionMutating)
				_ = s.GetPermission(chatID)
			}
		}(g)
	}

	wg.Wait()

	// Each of the 5 chats should end with exactly perGoroutine tools added.
	for c := 0; c < 5; c++ {
		chatID := fmt.Sprintf("chat-%d", c)
		assert.Len(t, s.Get(chatID), perGoroutine,
			"chat %q should have %d tools after concurrent writes", chatID, perGoroutine)
		assert.Equal(t, PermissionMutating, s.GetPermission(chatID))
	}
}
