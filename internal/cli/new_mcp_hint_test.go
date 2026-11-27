package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMCPRestartHint(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	withForge := `{"mcpServers":{"forge":{"command":"forge","args":["mcp"]}}}`
	for _, tc := range []struct {
		name   string
		body   string
		hadMCP bool
		want   bool
	}{
		{"forge written by this run", withForge, false, true},
		{"the user's own file", withForge, true, false},
		{"no forge server (reliant harness)", `{"mcpServers":{"reliant-docs":{"command":"npx","args":[]}}}`, false, false},
		{"no file (harness none)", "", false, false},
		{"malformed", `{"mcpServers":`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpRestartHint(write(t, tc.body), tc.hadMCP)
			if (got != "") != tc.want {
				t.Fatalf("hint = %q, want printed=%v", got, tc.want)
			}
			if got != "" && (strings.Contains(got, "\n") || !strings.HasPrefix(got, "Optional: restart your agent session")) {
				t.Errorf("hint must be one line saying the restart is optional: %q", got)
			}
			if got != "" && (!strings.Contains(got, "Nothing waits on it") || !strings.Contains(got, "every tool is also a CLI command")) {
				t.Errorf("hint must say nothing waits on the restart and name the CLI twins: %q", got)
			}
		})
	}
}

// TestMCPRestartHintNamesRealTools holds every tool the hint promises to a
// tool forge's MCP server actually lists, so a renamed tool cannot leave the
// hint naming one that does not exist.
func TestMCPRestartHintNamesRealTools(t *testing.T) {
	asForgeBinary(t) // the hint and the check below must agree on Name()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"forge":{"command":"forge","args":["mcp"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hint := mcpRestartHint(dir, false)
	named := regexp.MustCompile(`(?:: |, )([a-z_]+) \(`).FindAllStringSubmatch(hint, -1)
	if len(named) < 2 {
		t.Fatalf("hint names %d tools, want 2-3: %q", len(named), hint)
	}
	list, err := connectMCP(t, t.TempDir()).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range list.Tools {
		have[tool.Name] = true
	}
	for _, m := range named {
		if !have[m[1]] {
			t.Errorf("hint names tool %q, which forge mcp does not list", m[1])
		}
	}

	// Each tool's CLI twin is a command the root tree resolves.
	twins := regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(hint, -1)
	if len(twins) != len(named) {
		t.Fatalf("hint names %d tools but %d CLI twins: %q", len(named), len(twins), hint)
	}
	root := newTestRootCmd(t)
	for _, tw := range twins {
		line := tw[1]
		rest, ok := strings.CutPrefix(line, Name()+" ")
		if !ok {
			t.Errorf("CLI twin %q does not start with %q", line, Name())
			continue
		}
		cmd, _, err := root.Find(strings.Fields(rest))
		if err != nil || cmd == nil || cmd == root || !strings.HasPrefix(root.Name()+" "+rest+" ", cmd.CommandPath()+" ") {
			t.Errorf("CLI twin %q does not resolve to a forge command (got %v, %v)", line, cmd, err)
		}
	}
}
