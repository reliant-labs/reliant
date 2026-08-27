// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
)

// countingClient records how many tool calls reached it, so a test can assert
// that a refused project did not silently land on somebody else's index.
type countingClient struct {
	fakeManagerClient
	calls int
}

func (c *countingClient) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	_ = name
	_ = arguments
	c.calls++
	return &ToolResult{}, nil
}

func writeFile(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dirScopedServer is a tree indexer declaring the markers that make it worth
// starting — the gopls-shaped config the incident came from.
func dirScopedServer(requires ...string) config.MCPServer {
	return config.MCPServer{
		Command:       "lsp-bin",
		Type:          config.MCPStdio,
		Enabled:       true,
		DirScoped:     true,
		RequiresFiles: requires,
	}
}

// managerWithServer wires a manager whose clientFactory records every spawn,
// so a test asserts on PROCESSES rather than on bookkeeping.
func managerWithServer(t *testing.T, name string, cfg config.MCPServer) (*Manager, *[]string) {
	t.Helper()
	spawned := &[]string{}
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	m.clientFactory = func(_ string, c config.MCPServer) (Client, error) {
		*spawned = append(*spawned, c.Dir)
		return &fakeManagerClient{}, nil
	}
	m.serverConfigs[name] = cfg
	return m, spawned
}

// This is the incident, reduced: a Unity/C# checkout with no go.mod, for which
// reliant nevertheless started a Go language server that then indexed 9.9 GB and
// held 28,467 file descriptors. The server must not be started at all.
func TestPrecondition_NotSpawnedForProjectLackingItsMarker(t *testing.T) {
	m, spawned := managerWithServer(t, "lsp", dirScopedServer("go.mod", "go.work"))

	unityProject := t.TempDir()
	writeFile(t, unityProject, "Assets/Scripts/Player.cs")
	writeFile(t, unityProject, "ProjectSettings/ProjectVersion.txt")

	client, ok, err := m.dirClientFor("lsp", unityProject)
	if ok || client != nil {
		t.Fatal("a Go language server was started for a project with no Go module")
	}
	if err == nil {
		t.Fatal("a refused dir-scoped server must report why, not silently fall through to the shared client")
	}
	if len(*spawned) != 0 {
		t.Fatalf("no process should have been spawned, got %v", *spawned)
	}
}

// The mirror image: the same server, the same manager, a project that does have
// the marker. Without this the "fix" could be "never start anything".
func TestPrecondition_SpawnedWhenTheMarkerIsPresent(t *testing.T) {
	m, _ := managerWithServer(t, "lsp", dirScopedServer("go.mod"))

	goProject := t.TempDir()
	writeFile(t, goProject, "go.mod")

	client, ok, err := m.dirClientFor("lsp", goProject)
	if err != nil || !ok || client == nil {
		t.Fatalf("a project with the declared marker must get its server: ok=%v err=%v", ok, err)
	}
}

// A verdict must never be permanently cached: `go mod init` mid-session is a
// normal thing to do, and a sticky "no" would need a restart to clear. Project
// load is the explicit re-decision point.
func TestPrecondition_ReDecidedWhenTheProjectGainsItsMarker(t *testing.T) {
	m, _ := managerWithServer(t, "lsp", dirScopedServer("go.mod"))

	project := t.TempDir()
	if _, ok, _ := m.dirClientFor("lsp", project); ok {
		t.Fatal("precondition should not be met before the module exists")
	}

	writeFile(t, project, "go.mod")

	// Still refused from cache — this is the deliberate TTL, not a bug.
	if _, ok, _ := m.dirClientFor("lsp", project); ok {
		t.Fatal("verdict should be served from cache within the TTL")
	}

	// Reloading the project drops the cached verdict, as EnsureProjectServersLoaded does.
	m.forgetPreconditions(project)

	if _, ok, err := m.dirClientFor("lsp", project); !ok || err != nil {
		t.Fatalf("after reload the newly-initialized module must be seen: ok=%v err=%v", ok, err)
	}
}

// Every server that exists today declares nothing. None of them may change.
func TestPrecondition_AbsentDeclarationSpawnsAsBefore(t *testing.T) {
	m, _ := managerWithServer(t, "lsp", dirScopedServer())

	emptyProject := t.TempDir()
	if _, ok, err := m.dirClientFor("lsp", emptyProject); !ok || err != nil {
		t.Fatalf("a server declaring no precondition must spawn for any project: ok=%v err=%v", ok, err)
	}

	// An all-blank declaration is the same as none: it declares nothing.
	m2, _ := managerWithServer(t, "lsp2", dirScopedServer("  ", ""))
	if _, ok, err := m2.dirClientFor("lsp2", t.TempDir()); !ok || err != nil {
		t.Fatalf("a blank precondition must behave as no precondition: ok=%v err=%v", ok, err)
	}
}

// A monorepo whose only module is services/api/go.mod is still a project a Go
// language server should serve. A bare pattern matches by base name at any depth
// the bounded scan reaches; the root-only pass alone would miss it.
func TestPrecondition_BarePatternMatchesNestedMarker(t *testing.T) {
	m, _ := managerWithServer(t, "lsp", dirScopedServer("go.mod"))

	monorepo := t.TempDir()
	writeFile(t, monorepo, "services/api/go.mod")
	writeFile(t, monorepo, "web/package.json")

	if _, ok, err := m.dirClientFor("lsp", monorepo); !ok || err != nil {
		t.Fatalf("a nested module must satisfy a bare marker: ok=%v err=%v", ok, err)
	}
}

// A pattern containing "/" means what it says and is matched against the whole
// project-relative path, so it can be used to require a specific location.
func TestPrecondition_PatternWithSeparatorIsAnchored(t *testing.T) {
	project := t.TempDir()
	writeFile(t, project, "go.mod")

	m, _ := managerWithServer(t, "lsp", dirScopedServer("cmd/go.mod"))
	if _, ok, _ := m.dirClientFor("lsp", project); ok {
		t.Fatal("an anchored pattern must not be satisfied by a marker somewhere else")
	}

	m2, _ := managerWithServer(t, "lsp", dirScopedServer("services/*/go.mod"))
	nested := t.TempDir()
	writeFile(t, nested, "services/api/go.mod")
	if _, ok, err := m2.dirClientFor("lsp", nested); !ok || err != nil {
		t.Fatalf("an anchored pattern must match the path it describes: ok=%v err=%v", ok, err)
	}
}

// Globs are the point of the field: an ecosystem's marker is often a wildcard
// (*.csproj, *.sln) rather than a fixed name.
func TestPrecondition_GlobMatchesExtension(t *testing.T) {
	m, _ := managerWithServer(t, "csharp", dirScopedServer("*.csproj", "*.sln"))

	project := t.TempDir()
	writeFile(t, project, "Game.csproj")

	if _, ok, err := m.dirClientFor("csharp", project); !ok || err != nil {
		t.Fatalf("a glob marker must match: ok=%v err=%v", ok, err)
	}
}

// A malformed pattern fails CLOSED. Failing open would restore exactly the
// unbounded spawn the field exists to prevent, and the refusal is logged.
func TestPrecondition_MalformedPatternDoesNotSpawn(t *testing.T) {
	m, spawned := managerWithServer(t, "lsp", dirScopedServer("go.[mod"))

	project := t.TempDir()
	writeFile(t, project, "go.mod")

	if _, ok, _ := m.dirClientFor("lsp", project); ok {
		t.Fatal("a malformed pattern must not be treated as a match")
	}
	if len(*spawned) != 0 {
		t.Fatalf("no process should have been spawned, got %v", *spawned)
	}
}

// The refusal must NOT degrade into the shared client. For a dir-scoped server
// the shared client is bound to whichever project loaded it first, so falling
// through would answer this project from another project's index — the exact
// wrong-tree defect dir scoping exists to prevent.
func TestPrecondition_RefusalDoesNotFallBackToTheSharedClient(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })

	shared := &countingClient{}
	// A dir client, if one were wrongly created, would succeed — so a passing
	// assertion below means the refusal, not a failed subprocess spawn.
	m.clientFactory = func(_ string, _ config.MCPServer) (Client, error) {
		return &fakeManagerClient{}, nil
	}
	m.serverConfigs["lsp"] = dirScopedServer("go.mod")
	m.clients["lsp"] = shared
	m.healthMu.Lock()
	m.healthChecks["lsp"] = &healthStatus{healthy: true}
	m.healthMu.Unlock()

	unityProject := t.TempDir()
	writeFile(t, unityProject, "Assets/Scripts/Player.cs")

	if _, err := m.ProjectCallTool("", unityProject, "lsp", "definition", nil); err == nil {
		t.Fatal("a refused project must get an error, not another project's index")
	}
	if shared.calls != 0 {
		t.Fatalf("the shared client (bound to a different tree) was called %d times", shared.calls)
	}
}

// The eager autoload path is the other door into a spawn, and it must be gated
// identically: a skipped server is neither loaded nor failed, exactly like a
// disabled one, because declining to start something with nothing to do is not
// a failure.
func TestPrecondition_EnsureProjectServersLoadedSkipsUnsatisfiedServer(t *testing.T) {
	t.Setenv("RELIANT_USER_CONFIG_DIR", t.TempDir())

	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })

	// Count spawns of THIS server only: the autoload path also loads reliant's
	// built-in servers, which declare no precondition and are unaffected.
	spawned := 0
	m.clientFactory = func(name string, _ config.MCPServer) (Client, error) {
		if name == "lsp" {
			spawned++
		}
		return &fakeManagerClient{}, nil
	}
	m.SetProjectConfigResolver(func(_ context.Context, _ string) (*config.Config, error) {
		return &config.Config{MCPServers: map[string]config.MCPServer{
			"lsp": dirScopedServer("go.mod"),
		}}, nil
	})

	unityProject := t.TempDir()
	writeFile(t, unityProject, "Assets/Scripts/Player.cs")

	result := m.EnsureProjectServersLoaded(context.Background(), unityProject)
	for _, name := range result.LoadedServers {
		if name == "lsp" {
			t.Fatal("an unsatisfied server must not be reported as loaded")
		}
	}
	for _, name := range result.FailedServers {
		if name == "lsp" {
			t.Fatal("declining to start a server with nothing to index is not a failure")
		}
	}
	if _, exists := m.GetClient("lsp"); exists {
		t.Fatal("an unsatisfied server must not be registered as a running client")
	}
	if _, exists := m.GetProjectClient(unityProject, "lsp"); exists {
		t.Fatal("an unsatisfied server must not be associated with the project")
	}
	if spawned != 0 {
		t.Fatalf("no process should have been spawned, got %d", spawned)
	}
}

func TestMatchesRequirement(t *testing.T) {
	tests := []struct {
		pattern string
		relPath string
		want    bool
	}{
		{"go.mod", "go.mod", true},
		{"go.mod", "services/api/go.mod", true},
		{"go.mod", "go.sum", false},
		{"*.csproj", "Game.csproj", true},
		{"*.csproj", "src/Game.csproj", true},
		{"*.csproj", "Game.sln", false},
		{"cmd/*.go", "cmd/main.go", true},
		{"cmd/*.go", "main.go", false},
		{"cmd/*.go", "internal/cmd/main.go", false},
		{".python-version", ".python-version", true},
	}
	for _, tc := range tests {
		got, err := matchesRequirement(tc.pattern, tc.relPath)
		if err != nil {
			t.Errorf("matchesRequirement(%q, %q): %v", tc.pattern, tc.relPath, err)
			continue
		}
		if got != tc.want {
			t.Errorf("matchesRequirement(%q, %q) = %v, want %v", tc.pattern, tc.relPath, got, tc.want)
		}
	}

	if _, err := matchesRequirement("go.[mod", "go.mod"); err == nil {
		t.Error("a malformed pattern must report an error rather than silently never matching")
	}
}
