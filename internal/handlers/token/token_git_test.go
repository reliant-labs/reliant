// Copyright (c) 2025 Reliant Labs
package token

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

const secretGitToken = "ghu_SUPERSECRET123"

type fakeGitTokens struct {
	tok      gitcredentialclient.Token
	err      error
	gotUser  string
	gotProvr string
}

func (f *fakeGitTokens) UserAccessToken(_ context.Context, user, provider string) (gitcredentialclient.Token, error) {
	f.gotUser, f.gotProvr = user, provider
	return f.tok, f.err
}

// actsAsNoOne introspects every token as a daemon credential with no acting
// user, as an organization token would be.
type actsAsNoOne struct{ tokenauthority.Authority }

func (a actsAsNoOne) Introspect(ctx context.Context, tok string) (*fat.Principal, error) {
	p, err := a.Authority.Introspect(ctx, tok)
	if err != nil {
		return nil, err
	}
	cp := *p
	cp.ActingUserID = ""
	cp.Scopes = fat.SetOf(fat.ScopeDaemonConnect)
	return &cp, nil
}

func gitReq(provider string) *reliantv1.GetGitTokenRequest {
	return &reliantv1.GetGitTokenRequest{Provider: provider}
}

func gitSvc(t *testing.T, src *fakeGitTokens) (*Service, *tokenauthority.Memory) {
	authority := tokenauthority.NewMemory()
	deps := Deps{Authority: authority}
	if src != nil { // never a typed-nil GitTokens
		deps.GitTokens = src
	}
	return NewTestToken(t, WithTokenDeps(deps)), authority
}

func wantCode(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	// What the svcerr interceptor puts on the wire: the code and the reason
	// header, whether the handler returned a svcerr kind or a *connect.Error.
	ce := svcerr.ToConnectV2(err)
	if ce == nil || ce.Code() != code {
		t.Fatalf("err = %v, want code %v", err, code)
	}
	if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != reason {
		t.Errorf("reason = %q, want %q", got, reason)
	}
}

func TestGetGitToken_DaemonOK(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	src := &fakeGitTokens{tok: gitcredentialclient.NewToken(secretGitToken, &exp)}
	svc, authority := gitSvc(t, src)
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})

	resp, err := tdd.CallV2(context.Background(), svc.GetGitToken, gitReq(""), bearerInfo(bearer))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAccessToken() != secretGitToken || resp.GetExpiresAt() != "2030-01-02T03:04:05Z" {
		t.Errorf("response = %+v", resp)
	}
	if src.gotUser != "user-1" || src.gotProvr != "github" {
		t.Errorf("asked (%q, %q), want (user-1, github)", src.gotUser, src.gotProvr)
	}
}

func TestGetGitToken_NonExpiringHasEmptyExpiry(t *testing.T) {
	svc, authority := gitSvc(t, &fakeGitTokens{tok: gitcredentialclient.NewToken(secretGitToken, nil)})
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
	resp, err := tdd.CallV2(context.Background(), svc.GetGitToken, gitReq("GitHub"), bearerInfo(bearer))
	if err != nil || resp.GetExpiresAt() != "" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestGetGitToken_RejectsNonDaemonCredentials(t *testing.T) {
	svc, authority := gitSvc(t, &fakeGitTokens{tok: gitcredentialclient.NewToken(secretGitToken, nil)})
	api := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeReliantAPI}})
	_, err := tdd.CallV2(context.Background(), svc.GetGitToken, gitReq(""), bearerInfo(api))
	wantCode(t, err, connect.CodePermissionDenied, "")

	orgSvc := NewTestToken(t, WithTokenDeps(Deps{Authority: actsAsNoOne{authority}, GitTokens: &fakeGitTokens{}}))
	_, err = tdd.CallV2(context.Background(), orgSvc.GetGitToken, gitReq(""), bearerInfo(api))
	wantCode(t, err, connect.CodePermissionDenied, "")

	for _, bearer := range []string{"", "not-a-token"} {
		_, err = tdd.CallV2(context.Background(), svc.GetGitToken, gitReq(""), bearerInfo(bearer))
		wantCode(t, err, connect.CodeUnauthenticated, "")
	}
}

func TestGetGitToken_RevokedIsUnauthenticated(t *testing.T) {
	svc, authority := gitSvc(t, &fakeGitTokens{tok: gitcredentialclient.NewToken(secretGitToken, nil)})
	minted, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: "user-1", Name: "d", Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.RevokeForUser(context.Background(), "user-1", minted.TokenID); err != nil {
		t.Fatal(err)
	}
	_, err = tdd.CallV2(context.Background(), svc.GetGitToken, gitReq(""), bearerInfo(minted.Plaintext))
	wantCode(t, err, connect.CodeUnauthenticated, "")
}

func TestGetGitToken_ErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		src    *fakeGitTokens
		code   connect.Code
		reason string
	}{
		{"not connected", &fakeGitTokens{err: gitcredentialclient.ErrNotConnected}, connect.CodeFailedPrecondition, "git_credential_not_connected"},
		{"needs reconnect", &fakeGitTokens{err: gitcredentialclient.ErrNeedsReconnect}, connect.CodeFailedPrecondition, "git_credential_needs_reconnect"},
		{"transient", &fakeGitTokens{err: errors.New("dial tcp: boom " + secretGitToken)}, connect.CodeUnavailable, ""},
		{"self-hosted", nil, connect.CodeFailedPrecondition, "git_credential_no_control_plane"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, authority := gitSvc(t, c.src)
			bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
			_, err := tdd.CallV2(context.Background(), svc.GetGitToken, gitReq(""), bearerInfo(bearer))
			wantCode(t, err, c.code, c.reason)
			if strings.Contains(err.Error(), secretGitToken) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}

func TestGetGitToken_NeverLogsToken(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, src := range []*fakeGitTokens{
		{tok: gitcredentialclient.NewToken(secretGitToken, nil)},
		{err: errors.New("upstream said " + secretGitToken)},
		{err: gitcredentialclient.ErrNeedsReconnect},
	} {
		svc, authority := gitSvc(t, src)
		bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
		_, _ = tdd.CallV2(context.Background(), svc.GetGitToken, gitReq(""), bearerInfo(bearer))
		if strings.Contains(buf.String(), bearer) {
			t.Error("logs contain the caller's bearer")
		}
	}
	if buf.Len() == 0 {
		t.Fatal("expected log output")
	}
	if strings.Contains(buf.String(), secretGitToken) {
		t.Errorf("logs contain the git token:\n%s", buf.String())
	}
}
