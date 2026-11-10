// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

				svc := newTokenService(t, cp.serveWire(t), TokenControlPlane{Issuer: testControlPlane})
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

// serveWire exposes the contract over control-plane's
// AccessTokenInternalService wire protocol and returns the PRODUCTION client
// (accesstokenclient.Client) pointed at it, so ExchangeToken's introspections
// go through the real decoder: an explicit-list token that resolves to no
// org-wide scope is refused there exactly as against a real control plane.
func (c *controlPlaneMintContract) serveWire(t *testing.T) accesstokenclient.Service {
	t.Helper()
	type resource struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	toResource := func(r *fat.Resource) *resource {
		if r == nil {
			return nil
		}
		return &resource{Kind: string(r.Kind), ID: r.ID}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token    string    `json:"token"`
			UserID   string    `json:"userId"`
			TokenID  string    `json:"tokenId"`
			Name     string    `json:"name"`
			Scopes   []string  `json:"scopes"`
			OrgID    string    `json:"orgId"`
			Resource *resource `json:"resource"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reply := func(status int, body any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		}
		switch strings.TrimPrefix(r.URL.Path, accesstokenclient.ServicePath) {
		case "Introspect":
			p, err := c.Introspect(r.Context(), in.Token)
			if err != nil {
				reply(http.StatusOK, map[string]any{"active": false})
				return
			}
			reply(http.StatusOK, map[string]any{
				"active": true, "tokenId": p.TokenID, "orgId": p.OrgID, "actingUserId": p.ActingUserID,
				"scopes": p.Scopes.Strings(), "allScopes": p.AllScopes, "resource": toResource(p.Resource),
			})
		case "MintForUser":
			req := tokenauthority.MintRequest{UserID: in.UserID, Name: in.Name, OrgID: in.OrgID}
			for _, raw := range in.Scopes {
				scope, ok := fat.ParseScope(raw)
				if !ok {
					reply(http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": raw})
					return
				}
				req.Scopes = append(req.Scopes, scope)
			}
			if in.Resource != nil {
				req.Resource = &fat.Resource{Kind: fat.ResourceKind(in.Resource.Kind), ID: in.Resource.ID}
			}
			m, err := c.MintForUser(r.Context(), req)
			var refused *accesstokenclient.RPCError
			if errors.As(err, &refused) {
				reply(http.StatusForbidden, map[string]any{"code": refused.Code, "message": refused.Message})
				return
			}
			reply(http.StatusOK, map[string]any{"tokenId": m.TokenID, "secret": m.Plaintext, "scopes": m.Scopes})
		case "RevokeForUser":
			_ = c.RevokeForUser(r.Context(), in.UserID, in.TokenID)
			reply(http.StatusOK, map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return accesstokenclient.New(accesstokenclient.Deps{BaseURL: srv.URL, Sign: func() (string, error) { return "internal", nil }})
}

// RC-10: an exchange asking ONLY for a scope the person holds bound to one
// environment. The child's org-wide resolution is empty, so the production
// decoder refuses to authenticate it; the exchange must still confirm the
// child's org and hand it on (it authorizes nothing here, and the control
// plane judges each use on its environment), not revoke it as an outage.
func TestExchangeToken_CallerChain_EnvironmentOnlyScope(t *testing.T) {
	const org, user, prod, staging = "org-1", "user-1", "env-prod", "env-staging"
	query := fat.ScopeDBQuery
	for _, parent := range []struct {
		name     string
		resource *fat.Resource
	}{
		{"daemon-bound parent", &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"}},
		{"unbound login parent", nil},
	} {
		for _, tc := range []struct {
			name    string
			arrange func(c *controlPlaneMintContract)
			minted  bool
		}{
			{name: "prod-only grant", arrange: func(*controlPlaneMintContract) {}, minted: true},
			{name: "membership removed", arrange: func(c *controlPlaneMintContract) { c.member[user] = false }},
		} {
			t.Run(parent.name+"/"+tc.name, func(t *testing.T) {
				cp := newControlPlaneMintContract(org)
				cp.member[user] = true
				// No org-wide grant at all: db:query on prod is all they hold.
				cp.byEnv[user] = map[string]fat.Set{prod: fat.SetOf(query)}
				parentToken := cp.issue(user, true, nil, parent.resource)
				tc.arrange(cp)

				authority := cp.serveWire(t)
				svc := newTokenService(t, authority, TokenControlPlane{Issuer: testControlPlane})
				resp, err := svc.ExchangeToken(context.Background(), exchangeReq(parentToken, &reliantv1.ExchangeTokenRequest{
					Audience: testControlPlane, Scopes: []string{string(query)},
				}))
				if !tc.minted {
					if svcerr.Code(err) != connect.CodePermissionDenied {
						t.Fatalf("ExchangeToken: %v, want PermissionDenied", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("ExchangeToken: %v", err)
				}
				child := resp.Msg.GetToken()
				if got := resp.Msg.GetScopes(); len(got) != 1 || got[0] != string(query) {
					t.Errorf("granted %v, want [%s]", got, query)
				}
				if resp.Msg.GetOrgId() != org {
					t.Errorf("org = %q, want %q", resp.Msg.GetOrgId(), org)
				}
				if !cp.spendsOn(child, prod, query) {
					t.Error("the child cannot query prod, where the person holds db:query")
				}
				if cp.spendsOn(child, staging, query) {
					t.Error("the child queries staging, where the person holds no grant")
				}
				// It authenticates as nothing at this server: no org-wide authority.
				var scopeless *accesstokenclient.ScopelessTokenError
				if _, err := authority.Introspect(context.Background(), child); !errors.As(err, &scopeless) || scopeless.OrgID != org {
					t.Errorf("introspecting the child = %v, want a ScopelessTokenError in %s", err, org)
				}
				cp.mu.Lock()
				cp.member[user] = false
				cp.mu.Unlock()
				if cp.spendsOn(child, prod, query) {
					t.Error("the child still queries prod after its person left the org")
				}
			})
		}
	}
}
