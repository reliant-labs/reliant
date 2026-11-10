// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// controlPlaneMintContract is a FAITHFUL FAKE of the control plane's token
// authority, as control-plane #2400 defines it (docs/design/orgs-and-members.md
// §4.1). It implements the contract the control plane pins in
// internal/db TestMintForUser_ExchangeContract_BoundGrantSpentPerUse:
//
//   - Introspect resolves a person-acting token that may act on every
//     environment (unbound, or bound to a daemon) to its person's ORG-WIDE
//     grants only, and only while they are a member, then bounds it by the
//     daemon's scope policy. A wildcard token reports AllScopes.
//   - MintForUser keeps a bindable scope in the child's CEILING only when the
//     person holds it on at least one environment (HeldAnywhere), and only
//     inside a daemon's policy; it refuses a non-member, and refuses a mint
//     left holding nothing ("permission_denied", as the real client reports).
//     The child keeps the requested binding.
//   - spendsOn is the per-use check (control-plane accesstoken.ScopesOn): the
//     child's ceiling, for a live member, clipped to org-wide ∪ bound-to-env
//     grants and the daemon's policy.
//
// Scopes the person owns (daemon:connect, reliant:api, llm:invoke) need no
// grant; every other scope here is an org permission.
type controlPlaneMintContract struct {
	mu       sync.Mutex
	org      string
	member   map[string]bool
	orgWide  map[string]fat.Set            // user → org-wide grants
	byEnv    map[string]map[string]fat.Set // user → env → bound grants
	policies map[string]fat.Set            // daemon → policy (absent: inherit)
	tokens   map[string]*contractToken     // plaintext → token
	seq      int
}

type contractToken struct {
	id, user string
	ceiling  fat.Set
	all      bool
	resource *fat.Resource
	revoked  bool
}

var personalScopes = fat.SetOf(fat.ScopeDaemonConnect, fat.ScopeReliantAPI, fat.ScopeLLMInvoke)

func newControlPlaneMintContract(org string) *controlPlaneMintContract {
	return &controlPlaneMintContract{
		org: org, member: map[string]bool{}, orgWide: map[string]fat.Set{},
		byEnv: map[string]map[string]fat.Set{}, policies: map[string]fat.Set{}, tokens: map[string]*contractToken{},
	}
}

// issue stores a parent credential, as a daemon registration or a login would.
func (c *controlPlaneMintContract) issue(user string, all bool, ceiling fat.Set, res *fat.Resource) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store(user, all, ceiling, res)
}

func (c *controlPlaneMintContract) store(user string, all bool, ceiling fat.Set, res *fat.Resource) string {
	c.seq++
	minted, err := fat.Mint()
	if err != nil {
		panic(err)
	}
	c.tokens[minted.Plaintext] = &contractToken{id: fmt.Sprintf("tok-%d", c.seq), user: user, ceiling: ceiling, all: all, resource: res}
	return minted.Plaintext
}

// grants is the person's grants for env ("": org-wide only; "*": anywhere).
func (c *controlPlaneMintContract) grants(user, env string) fat.Set {
	out := fat.Set{}
	if !c.member[user] {
		return out
	}
	for s := range c.orgWide[user] {
		out[s] = struct{}{}
	}
	for e, bound := range c.byEnv[user] {
		if env == "*" || e == env {
			for s := range bound {
				out[s] = struct{}{}
			}
		}
	}
	return out
}

// clip is the control plane's resolution pipeline: ceiling (the wildcard is
// every scope) ∩ the person's grants in view ∩ the daemon's policy.
func (c *controlPlaneMintContract) clip(user string, all bool, ceiling fat.Set, res *fat.Resource, view string) fat.Set {
	held := c.grants(user, view)
	candidates := ceiling
	if all {
		candidates = fat.SetOf(fat.AllScopes...)
	}
	out := fat.Set{}
	for s := range candidates {
		if personalScopes.Has(s) || held.Has(s) {
			out[s] = struct{}{}
		}
	}
	if res != nil && res.Kind == fat.ResourceDaemon {
		if policy, ok := c.policies[res.ID]; ok {
			for s := range out {
				if !personalScopes.Has(s) && !policy.Has(s) {
					delete(out, s)
				}
			}
		}
	}
	return out
}

func (c *controlPlaneMintContract) Introspect(_ context.Context, token string) (*fat.Principal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[token]
	if !ok || t.revoked {
		return nil, tokenauthority.ErrInactive
	}
	return &fat.Principal{
		TokenID: t.id, OrgID: c.org, ActingUserID: t.user,
		Scopes: c.clip(t.user, t.all, t.ceiling, t.resource, ""), AllScopes: t.all, Resource: t.resource,
	}, nil
}

func (c *controlPlaneMintContract) MintForUser(_ context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.OrgID != "" && (req.OrgID != c.org || !c.member[req.UserID]) {
		return tokenauthority.Minted{}, &accesstokenclient.RPCError{Code: "permission_denied", Message: "not a member"}
	}
	view := "*"
	if req.Resource != nil && req.Resource.Kind == fat.ResourceEnvironment {
		view = req.Resource.ID
	}
	ceiling := c.clip(req.UserID, false, fat.SetOf(req.Scopes...), req.Resource, view)
	if len(ceiling) == 0 {
		return tokenauthority.Minted{}, &accesstokenclient.RPCError{Code: "permission_denied", Message: "your organization permissions do not include them"}
	}
	plaintext := c.store(req.UserID, false, ceiling, req.Resource)
	return tokenauthority.Minted{TokenID: c.tokens[plaintext].id, Plaintext: plaintext, Scopes: ceiling.Strings()}, nil
}

// spendsOn is the control plane's per-use check for one environment.
func (c *controlPlaneMintContract) spendsOn(token, env string, scope fat.Scope) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[token]
	if !ok || t.revoked {
		return false
	}
	return c.clip(t.user, t.all, t.ceiling, t.resource, env).Has(scope)
}

func (c *controlPlaneMintContract) resource(token string) *fat.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens[token].resource
}

func (c *controlPlaneMintContract) ListForUser(context.Context, string, fat.Scope) ([]tokenauthority.TokenInfo, error) {
	return nil, nil
}

func (c *controlPlaneMintContract) RevokeForUser(_ context.Context, _, tokenID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tokens {
		if t.id == tokenID {
			t.revoked = true
		}
	}
	return nil
}

func (c *controlPlaneMintContract) UpdateForUser(context.Context, string, string, *string, fat.Set) (tokenauthority.TokenInfo, error) {
	return tokenauthority.TokenInfo{}, fmt.Errorf("not used by ExchangeToken")
}

// THE CALLER CHAIN, end to end: a wildcard session's introspection (org-wide
// grants only) → ExchangeToken → the control plane's mint clipping → the
// exchanged child's authorization on each environment. A release manager
// holds deploy:write on prod only. Both parents a credential helper starts
// from: a daemon-bound daemon credential and an unbound login.
func TestExchangeToken_CallerChain_EnvironmentBoundGrant(t *testing.T) {
	const org, user, prod, staging = "org-1", "user-1", "env-prod", "env-staging"
	write := fat.ScopeDeployWrite
	for _, parent := range []struct {
		name     string
		resource *fat.Resource
	}{
		{"daemon-bound parent", &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"}},
		{"unbound login parent", nil},
	} {
		for _, tc := range []struct {
			name string
			// arrange sets the person's standing before the exchange.
			arrange func(c *controlPlaneMintContract)
			// daemonOnly rows need a daemon binding to mean anything.
			daemonOnly bool
			minted     bool
		}{
			{name: "prod-only grant", arrange: func(*controlPlaneMintContract) {}, minted: true},
			{name: "no held grant", arrange: func(c *controlPlaneMintContract) { delete(c.byEnv, user) }},
			{name: "daemon policy excludes deploy:write", daemonOnly: true, arrange: func(c *controlPlaneMintContract) {
				c.policies["daemon-1"] = fat.SetOf(fat.ScopeDeployRead)
			}},
			{name: "membership removed", arrange: func(c *controlPlaneMintContract) { c.member[user] = false }},
		} {
			if tc.daemonOnly && parent.resource == nil {
				continue
			}
			t.Run(parent.name+"/"+tc.name, func(t *testing.T) {
				cp := newControlPlaneMintContract(org)
				cp.member[user] = true
				cp.orgWide[user] = fat.SetOf(fat.ScopeDeployRead)
				cp.byEnv[user] = map[string]fat.Set{prod: fat.SetOf(write)}
				parentToken := cp.issue(user, true, fat.SetOf(fat.ScopeDaemonConnect), parent.resource)
				tc.arrange(cp)

				// The session resolves without the bound scope: the clip never widens.
				if p, err := cp.Introspect(context.Background(), parentToken); err == nil && p.Scopes.Has(write) {
					t.Fatalf("the parent resolves with deploy:write: %s", p.Scopes)
				}

				svc := newTokenService(t, cp, TokenControlPlane{Issuer: testControlPlane})
				resp, err := svc.ExchangeToken(context.Background(), exchangeReq(parentToken, &reliantv1.ExchangeTokenRequest{
					Audience: testControlPlane, Scopes: []string{string(fat.ScopeDeployRead), string(write)},
				}))
				child := ""
				if err == nil {
					child = resp.Msg.GetToken()
				} else if svcerr.Code(err) != connect.CodePermissionDenied {
					t.Fatalf("ExchangeToken: %v, want success or PermissionDenied", err)
				}

				prodOK := child != "" && cp.spendsOn(child, prod, write)
				stagingOK := child != "" && cp.spendsOn(child, staging, write)
				if prodOK != tc.minted {
					t.Errorf("child deploy:write on prod = %v, want %v (exchange err %v)", prodOK, tc.minted, err)
				}
				if stagingOK {
					t.Error("the child deploys staging, where the person holds no grant")
				}
				if child == "" {
					return
				}
				if got := cp.resource(child); fmt.Sprint(got) != fmt.Sprint(parent.resource) {
					t.Errorf("child binding = %v, want the parent's %v", got, parent.resource)
				}
				if p, err := cp.Introspect(context.Background(), child); err != nil || p.Scopes.Has(write) {
					t.Errorf("the child resolves with deploy:write (%v): a bound grant rode the org-wide clip", err)
				}
				// A removal after the exchange takes the child's use away too.
				cp.mu.Lock()
				cp.member[user] = false
				cp.mu.Unlock()
				if cp.spendsOn(child, prod, write) {
					t.Error("the child still deploys prod after its person left the org")
				}
			})
		}
	}
}
