// Copyright (c) 2025 Reliant Labs
package tools

import "testing"

// spawn_status must NOT be orchestrator-tier: an agent that already holds a
// handle to a sub-agent it spawned needs no extra privilege to observe it.
func TestSpawnStatus_IsNotOrchestratorTier(t *testing.T) {
	if got := MinimumPermissionForTool(ToolSpawnStatus); got == PermissionOrchestrator {
		t.Errorf("spawn_status must not require orchestrator permission — a depth-1 sub-agent could never check its own children")
	}
}

// Both tools must be registered — GetToolByName must resolve them, and
// they must be present in the master name list validators check requests
// against.
func TestSpawnTools_AreRegistered(t *testing.T) {
	names := map[string]bool{ToolSpawnStatus: false, ToolSpawnSend: false}
	for _, def := range GetToolRegistry() {
		if _, ok := names[def.Name]; ok {
			names[def.Name] = true
		}
	}
	for name, found := range names {
		if !found {
			t.Errorf("%s missing from GetToolRegistry()", name)
		}
	}
}

// spawn_status carries TagReadOnly — it must never be able to mutate state,
// and tag:readonly filters (plan mode) should still be able to pick it up
// once permission allows it.
func TestSpawnStatus_IsReadOnlyTagged(t *testing.T) {
	found := false
	for _, def := range GetToolRegistry() {
		if def.Name != ToolSpawnStatus {
			continue
		}
		found = true
		hasReadOnly := false
		for _, tag := range def.Tags {
			if tag == TagReadOnly {
				hasReadOnly = true
			}
		}
		if !hasReadOnly {
			t.Errorf("%s: missing TagReadOnly", ToolSpawnStatus)
		}
	}
	if !found {
		t.Fatalf("%s not found in registry", ToolSpawnStatus)
	}
}

// GetToolByName must actually construct these tools via the factory, not just
// list their names — a name present in the registry but unreachable through
// the factory is effectively unregistered.
func TestSpawnTools_ConstructibleViaFactory(t *testing.T) {
	factory := NewToolsFactory(&ToolsOptions{})
	for _, name := range []string{ToolSpawnStatus, ToolSpawnSend} {
		tool := factory.GetToolByName(name, nil)
		if tool == nil {
			t.Fatalf("factory.GetToolByName(%q) returned nil", name)
		}
		if tool.Name() != name {
			t.Errorf("tool.Name() = %q, want %q", tool.Name(), name)
		}
	}
}
