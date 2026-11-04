package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectMCP serves a forge MCP server on an in-memory pipe and returns a
// client session against it. The runner starts in dir.
func connectMCP(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	resetCmdRoute(t)
	t.Chdir(dir)
	r, err := newMCPRunner(NewRootCmd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := newMCPServer(r).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callMCP(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func writeMCPFixture(t *testing.T, dir string) {
	t.Helper()
	for rel, body := range map[string]string{
		"proto/services/shop/v1/shop.proto":         "syntax = \"proto3\";\nservice ShopService {\n  rpc ListWidgets(ListWidgetsRequest) returns (ListWidgetsResponse);\n}\nmessage Widget {\n  string id = 1;\n}\n",
		"db/migrations/00001_create_widgets.up.sql": "CREATE TABLE widgets (\n  id TEXT PRIMARY KEY\n);\n",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCP_ListsTheTools(t *testing.T) {
	cs := connectMCP(t, t.TempDir())

	init := cs.InitializeResult()
	if init.ServerInfo.Name != "forge" {
		t.Errorf("server name = %q, want forge", init.ServerInfo.Name)
	}
	for _, want := range []string{"Reliant Forge", "reliant-labs/forge", "Atlassian Forge", "skill_load"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("instructions missing %q:\n%s", want, init.Instructions)
		}
	}

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		got[tool.Name] = tool
	}
	readOnly := []string{"capabilities", "annotations", "libraries", "audit", "shapes", "skill_list", "skill_search", "skill_load"}
	mutating := []string{"lint", "generate", "report_bug"}
	if len(got) != len(readOnly)+len(mutating) {
		t.Errorf("got %d tools, want %d: %v", len(got), len(readOnly)+len(mutating), got)
	}
	for _, name := range readOnly {
		tool := got[name]
		if tool == nil {
			t.Errorf("missing tool %s", name)
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s must be annotated read-only", name)
		}
	}
	for _, name := range mutating {
		tool := got[name]
		if tool == nil {
			t.Errorf("missing tool %s", name)
			continue
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Errorf("%s must not be annotated read-only", name)
		}
	}
	// report_bug is the one tool that sends anything off the machine, and
	// says so.
	if rb := got["report_bug"]; rb != nil && (rb.Annotations == nil || rb.Annotations.OpenWorldHint == nil || !*rb.Annotations.OpenWorldHint) {
		t.Errorf("report_bug must be annotated open-world")
	}
	for name, tool := range got {
		schema, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(schema), `"dir"`) {
			t.Errorf("%s takes no dir: %s", name, schema)
		}
	}
}

func TestMCP_ReadOnlyToolsAnswerAgainstAProject(t *testing.T) {
	proj := t.TempDir()
	writeMCPFixture(t, proj)
	// The server starts somewhere else: dir must reach the project.
	cs := connectMCP(t, t.TempDir())

	out, isErr := callMCP(t, cs, "shapes", map[string]any{"dir": proj, "grep": "Widget"})
	if isErr {
		t.Fatalf("shapes failed: %s", out)
	}
	for _, want := range []string{"ShopService.ListWidgets", "widgets", "shop.proto"} {
		if !strings.Contains(out, want) {
			t.Errorf("shapes output missing %q:\n%s", want, out)
		}
	}

	out, isErr = callMCP(t, cs, "skill_load", map[string]any{"name": "forge"})
	if isErr {
		t.Fatalf("skill_load failed: %s", out)
	}
	if !strings.Contains(out, "Forge — start here") {
		t.Errorf("skill_load forge did not return the entry-point skill:\n%.300s", out)
	}

	out, isErr = callMCP(t, cs, "skill_search", map[string]any{"query": "migration"})
	if isErr || !strings.Contains(out, "db") {
		t.Errorf("skill_search = %q (isError %v)", out, isErr)
	}

	out, isErr = callMCP(t, cs, "capabilities", nil)
	if isErr || !strings.Contains(out, "lint") {
		t.Errorf("capabilities = %.200q (isError %v)", out, isErr)
	}

	out, isErr = callMCP(t, cs, "annotations", map[string]any{"kind": "go"})
	if isErr || !strings.Contains(out, "forge:") {
		t.Errorf("annotations = %.200q (isError %v)", out, isErr)
	}

}

func TestMCP_LibrariesResolvesFromTheForgeModule(t *testing.T) {
	if testing.Short() {
		t.Skip("libraries runs `go list` to resolve forge/pkg; runs in task test")
	}
	// This package sits inside the forge module, which `go list` can resolve.
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cs := connectMCP(t, t.TempDir())
	out, isErr := callMCP(t, cs, "libraries", map[string]any{"dir": repo, "packages": []string{"svcerr"}})
	if isErr || !strings.Contains(out, "svcerr") {
		t.Errorf("libraries = %.300q (isError %v)", out, isErr)
	}
}

func TestMCP_FailuresAreToolErrors(t *testing.T) {
	cs := connectMCP(t, t.TempDir())

	out, isErr := callMCP(t, cs, "skill_load", map[string]any{"name": "no-such-skill-anywhere"})
	if !isErr || !strings.Contains(out, "not found") {
		t.Errorf("unknown skill = %q (isError %v), want a tool error", out, isErr)
	}

	out, isErr = callMCP(t, cs, "shapes", map[string]any{"dir": filepath.Join(t.TempDir(), "missing")})
	if !isErr {
		t.Errorf("a missing dir must be a tool error, got %q", out)
	}
}

func TestMCP_CallsDoNotLeakProcessState(t *testing.T) {
	proj := t.TempDir()
	writeMCPFixture(t, proj)
	start := t.TempDir()
	cs := connectMCP(t, start)

	wdBefore, _ := os.Getwd()
	stdout, stderr, stdin := os.Stdout, os.Stderr, os.Stdin
	if _, isErr := callMCP(t, cs, "shapes", map[string]any{"dir": proj}); isErr {
		t.Fatal("shapes failed")
	}
	if wd, _ := os.Getwd(); wd != wdBefore {
		t.Errorf("working directory leaked: %s -> %s", wdBefore, wd)
	}
	if os.Stdout != stdout || os.Stderr != stderr || os.Stdin != stdin {
		t.Error("stdio was not restored after the call")
	}
}

func TestTruncateMCPOutputKeepsTheVerdict(t *testing.T) {
	big := strings.Repeat("x", maxMCPOutputHead+maxMCPOutputTail+5000) + "\nVERDICT: fail\n"
	got := truncateMCPOutput(big)
	if !strings.HasSuffix(got, "VERDICT: fail\n") {
		t.Error("the last line (lint's verdict) must survive truncation")
	}
	if !strings.Contains(got, "bytes omitted") || len(got) >= len(big) {
		t.Errorf("not truncated: %d -> %d", len(big), len(got))
	}
	if small := "short\n"; truncateMCPOutput(small) != small {
		t.Error("small output must pass through unchanged")
	}
}
