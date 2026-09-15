// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
)

// LoadedToolsStore tracks which tools have been dynamically loaded, and at what
// permission, for one agent scope. This is an in-memory store that persists
// across loop iterations within the same server process. Thread-safe for
// concurrent access.
//
// Entries are keyed by SCOPE — (chatID, thread) via scope() — not by chat.
// Thread is what distinguishes the three things a chat-only key conflated:
// a new run sets targetThread = workflowID, and a spawned child runs on the
// SAME chat under a new thread. Keying by chat alone therefore let a completed
// run's grants reappear in the next run, and let a child's grants outlive it
// into the parent (and vice versa). Callers must pass scope(chatID, thread).
type LoadedToolsStore struct {
	mu           sync.RWMutex
	tools        map[string]map[string]bool      // scope -> set of tool names
	permissions  map[string]string               // scope -> permission level
	access       map[string]ToolAccess           // scope -> what the workflow declared
	skills       map[string][]config.StoredSkill // scope -> skills
	availableMCP map[string][]MCPToolInfo        // scope -> connected/available MCP tools
}

// scope builds the store key identifying one agent's tool state: a single run of
// a single thread. Callers that genuinely have no thread (there should be none
// on the production path) degrade to chat-only keying rather than colliding on
// an empty key.
func scope(chatID, thread string) string {
	if thread == "" {
		return chatID
	}
	return chatID + "\x00" + thread
}

// Scope exposes the store's key construction to callers outside this package
// (the workflow activities), so the key shape stays defined in exactly one place.
func Scope(chatID, thread string) string {
	return scope(chatID, thread)
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
	access:       make(map[string]ToolAccess),
	skills:       make(map[string][]config.StoredSkill),
	availableMCP: make(map[string][]MCPToolInfo),
}

// GetLoadedToolsStore returns the global loaded tools store.
func GetLoadedToolsStore() *LoadedToolsStore {
	return globalLoadedToolsStore
}

// Add adds a tool to the loaded set for a scope.
// Returns true if the tool was newly added, false if already loaded.
func (s *LoadedToolsStore) Add(scopeKey, toolName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tools[scopeKey] == nil {
		s.tools[scopeKey] = make(map[string]bool)
	}
	if s.tools[scopeKey][toolName] {
		return false
	}
	s.tools[scopeKey][toolName] = true
	return true
}

// Get returns all loaded tool names for a scope.
func (s *LoadedToolsStore) Get(scopeKey string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	set := s.tools[scopeKey]
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

// Has checks if a tool is loaded for a scope.
func (s *LoadedToolsStore) Has(scopeKey, toolName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.tools[scopeKey][toolName]
}

// Clear removes all loaded tools, permission, and skills for a scope. Called
// when a run reaches a terminal state so its grants do not outlive it; scoped
// keying makes a missed Clear far less dangerous than it was, but a run that
// ends should still release its state rather than wait for process exit.
func (s *LoadedToolsStore) Clear(scopeKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tools, scopeKey)
	delete(s.permissions, scopeKey)
	delete(s.access, scopeKey)
	delete(s.skills, scopeKey)
	delete(s.availableMCP, scopeKey)
}

// SetAvailableMCPTools records the connected/available MCP tools for a scope so
// load_tool can search them by keyword and verify they exist before loading.
// Passing an empty slice clears any previously recorded set for the scope.
func (s *LoadedToolsStore) SetAvailableMCPTools(scopeKey string, mcpTools []MCPToolInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.availableMCP == nil {
		s.availableMCP = make(map[string][]MCPToolInfo)
	}
	if len(mcpTools) == 0 {
		delete(s.availableMCP, scopeKey)
		return
	}
	s.availableMCP[scopeKey] = mcpTools
}

// GetAvailableMCPTools returns the connected/available MCP tools recorded for a
// scope, or nil if none.
func (s *LoadedToolsStore) GetAvailableMCPTools(scopeKey string) []MCPToolInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.availableMCP[scopeKey]
}

// SetSkills stores the project skills for a scope so the executor can access them.
func (s *LoadedToolsStore) SetSkills(scopeKey string, skills []config.StoredSkill) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.skills[scopeKey] = skills
}

// GetSkills returns the stored skills for a scope, or nil if none.
func (s *LoadedToolsStore) GetSkills(scopeKey string) []config.StoredSkill {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.skills[scopeKey]
}

// SetPermission sets the permission level for a scope.
func (s *LoadedToolsStore) SetPermission(scopeKey, permission string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.permissions[scopeKey] = permission
}

// SetToolAccess records what a workflow declared for a scope: the tools handed
// to the model, and the tools load_tool may reach.
//
// Recording both is the correction to an earlier single "allowed" set. That set
// was the expansion of `tools:` — a starting bundle — and enforcing it at
// load_tool read an omission as a refusal. Every tool outside the bundle became
// unreachable, which broke the one route to tools deliberately excluded from
// every default bundle for cost rather than policy.
func (s *LoadedToolsStore) SetToolAccess(scopeKey string, access ToolAccess) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.access[scopeKey] = access
}

// CanLoadTool reports whether load_tool may reach a tool in this scope.
//
// A scope with no recorded access allows everything. That covers the workflow
// that declared nothing AND the scope lost to a worker restart — neither should
// strand a live run, and this is not the layer that makes a run safe.
func (s *LoadedToolsStore) CanLoadTool(scopeKey, toolName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	access, ok := s.access[scopeKey]
	if !ok {
		return true
	}
	return access.CanLoad(toolName)
}

// LoadableIsUnrestricted reports whether this scope can reach anything, so
// callers can skip per-name checks entirely — which is the common case, since
// unset means unrestricted.
func (s *LoadedToolsStore) LoadableIsUnrestricted(scopeKey string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	access, ok := s.access[scopeKey]
	if !ok {
		return true
	}
	return access.LoadableAll
}

// GetPermission returns the permission level for a scope.
//
// An unknown scope FAILS CLOSED at the LOWEST live tier. This store is
// in-memory, so a worker restart empties it while runs are in flight; defaulting
// to PermissionOrchestrator would grant maximum privilege — including spawn —
// to any execute_tools landing between the restart and the next call_llm, which
// is what re-populates the scope, precisely because the state had been lost.
//
// With the readonly tier removed, the lowest tier is PermissionMutating, so this
// default now withholds only spawn. That is a real reduction in what the
// fail-closed path protects, and it is the honest one: the readonly tier never
// withheld write in the first place, since the shell was granted at every level.
func (s *LoadedToolsStore) GetPermission(scopeKey string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if perm, ok := s.permissions[scopeKey]; ok {
		return NormalizePermission(perm)
	}
	return PermissionMutating
}

// DeferredToolNames returns tool names from the registry (and available MCP tools)
// that are NOT in the initial set and NOT already loaded for a given chat.
// These are the tools the LLM can request via load_tool.
func DeferredToolNames(scopeKey string, permission string, initialToolNames []string, mcpToolNames []string) []string {
	registry := GetToolRegistry()
	store := GetLoadedToolsStore()

	// Build set of initial + already-loaded tools
	loaded := make(map[string]bool, len(initialToolNames))
	for _, name := range initialToolNames {
		loaded[name] = true
	}
	for _, name := range store.Get(scopeKey) {
		loaded[name] = true
	}

	// Discovery must agree with enforcement. This list is rendered into
	// load_tool's own description as "Additional tools available (use load_tool
	// to enable)", which the model reads as a promise — so a name here that
	// loadTool would refuse is worse than an omission: the model cannot tell a
	// policy refusal from a malfunction, and retries something that can never
	// work. That mismatch is exactly how a workflow ended up advertising
	// generate_image and then refusing it.
	unrestricted := store.LoadableIsUnrestricted(scopeKey)

	var deferred []string
	for _, def := range registry {
		if loaded[def.Name] {
			continue
		}
		// Only include tools the agent's permission level allows
		if !PermissionAtLeast(permission, MinimumPermissionForTool(def.Name)) {
			continue
		}
		if !unrestricted && !store.CanLoadTool(scopeKey, def.Name) {
			continue
		}
		deferred = append(deferred, def.Name)
	}

	// Include MCP tools that aren't already in the active tool set
	for _, name := range mcpToolNames {
		if loaded[name] {
			continue
		}
		if !unrestricted && !store.CanLoadTool(scopeKey, name) {
			continue
		}
		deferred = append(deferred, name)
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
			MinPermission:     PermissionMutating,
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
