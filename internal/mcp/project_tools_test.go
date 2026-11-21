// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
)

// treeServerClient stands in for a server that needs its tree to start, the
// way one launched as `python3 ${projectPath}/tools/server.py` does: started
// with no project (the shared client's config) its handshake fails; started
// in a project it lists a tool.
type treeServerClient struct {
	fakeManagerClient
	dir string
}

func (c *treeServerClient) Initialize(context.Context) error {
	if c.dir == "" {
		return errors.New(`calling "initialize": EOF`)
	}
	return nil
}

func (c *treeServerClient) ListTools() ([]Tool, error) {
	return []Tool{{Name: "ticket_status", Description: "status of a ticket in " + c.dir}}, nil
}

// A dir-scoped server's tools are offered in the project it is loaded for.
// The listing used to ask the shared client, which is registered with no tree,
// so a server that takes its tree as an argument never started for it: the
// server showed as connected and an agent was never offered one of its tools.
func TestListProjectTools_OffersDirScopedServerToolsFromTheProjectsClient(t *testing.T) {
	project := t.TempDir()
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = func(_ string, cfg config.MCPServer) (Client, error) {
		return &treeServerClient{dir: cfg.Dir}, nil
	}

	cfg := config.MCPServer{
		Type:      config.MCPStdio,
		Command:   "python3",
		Args:      []string{"${projectPath}/tools/tickets_mcp.py"},
		Enabled:   true,
		DirScoped: true,
	}
	if err := m.AddServer(context.Background(), "tickets", cfg); err != nil {
		t.Fatalf("register dir-scoped server: %v", err)
	}
	m.trackProjectServer(project, "tickets")

	tools, err := m.ListProjectTools(project)
	if err != nil {
		t.Fatalf("ListProjectTools: %v", err)
	}
	if got := tools["tickets"]; len(got) != 1 || got[0].Name != "ticket_status" {
		t.Fatalf("the dir-scoped server's tools were not offered in its project: %v", tools)
	}
	if want := "status of a ticket in " + project; tools["tickets"][0].Description != want {
		t.Fatalf("tools listed from the wrong tree: %q, want %q", tools["tickets"][0].Description, want)
	}

	one, err := m.ProjectListTools(project, "tickets")
	if err != nil || len(one) != 1 {
		t.Fatalf("ProjectListTools (mcp.list_tools) = %v, %v; want the project's one tool", one, err)
	}
	if _, err := m.ProjectListTools(t.TempDir(), "tickets"); err == nil {
		t.Fatal("a project the server was never loaded for must not list it")
	}
}

// A project the server's requiresFiles rule out offers none of its tools,
// without failing the listing of the project's other servers.
func TestListProjectTools_DirScopedServerRuledOutByItsFilesIsLeftOut(t *testing.T) {
	project := t.TempDir()
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = func(_ string, cfg config.MCPServer) (Client, error) {
		return &treeServerClient{dir: cfg.Dir}, nil
	}
	lsp := config.MCPServer{Type: config.MCPStdio, Command: "gopls", Enabled: true, DirScoped: true, RequiresFiles: []string{"go.mod"}}
	plain := config.MCPServer{Type: config.MCPStdio, Command: "plain", Enabled: true, Dir: "/srv"}
	for name, cfg := range map[string]config.MCPServer{"lsp": lsp, "plain": plain} {
		if err := m.AddServer(context.Background(), name, cfg); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		m.trackProjectServer(project, name)
	}

	tools, err := m.ListProjectTools(project)
	if err != nil {
		t.Fatalf("ListProjectTools: %v", err)
	}
	if _, ok := tools["lsp"]; ok {
		t.Fatalf("a server whose files the project lacks must offer no tools: %v", tools)
	}
	if len(tools["plain"]) != 1 {
		t.Fatalf("the project's other server lost its tools: %v", tools)
	}
}

// editableConfig is a project's MCP config that a test edits between loads.
type editableConfig struct {
	mu      sync.Mutex
	servers map[string]config.MCPServer
}

func (e *editableConfig) set(name string, cfg config.MCPServer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.servers[name] = cfg
}

func (e *editableConfig) resolve(context.Context, string) (*config.Config, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	servers := make(map[string]config.MCPServer, len(e.servers))
	for k, v := range e.servers {
		servers[k] = v
	}
	return &config.Config{MCPServers: withoutBuiltins(servers)}, nil
}

// startedCommands records the command of every client a manager spawned.
type startedCommands struct {
	mu   sync.Mutex
	cmds []string
}

func (s *startedCommands) factory(_ string, cfg config.MCPServer) (Client, error) {
	s.mu.Lock()
	s.cmds = append(s.cmds, cfg.Command)
	s.mu.Unlock()
	if cfg.Command == "broken" {
		return &treeServerClient{}, nil // fails its handshake
	}
	return &fakeManagerClient{}, nil
}

func (s *startedCommands) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cmds) == 0 {
		return ""
	}
	return s.cmds[len(s.cmds)-1]
}

// An edit to a running server's config in mcp.json takes effect at the next
// load. It used to keep the old command running until something restarted it.
func TestEnsureProjectServersLoaded_AppliesAnEditToARunningServer(t *testing.T) {
	config.UseUserConfigDirForTest(t, t.TempDir())
	project := t.TempDir()
	cfgs := &editableConfig{servers: map[string]config.MCPServer{}}
	started := &startedCommands{}
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = started.factory
	m.SetProjectConfigResolver(cfgs.resolve)

	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "old-tickets", Enabled: true})
	if r := m.EnsureProjectServersLoaded(context.Background(), project); r.HasFailures() {
		t.Fatalf("first load: %v", r.Errors)
	}
	if got := started.last(); got != "old-tickets" {
		t.Fatalf("started %q, want old-tickets", got)
	}

	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "new-tickets", Enabled: true})
	r := m.EnsureProjectServersLoaded(context.Background(), project)
	if r.HasFailures() {
		t.Fatalf("load after the edit: %v", r.Errors)
	}
	if got := started.last(); got != "new-tickets" {
		t.Fatalf("after editing the command the server still runs %q; want new-tickets", got)
	}
	if cfg := m.serverConfigs["tickets"]; cfg.Command != "new-tickets" {
		t.Fatalf("registered config is %q, want the edited one", cfg.Command)
	}

	// An unchanged config starts nothing.
	before := len(started.cmds)
	m.EnsureProjectServersLoaded(context.Background(), project)
	if len(started.cmds) != before {
		t.Fatalf("a load with no edit restarted the server: %v", started.cmds)
	}
}

// Two projects declaring one server name differently share its single
// client, as before; loading them in turn must not restart it each time.
func TestEnsureProjectServersLoaded_TwoProjectsDifferingDoNotRestartEachOther(t *testing.T) {
	config.UseUserConfigDirForTest(t, t.TempDir())
	projA, projB := t.TempDir(), t.TempDir()
	started := &startedCommands{}
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = started.factory
	m.SetProjectConfigResolver(func(_ context.Context, project string) (*config.Config, error) {
		command := "a-tickets"
		if project == projB {
			command = "b-tickets"
		}
		return &config.Config{MCPServers: withoutBuiltins(map[string]config.MCPServer{
			"tickets": {Type: config.MCPStdio, Command: command, Enabled: true},
		})}, nil
	})

	for i := 0; i < 3; i++ {
		m.EnsureProjectServersLoaded(context.Background(), projA)
		m.EnsureProjectServersLoaded(context.Background(), projB)
	}
	if len(started.cmds) != 1 {
		t.Fatalf("alternating loads restarted the shared server: %v", started.cmds)
	}
}

// A server that failed to start is retried at once when its config changes:
// the change is a fix, not a retry of the same broken command.
func TestEnsureProjectServersLoaded_RetriesAFixedServerWithoutTheCooldown(t *testing.T) {
	config.UseUserConfigDirForTest(t, t.TempDir())
	project := t.TempDir()
	cfgs := &editableConfig{servers: map[string]config.MCPServer{}}
	started := &startedCommands{}
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = started.factory
	m.SetProjectConfigResolver(cfgs.resolve)

	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "broken", Enabled: true})
	if r := m.EnsureProjectServersLoaded(context.Background(), project); !r.HasFailures() {
		t.Fatal("the broken server should have failed to start")
	}
	// Unchanged, it waits out the cooldown.
	before := len(started.cmds)
	m.EnsureProjectServersLoaded(context.Background(), project)
	if len(started.cmds) != before {
		t.Fatalf("an unchanged broken server was retried inside its cooldown: %v", started.cmds)
	}

	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "fixed", Enabled: true})
	r := m.EnsureProjectServersLoaded(context.Background(), project)
	if r.HasFailures() {
		t.Fatalf("the fixed server failed: %v", r.Errors)
	}
	if got := started.last(); got != "fixed" {
		t.Fatalf("the fixed config was not started inside the cooldown (last start %q)", got)
	}
}

// ProjectServerConfig reads what the project's config declares now, which is
// what a restart starts.
func TestProjectServerConfig_ReadsTheCurrentProjectConfig(t *testing.T) {
	config.UseUserConfigDirForTest(t, t.TempDir())
	cfgs := &editableConfig{servers: map[string]config.MCPServer{}}
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.SetProjectConfigResolver(cfgs.resolve)

	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "v2", Enabled: true})
	got, ok := m.ProjectServerConfig(context.Background(), t.TempDir(), "tickets")
	if !ok || got.Command != "v2" {
		t.Fatalf("ProjectServerConfig = %+v, %v; want v2", got, ok)
	}
	if _, ok := m.ProjectServerConfig(context.Background(), t.TempDir(), "nope"); ok {
		t.Fatal("an undeclared server has no config")
	}
	if _, ok := m.ProjectServerConfig(context.Background(), "", "tickets"); ok {
		t.Fatal("no project, no project config")
	}
}

// slowStartClient is a server whose start takes as long as the test says: its
// handshake waits for release.
type slowStartClient struct {
	fakeManagerClient
	release <-chan struct{}
}

func (c *slowStartClient) Initialize(ctx context.Context) error {
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *slowStartClient) ListTools() ([]Tool, error) {
	return []Tool{{Name: "find_symbol"}}, nil
}

func shortToolListBudget(t *testing.T) {
	t.Helper()
	prev := projectToolListBudget
	projectToolListBudget = 20 * time.Millisecond
	t.Cleanup(func() { projectToolListBudget = prev })
}

// A dir-scoped server that takes long to start for a project answers its
// listing within the budget, as still starting, instead of holding the caller
// past the worker's 10s per-listing timeout. The start goes on once, however
// many listings ask meanwhile, and the listing after it has its tools.
func TestProjectListTools_ColdDirServerAnswersWithinTheBudget(t *testing.T) {
	shortToolListBudget(t)
	project := t.TempDir()
	release := make(chan struct{})
	var starts atomic.Int32
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = func(string, config.MCPServer) (Client, error) {
		starts.Add(1)
		return &slowStartClient{release: release}, nil
	}
	cfg := config.MCPServer{Type: config.MCPStdio, Command: "gopls", Enabled: true, DirScoped: true}
	if err := m.AddServer(context.Background(), "lsp", cfg); err != nil {
		t.Fatalf("register: %v", err)
	}
	m.trackProjectServer(project, "lsp")

	for i := 0; i < 3; i++ {
		_, err := m.ProjectListTools(project, "lsp")
		if !errors.Is(err, ErrToolsStillLoading) {
			t.Fatalf("listing %d of a server still starting = %v, want ErrToolsStillLoading", i, err)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("concurrent listings started the server %d times, want once", n)
	}
	if _, ok := m.ProjectToolCount(project, "lsp"); ok {
		t.Fatal("a tool count is not known before the server answered")
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, ok := m.ProjectToolCount(project, "lsp"); ok {
			if n != 1 {
				t.Fatalf("tool count = %d, want 1", n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the start that outlived the budget never delivered its tools")
		}
		time.Sleep(5 * time.Millisecond)
	}
	tools, err := m.ProjectListTools(project, "lsp")
	if err != nil || len(tools) != 1 || tools[0].Name != "find_symbol" {
		t.Fatalf("listing after the start = %v, %v", tools, err)
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("the listing after the start started it again (%d starts)", n)
	}
}

// A status report never starts a dir-scoped server: ProjectToolCount answers
// from what a listing already learned.
func TestProjectToolCount_NeverStartsADirScopedServer(t *testing.T) {
	project := t.TempDir()
	var starts atomic.Int32
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = func(_ string, cfg config.MCPServer) (Client, error) {
		starts.Add(1)
		return &treeServerClient{dir: cfg.Dir}, nil
	}
	cfg := config.MCPServer{Type: config.MCPStdio, Command: "python3", Enabled: true, DirScoped: true}
	if err := m.AddServer(context.Background(), "tickets", cfg); err != nil {
		t.Fatalf("register: %v", err)
	}
	m.trackProjectServer(project, "tickets")

	if _, ok := m.ProjectToolCount(project, "tickets"); ok || starts.Load() != 0 {
		t.Fatalf("status started the server (%d starts) or invented a count", starts.Load())
	}
	if _, err := m.ProjectListTools(project, "tickets"); err != nil {
		t.Fatalf("listing: %v", err)
	}
	if n, ok := m.ProjectToolCount(project, "tickets"); !ok || n != 1 {
		t.Fatalf("count after a listing = %d, %v; want 1, true", n, ok)
	}
}

// Two loads that see one edit at once replace the server once: the second
// waits for the first and finds the new config already running, so it neither
// closes the process the first started nor starts another.
func TestEnsureProjectServersLoaded_ConcurrentLoadsReplaceAnEditedServerOnce(t *testing.T) {
	config.UseUserConfigDirForTest(t, t.TempDir())
	projA, projB := t.TempDir(), t.TempDir()
	cfgs := &editableConfig{servers: map[string]config.MCPServer{}}
	release := make(chan struct{})
	var newStarts atomic.Int32
	var started []*slowStartClient
	var mu sync.Mutex
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = func(_ string, cfg config.MCPServer) (Client, error) {
		if cfg.Command != "new-tickets" {
			return &fakeManagerClient{}, nil
		}
		newStarts.Add(1)
		c := &slowStartClient{release: release}
		mu.Lock()
		started = append(started, c)
		mu.Unlock()
		return c, nil
	}
	m.SetProjectConfigResolver(cfgs.resolve)

	// Both projects load the user-scope server under its old config.
	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "old-tickets", Enabled: true})
	m.EnsureProjectServersLoaded(context.Background(), projA)
	m.EnsureProjectServersLoaded(context.Background(), projB)

	cfgs.set("tickets", config.MCPServer{Type: config.MCPStdio, Command: "new-tickets", Enabled: true})
	results := make([]*ProjectServerLoadResult, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); results[0] = m.EnsureProjectServersLoaded(context.Background(), projA) }()
	// A is starting the new server (blocked in its handshake). B now sees the
	// same edit.
	for newStarts.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	wg.Add(1)
	go func() { defer wg.Done(); results[1] = m.EnsureProjectServersLoaded(context.Background(), projB) }()
	// Wait for B to reach the decision: blocked on the server's reload lock
	// (serialized), or starting a second replacement (unserialized).
	deadline := time.Now().Add(10 * time.Second)
	for m.reloadWaiters("tickets") == 0 && newStarts.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the second load never reached the server")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	if n := newStarts.Load(); n != 1 {
		t.Fatalf("the edited server was replaced %d times, want once", n)
	}
	for i, r := range results {
		if r.HasFailures() || len(r.LoadedServers) != 1 {
			t.Fatalf("load %d: loaded=%v errors=%v", i, r.LoadedServers, r.Errors)
		}
	}
	client, ok := m.GetClient("tickets")
	if !ok || client != Client(started[0]) || started[0].closed {
		t.Fatalf("the running client is not the one replacement, open (ok=%v)", ok)
	}
	for _, p := range []string{projA, projB} {
		if _, ok := m.GetProjectClient(p, "tickets"); !ok {
			t.Fatalf("project %s lost the server", p)
		}
	}
}

// probeGatedClient's handshake waits for its gate, and it lists the tool it
// was built with: one instance per server generation.
type probeGatedClient struct {
	fakeManagerClient
	gate <-chan struct{}
	tool string
}

func (c *probeGatedClient) Initialize(ctx context.Context) error {
	if c.gate == nil {
		return nil
	}
	select {
	case <-c.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *probeGatedClient) ListTools() ([]Tool, error) {
	return []Tool{{Name: c.tool}}, nil
}

// A probe of the server an edit replaced, still running past its budget, is
// never the answer for the replacement: the new server's listing returns its
// own tools, not the old ones.
func TestProjectListTools_AReplacementNeverJoinsItsPredecessorsProbe(t *testing.T) {
	shortToolListBudget(t)
	project := t.TempDir()
	oldGate := make(chan struct{})
	defer close(oldGate)
	m := NewManager(RoleDaemon)
	defer func() { _ = m.Close() }()
	m.clientFactory = func(_ string, cfg config.MCPServer) (Client, error) {
		if cfg.Command == "old-lsp" {
			return &probeGatedClient{gate: oldGate, tool: "old_tool"}, nil
		}
		return &probeGatedClient{tool: "new_tool"}, nil
	}
	register := func(command string) {
		t.Helper()
		cfg := config.MCPServer{Type: config.MCPStdio, Command: command, Enabled: true, DirScoped: true}
		if err := m.AddServer(context.Background(), "lsp", cfg); err != nil {
			t.Fatalf("register %s: %v", command, err)
		}
		m.trackProjectServer(project, "lsp")
	}

	register("old-lsp")
	if _, err := m.ProjectListTools(project, "lsp"); !errors.Is(err, ErrToolsStillLoading) {
		t.Fatalf("old server's listing = %v, want still starting", err)
	}
	// The edit: the old server goes, its probe still blocked; the new one
	// comes in under the same name and project.
	if err := m.RemoveServer("lsp", "its config changed"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	register("new-lsp")

	tools, err := m.ProjectListTools(project, "lsp")
	if err != nil {
		t.Fatalf("replacement's listing: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "new_tool" {
		t.Fatalf("replacement listed %v, want only its own new_tool", tools)
	}
}
