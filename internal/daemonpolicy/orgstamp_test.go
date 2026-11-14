// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"encoding/json"
	"testing"
)

// A stamp crosses the wire as a decoded JSON object in the context map and
// reads back the same.
func TestOrgStampRoundTripsThroughTheContextMap(t *testing.T) {
	stamp := &OrgStamp{OrgID: "org-a", Version: 4, Credentials: CredentialsNone,
		MCP: &OrgStampMCP{AllowedServers: []string{"github"}}}
	raw, err := json.Marshal(map[string]interface{}{OrgStampContextKey: stamp.ContextValue()})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]interface{}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	got := OrgStampFrom(wire)
	if got == nil || got.OrgID != "org-a" || got.Version != 4 || !got.CredentialsForced() ||
		got.MCP == nil || len(got.MCP.AllowedServers) != 1 {
		t.Fatalf("round trip = %+v", got)
	}
}

// No stamp, and a stamp that asks for nothing, change nothing.
func TestOrgStampAbsentAllowsEverything(t *testing.T) {
	var none *OrgStamp
	if none.CredentialsForced() || !none.MCPServerAllowed("x") || !none.MCPToolAllowed("mcp__x__y") || !none.Empty() {
		t.Fatal("a nil stamp must ask for nothing")
	}
	if none.ContextValue() != nil || (&OrgStamp{OrgID: "org-a"}).ContextValue() != nil {
		t.Fatal("a stamp that asks for nothing is not carried")
	}
	if OrgStampFrom(map[string]interface{}{}) != nil || OrgStampFrom(map[string]interface{}{OrgStampContextKey: "garbage"}) != nil {
		t.Fatal("no stamp, or one this build cannot read, is nil")
	}
	ctx := WithOrgStamp(context.Background(), &OrgStamp{OrgID: "org-a"})
	if OrgStampFromContext(ctx) != nil || OrgStampFromContext(context.Background()) != nil {
		t.Fatal("an empty stamp is not put on the context")
	}
}

func TestOrgStampMCPAllowlist(t *testing.T) {
	s := &OrgStamp{MCP: &OrgStampMCP{AllowedServers: []string{"github", "my__srv"}}}
	cases := map[string]bool{
		"mcp__github__create_issue": true,
		"mcp__my__srv__tool":        true, // a server whose own name has "__"
		"mcp__slack__post":          false,
		"mcp__githubx__tool":        false, // a prefix of the name is not the server
		"shell":                     true,  // not an MCP tool
	}
	for name, want := range cases {
		if got := s.MCPToolAllowed(name); got != want {
			t.Errorf("MCPToolAllowed(%q) = %v, want %v", name, got, want)
		}
	}
	if !s.MCPServerAllowed("github") || s.MCPServerAllowed("slack") {
		t.Error("MCPServerAllowed does not follow the allowlist")
	}
	none := &OrgStamp{MCP: &OrgStampMCP{}}
	if none.MCPServerAllowed("github") || none.MCPToolAllowed("mcp__github__x") {
		t.Error("an empty allowlist admits none")
	}
	all := &OrgStamp{MCP: &OrgStampMCP{AllowAll: true}}
	if !all.MCPServerAllowed("anything") || !all.MCPToolAllowed("mcp__anything__x") {
		t.Error("allow_all admits every server")
	}
}
