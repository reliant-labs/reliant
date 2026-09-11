// Copyright (c) 2025 Reliant Labs
package tools

import "testing"

// TestNetworkToolsAreNotDaemonBound pins fetch and websearch to a location that
// does not require a daemon.
//
// Both tools are pure net/http plus HTML parsing — they open no files and spawn
// no processes — so a daemon buys them nothing but a hard dependency. The cost
// of the old marking was not just a wasted hop: RequiresDaemon (see
// internal/workflow/runtime/preflight.go) treats any daemon-located tool in a
// node's filter as proof the whole workflow needs a daemon, and both tools carry
// TagDefault. That made `tag:default` — the most common filter there is — enough
// to fire the preflight gate and refuse to start a workflow that never touches
// the user's machine.
//
// The tradeoff this test locks in: outbound HTTP now originates from the server
// rather than the user's machine.
func TestNetworkToolsAreNotDaemonBound(t *testing.T) {
	t.Parallel()

	for _, name := range []string{ToolFetch, ToolWebSearch} {
		var found *ToolDefinition
		for _, def := range GetToolRegistry() {
			if def.Name == name {
				found = &def
				break
			}
		}
		if found == nil {
			t.Fatalf("tool %q is not in the registry", name)
		}
		if found.RunsOn == ToolRunsOnDaemon {
			t.Errorf("tool %q is marked %q; network-only tools must not require a daemon",
				name, ToolRunsOnDaemon)
		}
	}
}
