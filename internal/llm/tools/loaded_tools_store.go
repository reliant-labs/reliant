// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sort"
	"strings"
	"sync"
)

// LoadedToolsStore tracks which tools have been dynamically loaded per chat.
// This is an in-memory store that persists across loop iterations within
// the same server process. Thread-safe for concurrent access.
type LoadedToolsStore struct {
	mu          sync.RWMutex
	tools       map[string]map[string]bool // chatID -> set of tool names
	permissions map[string]string          // chatID -> permission level
}

var globalLoadedToolsStore = &LoadedToolsStore{
	tools:       make(map[string]map[string]bool),
	permissions: make(map[string]string),
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

// Clear removes all loaded tools and permission for a chat.
func (s *LoadedToolsStore) Clear(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tools, chatID)
	delete(s.permissions, chatID)
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

// DeferredToolNames returns tool names from the registry that are NOT in the
// initial set and NOT already loaded for a given chat. These are the tools
// the LLM can request via load_tool.
func DeferredToolNames(chatID string, permission string, initialToolNames []string) []string {
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

	sort.Strings(deferred)
	return deferred
}

// SearchTools searches for tools by keyword in the registry.
func SearchTools(query string, permission string) []ToolSearchResult {
	registry := GetToolRegistry()
	query = strings.ToLower(query)

	var results []ToolSearchResult
	for _, def := range registry {
		name := strings.ToLower(def.Name)
		if !strings.Contains(name, query) {
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
	return results
}

// ToolSearchResult represents a search result for tool discovery.
type ToolSearchResult struct {
	Name              string    `json:"name"`
	Tags              []ToolTag `json:"tags"`
	MinPermission     string    `json:"min_permission"`
	PermissionAllowed bool      `json:"allowed"`
}
