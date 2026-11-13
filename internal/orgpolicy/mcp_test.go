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
		"mcp__my__server__tool":      true, // a server name with "__" in it
		"mcp__slack__post":           false,
		"mcp__githubx__create_issue": false,
		"mcp__github__":              false,
		"mcp__invented__x":           false,
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
	if adm := MCPServerAdmission(doc, "github__evil"); adm.Admitted {
		t.Errorf("exact server admission: %+v", adm)
	}
}
