package accesstoken

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mcpForgeGrant() Grant {
	return Grant{OrgID: "o", Name: "Claude (Reliant Forge MCP)", Scopes: SetOf(ScopeMCPForge), ActingUserID: "u",
		ClientID: "mcp_abc", Audience: "https://api.example.com/forge/mcp"}
}

func TestMCPForgeGrantShape(t *testing.T) {
	now := time.Now()
	if err := mcpForgeGrant().Validate(now); err != nil {
		t.Fatalf("a well-formed mcp:forge grant was refused: %v", err)
	}
	cases := map[string]func(*Grant){
		"no acting user":    func(g *Grant) { g.ActingUserID = "" },
		"no client":         func(g *Grant) { g.ClientID = "" },
		"no audience":       func(g *Grant) { g.Audience = "" },
		"relative audience": func(g *Grant) { g.Audience = "/forge/mcp" },
		"audience fragment": func(g *Grant) { g.Audience = "https://api.example.com/forge/mcp#x" },
		"second scope":      func(g *Grant) { g.Scopes = SetOf(ScopeMCPForge, ScopeDeployRead) },
		"both MCP scopes":   func(g *Grant) { g.Scopes = SetOf(ScopeMCPForge, ScopeMCPOAuth) },
		"bound":             func(g *Grant) { g.Resource = &Resource{Kind: ResourceDaemon, ID: "d"} },
		"ephemeral":         func(g *Grant) { g.Ephemeral = true },
		"audience on read":  func(g *Grant) { g.Scopes = SetOf(ScopeDeployRead) },
	}
	for name, mutate := range cases {
		g := mcpForgeGrant()
		mutate(&g)
		if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: Validate = %v, want ErrInvalidGrant", name, err)
		}
	}
}

// The two MCP servers' tokens are not interchangeable, whatever the audience.
func TestIsMCPForgeFor(t *testing.T) {
	const forge, mcp = "https://api.example.com/forge/mcp", "https://api.example.com/mcp"
	g := mcpForgeGrant()
	p := &Principal{Scopes: g.Scopes, ActingUserID: g.ActingUserID, ClientID: g.ClientID, Audience: g.Audience}
	if !p.IsMCPForgeFor(forge) {
		t.Fatal("the token was refused for its own audience")
	}
	if p.IsMCPForgeFor(mcp) || p.IsMCPForgeFor("") || (*Principal)(nil).IsMCPForgeFor(forge) {
		t.Error("accepted for another, empty or nil resource")
	}
	if p.IsMCPOAuthFor(forge) {
		t.Error("an mcp:forge token passed the mcp:oauth check")
	}
	oauth := &Principal{Scopes: SetOf(ScopeMCPOAuth), ActingUserID: "u", ClientID: "mcp_abc", Audience: forge}
	if oauth.IsMCPForgeFor(forge) {
		t.Error("an mcp:oauth token passed the mcp:forge check")
	}
	wild := *p
	wild.AllScopes = true
	bound := *p
	bound.Resource = &Resource{Kind: ResourceDaemon, ID: "d"}
	extra := *p
	extra.Scopes = SetOf(ScopeMCPForge, ScopeDeployRead)
	noUser := *p
	noUser.ActingUserID = ""
	noClient := *p
	noClient.ClientID = ""
	for name, q := range map[string]Principal{"wildcard": wild, "bound": bound, "extra scope": extra, "no user": noUser, "no client": noClient} {
		if q.IsMCPForgeFor(forge) {
			t.Errorf("%s: accepted", name)
		}
	}
}

// mcp:forge implies nothing and nothing implies it: its authority is the
// per-procedure scope a control plane checks against the person.
func TestMCPForgePermitsNothingElse(t *testing.T) {
	for _, s := range AllScopes {
		if s != ScopeMCPForge && SetOf(ScopeMCPForge).Permits(s) {
			t.Errorf("mcp:forge permits %s", s)
		}
		if s != ScopeMCPForge && SetOf(s).Permits(ScopeMCPForge) {
			t.Errorf("%s permits mcp:forge", s)
		}
	}
}

// The table names Connect procedures of the control plane, and only the
// read scopes plus deploy:write: never a scope that administers tokens,
// secrets, members or data.
func TestMCPForgeProceduresTable(t *testing.T) {
	allowed := map[Scope]bool{ScopeDeployRead: true, ScopeDeployWrite: true, ScopeObsRead: true}
	procs := MCPForgeProcedures()
	if len(procs) == 0 {
		t.Fatal("empty table")
	}
	for _, p := range procs {
		if !strings.HasPrefix(p, "/controlplane.v1.") || strings.Count(p, "/") != 2 {
			t.Errorf("%q is not a control-plane Connect procedure path", p)
		}
		s, ok := MCPForgeScopeFor(p)
		if !ok || !allowed[s] {
			t.Errorf("%s needs %q, outside the hosted tools' scopes", p, s)
		}
	}
	for _, p := range []string{
		"/controlplane.v1.AccessTokenService/ExchangeToken",
		"/controlplane.v1.TelemetryTokenService/CreateIngestToken",
		"/controlplane.v1.DaemonService/ResumeDaemon",
		"/controlplane.v1.DeployService/GetTenant",
		"",
	} {
		if _, ok := MCPForgeScopeFor(p); ok {
			t.Errorf("%q is in the table", p)
		}
	}
}
