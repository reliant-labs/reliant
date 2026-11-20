// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"

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
