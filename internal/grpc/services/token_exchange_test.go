// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

const testControlPlane = "https://admin.example.com"

// mintSession mints a session credential straight into the authority, as a
// daemon registration would have.
func mintSession(t *testing.T, authority tokenauthority.Authority, userID string, req tokenauthority.MintRequest) string {
	t.Helper()
	req.UserID = userID
	if req.Name == "" {
		req.Name = "session"
	}
	minted, err := authority.MintForUser(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return minted.Plaintext
}

func exchangeReq(bearer string, msg *reliantv1.ExchangeTokenRequest) *connect.Request[reliantv1.ExchangeTokenRequest] {
	r := connect.NewRequest(msg)
	r.Header().Set("Authorization", "Bearer "+bearer)
	return r
}

var fullDaemon = []fat.Scope{
	fat.ScopeDaemonConnect,
	fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite,
	fat.ScopeDomainRead, fat.ScopeDomainWrite,
}

func TestExchangeToken_InheritsBindingAndNarrows(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := NewTokenService(authority, TokenControlPlane{Issuer: testControlPlane})
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{
		Scopes: fullDaemon, Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"},
	})

	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
		Audience: testControlPlane, Scopes: []string{"deploy:read"},
	}))
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	p, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Scopes.Has(fat.ScopeDeployRead) || len(p.Scopes) != 1 {
		t.Errorf("scopes = %s, want exactly what was requested", p.Scopes)
	}
	if !p.BoundTo(fat.ResourceDaemon, "daemon-1") {
		t.Errorf("resource = %+v, want the parent's daemon binding (teardown revokes both)", p.Resource)
	}
	if p.ActingUserID != "user-1" {
		t.Errorf("acting user = %q", p.ActingUserID)
	}
	if !strings.Contains(strings.Join(listNames(t, authority, "user-1"), ","), "forge (exchanged from ") {
		t.Error("the exchanged token's name must point at its parent for audit")
	}
}

func listNames(t *testing.T, authority tokenauthority.Authority, userID string) []string {
	t.Helper()
	infos, err := authority.ListForUser(context.Background(), userID, "")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(infos))
	for _, i := range infos {
		names = append(names, i.Name)
	}
	return names
}

func TestExchangeToken_NeverOutlivesItsParent(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := NewTokenService(authority, TokenControlPlane{Issuer: testControlPlane})
	soon := time.Now().Add(10 * time.Minute)
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon, ExpiresAt: &soon})

	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if err != nil {
		t.Fatal(err)
	}
	p, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if p.ExpiresAt == nil || p.ExpiresAt.After(soon) {
		t.Fatalf("exchanged expiry %v outlives the parent's %v", p.ExpiresAt, soon)
	}
}

func TestExchangeToken_EphemeralParentMakesAnEphemeralChild(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := NewTokenService(authority, TokenControlPlane{Issuer: testControlPlane})
	end := time.Now().Add(2 * time.Hour)
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{
		Scopes: fullDaemon, Ephemeral: true, ExpiresAt: &end,
		Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "desktop-run"},
	})
	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := authority.Introspect(context.Background(), resp.Msg.GetToken()); err != nil || !p.Ephemeral {
		t.Fatalf("child of an ephemeral session must die with it: %+v %v", p, err)
	}
}

func TestExchangeToken_Audience(t *testing.T) {
	for _, tc := range []struct {
		issuer, audience string
		ok               bool
	}{
		{"https://admin.example.com", "https://ADMIN.example.com:443/", true},
		{"https://admin.example.com/", "https://admin.example.com", true},
		{"https://admin.example.com", "https://api.example.com", false},
		{"https://admin.example.com", "http://admin.example.com", false},
		{"https://admin.example.com", "", false},
		// One dev server, two spellings.
		{"http://localhost:8090", "http://127.0.0.1:8090", true},
		{"http://127.0.0.1:8090", "http://[::1]:8090", true},
		{"http://localhost:8090", "http://127.0.0.1:9999", false},
		{"http://localhost:8090", "http://example.com:8090", false},
	} {
		authority := tokenauthority.NewMemory()
		svc := NewTokenService(authority, TokenControlPlane{Issuer: tc.issuer})
		bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
		_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: tc.audience}))
		if tc.ok && err != nil {
			t.Errorf("issuer %s, audience %q: %v", tc.issuer, tc.audience, err)
		}
		if !tc.ok {
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Errorf("issuer %s, audience %q: err = %v, want FailedPrecondition", tc.issuer, tc.audience, err)
			}
			if n := len(listNames(t, authority, "user-1")); n != 1 {
				t.Errorf("issuer %s, audience %q: a refused audience minted (%d tokens)", tc.issuer, tc.audience, n)
			}
		}
	}
}

func TestExchangeToken_SelfHostedIssuesNothing(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := NewTokenService(authority, TokenControlPlane{})
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no control plane") {
		t.Fatalf("err = %v, want FailedPrecondition naming the missing control plane", err)
	}
}

func TestExchangeToken_ScopeRequests(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := NewTokenService(authority, TokenControlPlane{Issuer: testControlPlane})
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})

	for _, tc := range []struct {
		scopes []string
		code   connect.Code
		want   []string
	}{
		// Non-exchangeable asks are dropped, not granted and not fatal.
		{scopes: []string{"secret:read", "token:write", "daemon:connect"}, want: []string{"secret:read"}},
		{scopes: []string{"token:write"}, code: connect.CodeInvalidArgument},
		{scopes: []string{"deploy:wrIte"}, code: connect.CodeInvalidArgument},
	} {
		resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
			Audience: testControlPlane, Scopes: tc.scopes,
		}))
		if tc.code != 0 {
			if connect.CodeOf(err) != tc.code {
				t.Errorf("scopes %v: err = %v, want %v", tc.scopes, err, tc.code)
			}
			continue
		}
		if err != nil {
			t.Errorf("scopes %v: %v", tc.scopes, err)
			continue
		}
		if strings.Join(resp.Msg.GetScopes(), ",") != strings.Join(tc.want, ",") {
			t.Errorf("scopes %v: granted %v, want %v", tc.scopes, resp.Msg.GetScopes(), tc.want)
		}
	}
}

func TestExchangeToken_RefusesNonSessionCallers(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := NewTokenService(authority, TokenControlPlane{Issuer: testControlPlane})
	ctx := context.Background()

	// An org automation token acts as no one.
	orgToken, err := authority.MintForUser(ctx, tokenauthority.MintRequest{UserID: "user-1", Name: "ci", Scopes: []fat.Scope{fat.ScopeDeployWrite}})
	if err != nil {
		t.Fatal(err)
	}
	// It does act as user-1 in Memory (acting user = minting user), but holds
	// no session scope: it is a deploy token, not a Reliant session.
	_, err = svc.ExchangeToken(ctx, exchangeReq(orgToken.Plaintext, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a non-session token: err = %v, want PermissionDenied", err)
	}

	// No bearer at all, or a JWT-shaped one.
	for _, header := range []string{"", "Bearer eyJhbGciOi.not.an-rlat"} {
		r := connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: testControlPlane})
		if header != "" {
			r.Header().Set("Authorization", header)
		}
		if _, err := svc.ExchangeToken(ctx, r); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("header %q: err = %v, want Unauthenticated", header, err)
		}
	}

	// A revoked session.
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon, Name: "to-revoke"})
	p, _ := authority.Introspect(ctx, bearer)
	if err := authority.RevokeForUser(ctx, "user-1", p.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExchangeToken(ctx, exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("revoked: err = %v, want Unauthenticated", err)
	}
}

// orgShiftAuthority mints in a different org than the parent's, as control-plane
// would after the user's primary org changed.
type orgShiftAuthority struct {
	*tokenauthority.Memory
	parentToken string
}

func (a orgShiftAuthority) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	p, err := a.Memory.Introspect(ctx, token)
	if err == nil && token != a.parentToken {
		shifted := *p
		shifted.OrgID = "another-org"
		return &shifted, nil
	}
	return p, err
}

func TestExchangeToken_NeverCrossesOrgs(t *testing.T) {
	mem := tokenauthority.NewMemory()
	bearer := mintSession(t, mem, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	svc := NewTokenService(orgShiftAuthority{Memory: mem, parentToken: bearer}, TokenControlPlane{Issuer: testControlPlane})

	_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want FailedPrecondition", err)
	}
	// The mis-scoped token was revoked, leaving only the parent live.
	if names := listNames(t, mem, "user-1"); len(names) != 1 {
		t.Fatalf("live tokens = %v, want the parent alone", names)
	}
}

func TestCreateToken_DaemonCarriesTheClippedCeilingOnlyWhereClipped(t *testing.T) {
	for _, clips := range []bool{true, false} {
		authority := tokenauthority.NewMemory()
		svc := NewTokenService(authority, TokenControlPlane{Issuer: testControlPlane, ClipsGrants: clips})
		resp, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
			Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
		}))
		if err != nil {
			t.Fatal(err)
		}
		p, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
		if err != nil {
			t.Fatal(err)
		}
		if p.Scopes.Has(fat.ScopeReliantAPI) {
			t.Errorf("clips=%v: a daemon credential must not become an API credential", clips)
		}
		if got := p.Scopes.Has(fat.ScopeDeployWrite); got != clips {
			t.Errorf("clips=%v: deploy:write present = %v — the ceiling is applied only where control-plane clips it", clips, got)
		}
		if strings.Join(resp.Msg.GetInfo().GetScopes(), ",") != p.Scopes.String() {
			t.Errorf("clips=%v: response scopes %v != stored %s", clips, resp.Msg.GetInfo().GetScopes(), p.Scopes)
		}
		if resp.Msg.GetInfo().GetKind() != reliantv1.TokenKind_TOKEN_KIND_DAEMON {
			t.Errorf("clips=%v: kind = %v", clips, resp.Msg.GetInfo().GetKind())
		}
	}
}

func TestKindForScopes_DaemonWinsWhereverItSits(t *testing.T) {
	for _, scopes := range [][]string{
		{"daemon:connect", "reliant:api"},
		{"reliant:api", "daemon:connect"},
		{"deploy:read", "reliant:api", "daemon:connect"},
	} {
		if got := kindForScopes(scopes); got != reliantv1.TokenKind_TOKEN_KIND_DAEMON {
			t.Errorf("kindForScopes(%v) = %v, want DAEMON", scopes, got)
		}
	}
	if got := kindForScopes([]string{"deploy:read", "reliant:api"}); got != reliantv1.TokenKind_TOKEN_KIND_API {
		t.Errorf("an API token = %v", got)
	}
	if got := kindForScopes([]string{"deploy:read", "deploy:write"}); got != reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED {
		t.Errorf("an exchanged deploy token is no session kind; got %v", got)
	}
}
