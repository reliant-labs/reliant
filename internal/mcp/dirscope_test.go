// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

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

	a, okA, errA := m.dirClientFor(server, projA)
	b, okB, errB := m.dirClientFor(server, projB)
	if errA != nil || errB != nil {
		t.Fatalf("a server with no declared precondition must resolve (a=%v b=%v)", errA, errB)
	}
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
	if _, ok, _ := m.dirClientFor("plain", t.TempDir()); ok {
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
	if _, ok, _ := m.dirClientFor("lsp", ""); ok {
		t.Error("an empty project path has no tree to scope to; must not create a dir client")
	}
	if _, ok, _ := m.dirClientFor("unknown", t.TempDir()); ok {
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

// ---------------------------------------------------------------------------
// Concurrency ceiling
// ---------------------------------------------------------------------------

// blockingClient parks inside a tool call until released, so a test can hold a
// dir client genuinely mid-call while eviction runs.
type blockingClient struct {
	fakeManagerClient
	entered chan struct{}
	release chan struct{}
}

func (b *blockingClient) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	_ = name
	_ = arguments
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return &ToolResult{}, nil
}

// dirScopedManager returns a manager with one registered dir-scoped server and
// no declared precondition, so these tests exercise the ceiling alone.
func dirScopedManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	m.clientFactory = func(_ string, _ config.MCPServer) (Client, error) {
		return &fakeManagerClient{}, nil
	}
	m.serverConfigs["lsp"] = config.MCPServer{
		Command: "lsp-bin", Type: config.MCPStdio, Enabled: true, DirScoped: true,
	}
	return m
}

// openProjects resolves a dir client for n fresh projects and returns their
// keys, then stamps a strictly increasing last-use order so LRU is decidable
// rather than dependent on how fast the loop ran.
func openProjects(t *testing.T, m *Manager, n int) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		project := t.TempDir()
		if _, ok, err := m.dirClientFor("lsp", project); !ok || err != nil {
			t.Fatalf("project %d did not get a client: ok=%v err=%v", i, ok, err)
		}
		keys = append(keys, dirClientKey("lsp", normalizeProjectPath(project)))
	}
	base := time.Now().Add(-time.Hour)
	m.mu.Lock()
	for i, key := range keys {
		m.dirClientLastUse[key] = base.Add(time.Duration(i) * time.Minute)
	}
	m.mu.Unlock()
	return keys
}

func (m *Manager) hasDirClient(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.dirClients[key]
	return ok
}

func (m *Manager) dirClientCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.dirClients)
}

// Dir scoping deliberately spawns one process per project, and nothing bounded
// the count: the idle reaper only reclaims after five quiet minutes, which a
// session that keeps touching projects never reaches. Measured, that is how ~17
// language servers at 28,467 descriptors each exhausted a 491,520-entry
// system-wide file table and took the machine's other processes with it.
func TestDirClients_LRUEvictsAtTheCeiling(t *testing.T) {
	m := dirScopedManager(t)

	keys := openProjects(t, m, maxDirScopedClients)

	overflow := t.TempDir()
	if _, ok, err := m.dirClientFor("lsp", overflow); !ok || err != nil {
		t.Fatalf("the overflowing project must still get its own client: ok=%v err=%v", ok, err)
	}
	overflowKey := dirClientKey("lsp", normalizeProjectPath(overflow))

	if got := m.dirClientCount(); got != maxDirScopedClients {
		t.Fatalf("dir clients must stay at the ceiling: got %d want %d", got, maxDirScopedClients)
	}
	if m.hasDirClient(keys[0]) {
		t.Error("the least recently used client survived the ceiling")
	}
	for _, key := range keys[1:] {
		if !m.hasDirClient(key) {
			t.Errorf("a more recently used client was evicted: %s", key)
		}
	}
	if !m.hasDirClient(overflowKey) {
		t.Error("the project that triggered eviction lost its own client")
	}
}

// The ceiling must never cost a caller its in-flight tool call. The least
// recently used client is skipped while it is mid-call, and the next-oldest idle
// one goes instead — a bound worth breaching for the seconds a call takes.
func TestDirClients_LRUNeverEvictsAClientMidCall(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })

	busyProject := t.TempDir()
	blocking := &blockingClient{entered: make(chan struct{}, 1), release: make(chan struct{})}
	m.clientFactory = func(_ string, cfg config.MCPServer) (Client, error) {
		if cfg.Dir == normalizeProjectPath(busyProject) {
			return blocking, nil
		}
		return &fakeManagerClient{}, nil
	}
	m.serverConfigs["lsp"] = config.MCPServer{
		Command: "lsp-bin", Type: config.MCPStdio, Enabled: true, DirScoped: true,
	}

	busyClient, ok, err := m.dirClientFor("lsp", busyProject)
	if !ok || err != nil {
		t.Fatalf("busy project did not get a client: ok=%v err=%v", ok, err)
	}
	busyKey := dirClientKey("lsp", normalizeProjectPath(busyProject))

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := busyClient.CallTool("slow", nil); err != nil {
			t.Errorf("the in-flight call failed: %v", err)
		}
	}()
	<-blocking.entered
	defer func() {
		close(blocking.release)
		<-done
	}()

	keys := openProjects(t, m, maxDirScopedClients-1)

	// Make the BUSY client the least recently used, so LRU order alone would
	// pick it and only the busy check saves it.
	m.mu.Lock()
	m.dirClientLastUse[busyKey] = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	if _, ok, err := m.dirClientFor("lsp", t.TempDir()); !ok || err != nil {
		t.Fatalf("the overflowing project must still get its own client: ok=%v err=%v", ok, err)
	}

	if !m.hasDirClient(busyKey) {
		t.Fatal("a client with a tool call in flight was evicted")
	}
	if m.hasDirClient(keys[0]) {
		t.Error("the oldest IDLE client should have been evicted instead")
	}
}

// A dir-scoped server's real processes are its per-project clients. Removing or
// disabling the server used to tear down only the shared client and leave every
// project's indexer running with nothing left to route to it.
func TestRemoveServer_ClosesDirScopedClients(t *testing.T) {
	m := dirScopedManager(t)
	m.clients["lsp"] = &fakeManagerClient{}

	project := t.TempDir()
	client, ok, err := m.dirClientFor("lsp", project)
	if !ok || err != nil {
		t.Fatalf("project did not get a client: ok=%v err=%v", ok, err)
	}

	if err := m.RemoveServer("lsp"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if m.dirClientCount() != 0 {
		t.Error("removing a server must drop its per-project clients")
	}
	if lc, isLazy := client.(*lazyClient); isLazy && lc.IsConnected() {
		t.Error("removing a server must close its per-project clients, not orphan them")
	}
}
