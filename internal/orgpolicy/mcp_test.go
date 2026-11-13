// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"testing"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
)

func TestMCPToolAdmission(t *testing.T) {
	doc := &controlplanev1.OrgPolicy{Mcp: &controlplanev1.McpPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, AllowedServers: []string{"github", "my__server"},
	}}
	for name, want := range map[string]bool{
		"mcp__github__create_issue":  true,
		"mcp__slack__post":           false,
		"mcp__githubx__create_issue": false,
		"mcp__github__":              false,
		"mcp__invented__x":           false,
		// Ambiguous names are admitted only when every server they could
		// belong to is listed: github__evil's tool x looks like github's
		// tool evil__x.
		"mcp__github__evil__exfiltrate": false,
		// my__server is listed, but the name could be my's tool server__tool.
		"mcp__my__server__tool": false,
		// my and my__server both listed: every reading is allowed.
		"mcp__my__x": false,
	} {
		adm := MCPToolAdmission(doc, name)
		if !adm.Governed || adm.Admitted != want {
			t.Errorf("%s: admission %+v, want admitted=%v", name, adm, want)
		}
	}
	if adm := MCPToolAdmission(doc, "shell"); adm.Governed {
		t.Errorf("a tool that is not MCP is not governed: %+v", adm)
	}
	if adm := MCPToolAdmission(&controlplanev1.OrgPolicy{}, "mcp__slack__post"); adm.Governed {
		t.Errorf("no mcp section governs nothing: %+v", adm)
	}
	all := &controlplanev1.OrgPolicy{Mcp: &controlplanev1.McpPolicy{AllowAll: true}}
	if adm := MCPToolAdmission(all, "mcp__slack__post"); !adm.Admitted {
		t.Errorf("allow_all admits every server: %+v", adm)
	}
	both := &controlplanev1.OrgPolicy{Mcp: &controlplanev1.McpPolicy{AllowedServers: []string{"my", "my__server"}}}
	if adm := MCPToolAdmission(both, "mcp__my__server__tool"); !adm.Admitted {
		t.Errorf("every server the name could belong to is listed: %+v", adm)
	}
	if adm := MCPServerAdmission(doc, "github__evil"); adm.Admitted {
		t.Errorf("exact server admission: %+v", adm)
	}
}

func TestMCPServersOf(t *testing.T) {
	for name, want := range map[string][]string{
		"mcp__github__create_issue":     {"github"},
		"mcp__github__evil__exfiltrate": {"github", "github__evil"},
		"mcp__a___b":                    {"a", "a_"},
		"mcp__github__":                 nil,
		"mcp____x":                      nil,
	} {
		got := mcpServersOf(name)
		if len(got) != len(want) {
			t.Errorf("%s: %v, want %v", name, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: %v, want %v", name, got, want)
			}
		}
	}
}
