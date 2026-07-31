// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
)

// LoadedToolsStore tracks which tools have been dynamically loaded per chat.
// This is an in-memory store that persists across loop iterations within
// the same server process. Thread-safe for concurrent access.
type LoadedToolsStore struct {
	mu           sync.RWMutex
	tools        map[string]map[string]bool      // chatID -> set of tool names
	permissions  map[string]string               // chatID -> permission level
	skills       map[string][]config.StoredSkill // chatID -> skills
	availableMCP map[string][]MCPToolInfo        // chatID -> connected/available MCP tools
}

// MCPToolInfo carries the minimal metadata needed for progressive discovery of
// available (connected) MCP tools via load_tool: the prefixed tool name
// (mcp__server__tool) and its description, used for keyword search and to
// verify a tool is actually connected before loading it.
type MCPToolInfo struct {
	Name        string
	Description string
}

var globalLoadedToolsStore = &LoadedToolsStore{
	tools:        make(map[string]map[string]bool),
	permissions:  make(map[string]string),
	skills:       make(map[string][]config.StoredSkill),
	availableMCP: make(map[string][]MCPToolInfo),
}

// GetLoadedToolsStore returns the global loaded tools store.
func GetLoadedToolsStore() *LoadedToolsStore {
	return globalLoadedToolsStore
}

// Add adds a tool to the loaded set for a chat.
// Returns true if the tool was newly added, false if already loaded.
func (s *LoadedToolsStore) Add(chatID, toolName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tools[chatID] == nil {
		s.tools[chatID] = make(map[string]bool)
	}
	if s.tools[chatID][toolName] {
		return false
	}
	s.tools[chatID][toolName] = true
	return true
}

// Get returns all loaded tool names for a chat.
func (s *LoadedToolsStore) Get(chatID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	set := s.tools[chatID]
	if len(set) == 0 {
		return nil
	}

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Has checks if a tool is loaded for a chat.
func (s *LoadedToolsStore) Has(chatID, toolName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.tools[chatID][toolName]
}

// Clear removes all loaded tools, permission, and skills for a chat.
func (s *LoadedToolsStore) Clear(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tools, chatID)
	delete(s.permissions, chatID)
	delete(s.skills, chatID)
	delete(s.availableMCP, chatID)
}

// SetAvailableMCPTools records the connected/available MCP tools for a chat so
// load_tool can search them by keyword and verify they exist before loading.
// Passing an empty slice clears any previously recorded set for the chat.
func (s *LoadedToolsStore) SetAvailableMCPTools(chatID string, mcpTools []MCPToolInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.availableMCP == nil {
		s.availableMCP = make(map[string][]MCPToolInfo)
	}
	if len(mcpTools) == 0 {
		delete(s.availableMCP, chatID)
		return
	}
	s.availableMCP[chatID] = mcpTools
}

// GetAvailableMCPTools returns the connected/available MCP tools recorded for a
// chat, or nil if none.
func (s *LoadedToolsStore) GetAvailableMCPTools(chatID string) []MCPToolInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.availableMCP[chatID]
}

// SetSkills stores the project skills for a chat so the executor can access them.
func (s *LoadedToolsStore) SetSkills(chatID string, skills []config.StoredSkill) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.skills[chatID] = skills
}

// GetSkills returns the stored skills for a chat, or nil if none.
func (s *LoadedToolsStore) GetSkills(chatID string) []config.StoredSkill {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.skills[chatID]
}

// SetPermission sets the permission level for a chat.
func (s *LoadedToolsStore) SetPermission(chatID, permission string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.permissions[chatID] = permission
}

// GetPermission returns the permission level for a chat.
// Returns PermissionOrchestrator if not set (backward compatible default).
func (s *LoadedToolsStore) GetPermission(chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if perm, ok := s.permissions[chatID]; ok {
		return perm
	}
	return PermissionOrchestrator
}

// DeferredToolNames returns tool names from the registry (and available MCP tools)
// that are NOT in the initial set and NOT already loaded for a given chat.
// These are the tools the LLM can request via load_tool.
func DeferredToolNames(chatID string, permission string, initialToolNames []string, mcpToolNames []string) []string {
	registry := GetToolRegistry()
	store := GetLoadedToolsStore()

	// Build set of initial + already-loaded tools
	loaded := make(map[string]bool, len(initialToolNames))
	for _, name := range initialToolNames {
		loaded[name] = true
	}
	for _, name := range store.Get(chatID) {
		loaded[name] = true
	}

	var deferred []string
	for _, def := range registry {
		if loaded[def.Name] {
			continue
		}
		// Only include tools the agent's permission level allows
		if !PermissionAtLeast(permission, MinimumPermissionForTool(def.Name)) {
			continue
		}
		deferred = append(deferred, def.Name)
	}

	// Include MCP tools that aren't already in the active tool set
	for _, name := range mcpToolNames {
		if !loaded[name] {
			deferred = append(deferred, name)
		}
	}

	sort.Strings(deferred)
	return deferred
}

// SearchTools searches for tools by keyword in the built-in registry AND in the
// set of available/connected MCP tools. The mcpTools argument carries the
// connected MCP tools (name + description) so that, e.g., load_tool(query=
// "screenshot") can surface mcp__chrome-devtools__take_screenshot even though
// MCP tools are not part of the static registry.
func SearchTools(query string, permission string, mcpTools []MCPToolInfo) []ToolSearchResult {
	registry := GetToolRegistry()
	q := strings.ToLower(query)

	var results []ToolSearchResult
	for _, def := range registry {
		name := strings.ToLower(def.Name)
		if !strings.Contains(name, q) {
			continue
		}
		minPerm := MinimumPermissionForTool(def.Name)
		results = append(results, ToolSearchResult{
			Name:              def.Name,
			Tags:              def.Tags,
			MinPermission:     minPerm,
			PermissionAllowed: PermissionAtLeast(permission, minPerm),
		})
	}

	// Also match connected MCP tools by name or description keyword. MCP tools
	// are gated by MCP configuration rather than the agent permission ladder, so
	// they always report as available once connected.
	for _, m := range mcpTools {
		if !strings.Contains(strings.ToLower(m.Name), q) &&
			!strings.Contains(strings.ToLower(m.Description), q) {
			continue
		}
		results = append(results, ToolSearchResult{
			Name:              m.Name,
			Tags:              []ToolTag{TagMCP},
			MinPermission:     PermissionReadOnly,
			PermissionAllowed: true,
		})
	}

	return results
}

// ToolSearchResult represents a search result for tool discovery.
type ToolSearchResult struct {
	Name              string    `json:"name"`
	Tags              []ToolTag `json:"tags"`
	MinPermission     string    `json:"min_permission"`
	PermissionAllowed bool      `json:"allowed"`
}
