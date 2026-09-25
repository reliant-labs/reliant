// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

func newTokenSvc() (*TokenService, *tokenauthority.Memory) {
	authority := tokenauthority.NewMemory()
	return NewTokenService(authority), authority
}

// tokenAuthCtx returns a context carrying an interactive-session identity, as
// the auth interceptor populates after validating a session JWT.
func tokenAuthCtx(userID string) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return context.WithValue(ctx, auth.UserEmailContextKey, userID+"@example.com")
}

// machineAuthCtx is tokenAuthCtx for a caller authenticated by an `rlat_`
// reliant:api token.
func machineAuthCtx(userID string) context.Context {
	return auth.WithMachineToken(tokenAuthCtx(userID), &fat.Principal{
		TokenID: "tok-1", ActingUserID: userID, Scopes: fat.SetOf(fat.ScopeReliantAPI),
	})
}

func req[T any](msg *T) *connect.Request[T] { return connect.NewRequest(msg) }

func TestTokenServiceCreateListRevokeFlow(t *testing.T) {
	svc, authority := newTokenSvc()

	createResp, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "ci", Kind: reliantv1.TokenKind_TOKEN_KIND_API,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	raw := createResp.Msg.GetToken()
	if !auth.IsAccessTokenFormat(raw) {
		t.Errorf("raw token %q is not an rlat_ access token", raw)
	}
	info := createResp.Msg.GetInfo()
	if info.GetId() == "" || info.GetName() != "ci" || info.GetKind() != reliantv1.TokenKind_TOKEN_KIND_API {
		t.Errorf("create response missing metadata: %+v", info)
	}
	if !strings.HasPrefix(raw, info.GetTokenPrefix()) {
		t.Errorf("token prefix %q does not prefix the raw token", info.GetTokenPrefix())
	}

	// The minted token acts as the caller with exactly the kind's scope.
	p, err := authority.Introspect(context.Background(), raw)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if p.ActingUserID != "user-1" || !p.Scopes.Permits(fat.ScopeReliantAPI) || p.Scopes.Permits(fat.ScopeDaemonConnect) {
		t.Errorf("principal = %+v, want user-1 with reliant:api only", p)
	}

	// List returns the token, never the secret.
	listResp, err := svc.ListTokens(tokenAuthCtx("user-1"), req(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(listResp.Msg.GetTokens()) != 1 || listResp.Msg.GetTokens()[0].GetId() != info.GetId() {
		t.Fatalf("list = %+v, want the created token", listResp.Msg.GetTokens())
	}

	// Another user sees nothing and cannot revoke it.
	otherList, err := svc.ListTokens(tokenAuthCtx("user-2"), req(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens(other): %v", err)
	}
	if len(otherList.Msg.GetTokens()) != 0 {
		t.Fatalf("other user sees %d tokens, want 0", len(otherList.Msg.GetTokens()))
	}
	if _, err := svc.RevokeToken(tokenAuthCtx("user-2"), req(&reliantv1.RevokeTokenRequest{Id: info.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("foreign revoke code = %v, want NotFound", connect.CodeOf(err))
	}

	// Owner revoke kills the token at the authority.
	if _, err := svc.RevokeToken(tokenAuthCtx("user-1"), req(&reliantv1.RevokeTokenRequest{Id: info.GetId()})); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if _, err := authority.Introspect(context.Background(), raw); err == nil {
		t.Error("revoked token still introspects as live")
	}
	listResp, err = svc.ListTokens(tokenAuthCtx("user-1"), req(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens after revoke: %v", err)
	}
	if len(listResp.Msg.GetTokens()) != 0 {
		t.Errorf("revoked token still listed: %+v", listResp.Msg.GetTokens())
	}
}

func TestTokenServiceKindsMapToScopesAndFilter(t *testing.T) {
	svc, authority := newTokenSvc()
	ctx := tokenAuthCtx("user-1")

	daemon, err := svc.CreateToken(ctx, req(&reliantv1.CreateTokenRequest{Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON}))
	if err != nil {
		t.Fatalf("create daemon: %v", err)
	}
	if _, err := svc.CreateToken(ctx, req(&reliantv1.CreateTokenRequest{Name: "ci", Kind: reliantv1.TokenKind_TOKEN_KIND_API})); err != nil {
		t.Fatalf("create api: %v", err)
	}
	p, err := authority.Introspect(context.Background(), daemon.Msg.GetToken())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if !p.Scopes.Permits(fat.ScopeDaemonConnect) || p.Scopes.Permits(fat.ScopeReliantAPI) {
		t.Errorf("daemon token scopes = %v, want daemon:connect only", p.Scopes.Strings())
	}

	onlyDaemon, err := svc.ListTokens(ctx, req(&reliantv1.ListTokensRequest{Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON}))
	if err != nil {
		t.Fatalf("ListTokens(DAEMON): %v", err)
	}
	if got := onlyDaemon.Msg.GetTokens(); len(got) != 1 || got[0].GetKind() != reliantv1.TokenKind_TOKEN_KIND_DAEMON || got[0].GetName() != "laptop" {
		t.Errorf("DAEMON list = %+v, want the one daemon token", got)
	}
	all, err := svc.ListTokens(ctx, req(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(all.Msg.GetTokens()) != 2 {
		t.Errorf("unfiltered list has %d tokens, want 2", len(all.Msg.GetTokens()))
	}
}

// Tokens of scopes this surface does not manage (LLM keys, connector
// credentials) are the user's too, but never listed here.
func TestTokenServiceListHidesForeignKinds(t *testing.T) {
	svc, authority := newTokenSvc()
	if _, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: "user-1", Name: "llm", Scopes: []fat.Scope{fat.ScopeLLMInvoke},
	}); err != nil {
		t.Fatalf("MintForUser: %v", err)
	}
	resp, err := svc.ListTokens(tokenAuthCtx("user-1"), req(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(resp.Msg.GetTokens()) != 0 {
		t.Errorf("list = %+v, want the llm:invoke key hidden", resp.Msg.GetTokens())
	}
}

// A machine credential can never mint a credential.
func TestTokenServiceCreateRejectsMachineCaller(t *testing.T) {
	svc, _ := newTokenSvc()
	_, err := svc.CreateToken(machineAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "x", Kind: reliantv1.TokenKind_TOKEN_KIND_API,
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("machine-authed create code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "interactive session") {
		t.Errorf("error = %v, want an interactive-session message", err)
	}
}

// List and revoke work for a machine caller: headless CI can inspect and
// retire its own tokens without a browser login.
func TestTokenServiceListRevokeAcceptMachineCaller(t *testing.T) {
	svc, _ := newTokenSvc()
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "ci", Kind: reliantv1.TokenKind_TOKEN_KIND_API,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := svc.ListTokens(machineAuthCtx("user-1"), req(&reliantv1.ListTokensRequest{})); err != nil {
		t.Errorf("machine-authed list: %v", err)
	}
	if _, err := svc.RevokeToken(machineAuthCtx("user-1"), req(&reliantv1.RevokeTokenRequest{Id: created.Msg.GetInfo().GetId()})); err != nil {
		t.Errorf("machine-authed revoke: %v", err)
	}
}

func TestTokenServiceCreateValidation(t *testing.T) {
	svc, _ := newTokenSvc()
	ctx := tokenAuthCtx("user-1")
	cases := map[string]*reliantv1.CreateTokenRequest{
		"empty name":   {Name: "", Kind: reliantv1.TokenKind_TOKEN_KIND_API},
		"long name":    {Name: strings.Repeat("n", maxTokenNameLen+1), Kind: reliantv1.TokenKind_TOKEN_KIND_API},
		"negative ttl": {Name: "x", Kind: reliantv1.TokenKind_TOKEN_KIND_API, TtlSeconds: -5},
		"no kind":      {Name: "x"},
	}
	for name, msg := range cases {
		if _, err := svc.CreateToken(ctx, req(msg)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, connect.CodeOf(err))
		}
	}
}

func TestTokenServiceRequiresIdentity(t *testing.T) {
	svc, _ := newTokenSvc()
	anon := context.Background()
	if _, err := svc.CreateToken(anon, req(&reliantv1.CreateTokenRequest{Name: "x", Kind: reliantv1.TokenKind_TOKEN_KIND_API})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("create without identity code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if _, err := svc.ListTokens(anon, req(&reliantv1.ListTokensRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("list without identity code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if _, err := svc.RevokeToken(anon, req(&reliantv1.RevokeTokenRequest{Id: "x"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("revoke without identity code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestTokenServiceRevokeRequiresID(t *testing.T) {
	svc, _ := newTokenSvc()
	if _, err := svc.RevokeToken(tokenAuthCtx("user-1"), req(&reliantv1.RevokeTokenRequest{Id: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty id code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}
