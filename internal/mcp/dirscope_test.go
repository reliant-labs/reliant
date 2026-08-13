// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
)

// recordingClient reports which config it was spawned with, so a test can assert
// WHICH TREE a server was pointed at rather than merely that a client exists.
type recordingClient struct {
	fakeManagerClient
	cfg config.MCPServer
}

// TestDirScopedServer_EachProjectGetsItsOwnClient pins the defect that motivated
// dir scoping.
//
// The manager keeps one client per server NAME and associates it with every
// project that asks (manager.go, EnsureProjectServersLoaded). For a stateless
// server that is correct. For a server that indexes a tree it means the second
// project silently reads the FIRST project's index — measured with a Go language
// server registered globally, which answered a chat in project B from the
// daemon's own checkout and returned confident matches from the wrong repo.
func TestDirScopedServer_EachProjectGetsItsOwnClient(t *testing.T) {
	const server = "lsp"
	projA := t.TempDir()
	projB := t.TempDir()

	spawned := map[string]config.MCPServer{}
	m := NewManager()
	defer func() { _ = m.Close() }()
	m.clientFactory = func(name string, cfg config.MCPServer) (Client, error) {
		spawned[cfg.Dir] = cfg
		return &recordingClient{cfg: cfg}, nil
	}
	m.serverConfigs[server] = config.MCPServer{
		Command:   "lsp-bin",
		Type:      config.MCPStdio,
		Enabled:   true,
		DirScoped: true,
	}

	a, okA := m.dirClientFor(server, projA)
	b, okB := m.dirClientFor(server, projB)
	if !okA || !okB {
		t.Fatalf("a dir-scoped server must resolve a per-project client (a=%v b=%v)", okA, okB)
	}
	if a == b {
		t.Fatal("two projects share one client for a dir-scoped server — the second project reads the first project's tree")
	}

	// Force the lazy clients to spawn so the resolved Dir is observable.
	if _, err := a.CallTool("noop", nil); err != nil {
		t.Fatalf("project A call: %v", err)
	}
	if _, err := b.CallTool("noop", nil); err != nil {
		t.Fatalf("project B call: %v", err)
	}
	if _, ok := spawned[projA]; !ok {
		t.Errorf("no server started in project A (%s); started in: %v", projA, keysOf(spawned))
	}
	if _, ok := spawned[projB]; !ok {
		t.Errorf("no server started in project B (%s); started in: %v", projB, keysOf(spawned))
	}
}

// A server that does NOT declare dirScoped keeps today's shared-client
// behaviour. This is the guard on the change: sharing is correct and cheaper for
// the stateless majority, and dir scoping must not silently multiply processes.
func TestNonDirScopedServer_StillShared(t *testing.T) {
	m := NewManager()
	defer func() { _ = m.Close() }()
	m.serverConfigs["plain"] = config.MCPServer{
		Command: "x", Type: config.MCPStdio, Enabled: true,
	}
	if _, ok := m.dirClientFor("plain", t.TempDir()); ok {
		t.Error("a server that did not declare dirScoped must route to the shared client")
	}
}

// An unregistered server, and an empty project path, both route to the shared
// client rather than spawning something unkeyed.
func TestDirClientFor_RequiresRegistrationAndPath(t *testing.T) {
	m := NewManager()
	defer func() { _ = m.Close() }()
	m.serverConfigs["lsp"] = config.MCPServer{
		Command: "x", Type: config.MCPStdio, Enabled: true, DirScoped: true,
	}
	if _, ok := m.dirClientFor("lsp", ""); ok {
		t.Error("an empty project path has no tree to scope to; must not create a dir client")
	}
	if _, ok := m.dirClientFor("unknown", t.TempDir()); ok {
		t.Error("an unregistered server must not create a dir client")
	}
}

// An explicit Dir pins the server to one tree regardless of the caller, which is
// the only reason to write the field.
func TestResolveServerDir_ExplicitDirWinsOverProject(t *testing.T) {
	pinned := t.TempDir()
	project := t.TempDir()
	if got := resolveServerDir(config.MCPServer{Dir: pinned}, project); got != pinned {
		t.Errorf("explicit Dir must win: got %q want %q", got, pinned)
	}
	if got := resolveServerDir(config.MCPServer{}, project); got != project {
		t.Errorf("empty Dir must default to the caller's project: got %q want %q", got, project)
	}
	if got := resolveServerDir(config.MCPServer{}, ""); got != "" {
		t.Errorf("no Dir and no project means inherit the daemon's cwd: got %q", got)
	}
}

// Some servers take the tree as an ARGUMENT rather than reading their working
// directory. Without substitution such a server can only be configured by
// hard-coding one project's path — the same wrong-tree bug in another disguise.
func TestExpandArgs_SubstitutesProjectPath(t *testing.T) {
	for _, ph := range projectPathPlaceholders {
		got := expandArgs([]string{"--workspace", ph, "--stdio"}, "/srv/proj")
		if got[1] != "/srv/proj" {
			t.Errorf("placeholder %q not substituted: got %q", ph, got[1])
		}
		if got[0] != "--workspace" || got[2] != "--stdio" {
			t.Errorf("placeholder %q disturbed sibling args: %v", ph, got)
		}
	}
}

// The composite key must never be mistakable for a real server name, or it could
// reach the model as part of an mcp__<server>__<tool> tool name.
func TestDirClientKey_CannotCollideWithAServerName(t *testing.T) {
	key := dirClientKey("lsp", "/srv/proj")
	if !strings.Contains(key, dirKeySeparator) {
		t.Fatalf("key %q must carry the separator", key)
	}
	// The tool-name validator (internal/llm/tools) rejects both "::" and "/";
	// assert the property here rather than importing it, which would invert the
	// dependency between the mcp layer and the tool layer.
	if !strings.Contains(key, "::") {
		t.Errorf("key %q must contain \"::\" so a logical-server-name check rejects it", key)
	}
	if !strings.Contains(dirKeySeparator, "::") {
		t.Errorf("separator %q must be built on \"::\" for the same reason sessionKeySeparator is", dirKeySeparator)
	}
}

func keysOf(m map[string]config.MCPServer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var _ = context.Background
