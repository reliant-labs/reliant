package generator

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestMCPJSONRegistersForgeForClaudeOnly renders .mcp.json for each harness:
// Claude Code, the one harness that loads a project's .mcp.json, gets forge's
// own MCP server run by the same CLI that scaffolded the project; every other
// harness gets the file without it. Every rendering must be valid JSON.
func TestMCPJSONRegistersForgeForClaudeOnly(t *testing.T) {
	for _, tc := range []struct {
		harness Harness
		cli     string
		want    *[2]any // command, args; nil: no forge entry
	}{
		{HarnessClaude, "forge", &[2]any{"forge", []any{"mcp"}}},
		{HarnessClaude, "reliant forge", &[2]any{"reliant", []any{"forge", "mcp"}}},
		{HarnessReliant, "reliant forge", nil},
		{HarnessCursor, "forge", nil},
		{HarnessCodex, "forge", nil},
	} {
		t.Run(string(tc.harness)+"/"+tc.cli, func(t *testing.T) {
			out, err := templates.ProjectTemplates().Render("mcp.json.tmpl", newMCPJSONData(tc.harness, tc.cli))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				MCPServers map[string]struct {
					Command string `json:"command"`
					Args    []any  `json:"args"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf(".mcp.json is not valid JSON: %v\n%s", err, out)
			}
			for _, keep := range []string{"chrome-devtools", "reliant-docs"} {
				if _, ok := doc.MCPServers[keep]; !ok {
					t.Errorf("lost the %s server:\n%s", keep, out)
				}
			}
			forge, ok := doc.MCPServers["forge"]
			if tc.want == nil {
				if ok {
					t.Errorf("harness %s must not register forge:\n%s", tc.harness, out)
				}
				return
			}
			if !ok || forge.Command != tc.want[0] || !reflect.DeepEqual(forge.Args, tc.want[1]) {
				t.Errorf("forge entry = %+v (present %v), want command %v args %v", forge, ok, tc.want[0], tc.want[1])
			}
		})
	}
}
