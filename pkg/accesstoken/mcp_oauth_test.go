package accesstoken

import (
	"errors"
	"testing"
	"time"
)

func mcpOAuthGrant() Grant {
	return Grant{OrgID: "o", Name: "Claude@mcp", Scopes: SetOf(ScopeMCPOAuth), ActingUserID: "u",
		ClientID: "mcp_abc", Audience: "https://api.example.com/mcp"}
}

func TestMCPOAuthGrantShape(t *testing.T) {
	now := time.Now()
	if err := mcpOAuthGrant().Validate(now); err != nil {
		t.Fatalf("a well-formed mcp:oauth grant was refused: %v", err)
	}
	cases := map[string]func(*Grant){
		"no acting user":      func(g *Grant) { g.ActingUserID = "" },
		"no client":           func(g *Grant) { g.ClientID = "" },
		"no audience":         func(g *Grant) { g.Audience = "" },
		"relative audience":   func(g *Grant) { g.Audience = "/mcp" },
		"audience fragment":   func(g *Grant) { g.Audience = "https://api.example.com/mcp#x" },
		"audience userinfo":   func(g *Grant) { g.Audience = "https://a@api.example.com/mcp" },
		"audience scheme":     func(g *Grant) { g.Audience = "ftp://api.example.com/mcp" },
		"audience whitespace": func(g *Grant) { g.Audience = " https://api.example.com/mcp" },
		"second scope":        func(g *Grant) { g.Scopes = SetOf(ScopeMCPOAuth, ScopeReliantAPI) },
		"bound":               func(g *Grant) { g.Resource = &Resource{Kind: ResourceDaemon, ID: "d"} },
		"ephemeral":           func(g *Grant) { g.Ephemeral = true },
		"audience on api":     func(g *Grant) { g.Scopes = SetOf(ScopeReliantAPI) },
		"client on api":       func(g *Grant) { g.Scopes, g.Audience = SetOf(ScopeReliantAPI), "" },
	}
	for name, mutate := range cases {
		g := mcpOAuthGrant()
		mutate(&g)
		if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: Validate = %v, want ErrInvalidGrant", name, err)
		}
	}
}

func TestIsMCPOAuthFor(t *testing.T) {
	g := mcpOAuthGrant()
	p := &Principal{Scopes: g.Scopes, ActingUserID: g.ActingUserID, ClientID: g.ClientID, Audience: g.Audience}
	if !p.IsMCPOAuthFor("https://api.example.com/mcp") {
		t.Fatal("the token was refused for its own audience")
	}
	for name, q := range map[string]*Principal{
		"other resource": p,
		"nil":            nil,
	} {
		if q.IsMCPOAuthFor("https://api.example.com/forge/mcp") {
			t.Errorf("%s: accepted for another resource", name)
		}
	}
	if p.IsMCPOAuthFor("") {
		t.Error("accepted for an empty resource")
	}
	wild := *p
	wild.AllScopes = true
	bound := *p
	bound.Resource = &Resource{Kind: ResourceDaemon, ID: "d"}
	api := *p
	api.Scopes = SetOf(ScopeMCPOAuth, ScopeReliantAPI)
	noUser := *p
	noUser.ActingUserID = ""
	noClient := *p
	noClient.ClientID = ""
	for name, q := range map[string]Principal{"wildcard": wild, "bound": bound, "extra scope": api, "no user": noUser, "no client": noClient} {
		if q.IsMCPOAuthFor(p.Audience) {
			t.Errorf("%s: accepted", name)
		}
	}
}
