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

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

const secretBotToken = "ghs_BOTSECRET123"

type fakeBotTokens struct {
	tok   gitcredentialclient.InstallationToken
	err   error
	calls int
	got   gitcredentialclient.InstallationRequest
}

func (f *fakeBotTokens) MintInstallationToken(_ context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error) {
	f.calls++
	f.got = req
	return f.tok, f.err
}

func mintedBotToken() gitcredentialclient.InstallationToken {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	return gitcredentialclient.InstallationToken{
		Token: gitcredentialclient.NewToken(secretBotToken, &exp), ExpiresAt: exp,
		BotLogin: "reliant-labs[bot]", ActingForLogin: "octocat", Repository: "acme/api",
	}
}

func botReq(bearer, owner, repo string, perms map[string]string) *connect.Request[reliantv1.GetGitHubBotTokenRequest] {
	r := connect.NewRequest(&reliantv1.GetGitHubBotTokenRequest{Owner: owner, Repo: repo, Permissions: perms})
	if bearer != "" {
		r.Header().Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func botSvc(t *testing.T, src *fakeBotTokens) (*Service, *tokenauthority.Memory) {
	authority := tokenauthority.NewMemory()
	deps := Deps{Authority: authority}
	if src != nil { // never a typed-nil GitBotTokens
		deps.GitBotTokens = src
	}
	return NewTestToken(t, WithTokenDeps(deps)), authority
}

func daemonBearer(t *testing.T, authority *tokenauthority.Memory) string {
	return mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect}})
}

func TestGetGitHubBotToken_MintsForTheCallerAndOneRepository(t *testing.T) {
	src := &fakeBotTokens{tok: mintedBotToken()}
	svc, authority := botSvc(t, src)

	resp, err := svc.GetGitHubBotToken(context.Background(), botReq(daemonBearer(t, authority), " acme ", "api",
		map[string]string{"Pull_Requests": "WRITE"}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if m.GetToken() != secretBotToken || m.GetExpiresAt() != "2030-01-02T03:04:05Z" ||
		m.GetBotLogin() != "reliant-labs[bot]" || m.GetActingForLogin() != "octocat" || m.GetRepository() != "acme/api" {
		t.Errorf("response = %+v", m)
	}
	// The acting user comes from the session, never the request; the
	// permissions are normalized and passed through, never widened.
	if src.got.ExternalUserID != "user-1" || src.got.Owner != "acme" || src.got.Repo != "api" ||
		len(src.got.Permissions) != 1 || src.got.Permissions["pull_requests"] != "write" {
		t.Errorf("minted with %+v", src.got)
	}
}

func TestGetGitHubBotToken_RejectsNonDaemonCredentials(t *testing.T) {
	src := &fakeBotTokens{tok: mintedBotToken()}
	svc, authority := botSvc(t, src)
	api := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeReliantAPI}})
	_, err := svc.GetGitHubBotToken(context.Background(), botReq(api, "acme", "api", nil))
	wantCode(t, err, connect.CodePermissionDenied, "")

	orgSvc := NewTestToken(t, WithTokenDeps(Deps{Authority: actsAsNoOne{authority}, GitBotTokens: src}))
	_, err = orgSvc.GetGitHubBotToken(context.Background(), botReq(api, "acme", "api", nil))
	wantCode(t, err, connect.CodePermissionDenied, "")

	for _, bearer := range []string{"", "not-a-token"} {
		_, err = svc.GetGitHubBotToken(context.Background(), botReq(bearer, "acme", "api", nil))
		wantCode(t, err, connect.CodeUnauthenticated, "")
	}
	if src.calls != 0 {
		t.Errorf("minted %d tokens for refused callers", src.calls)
	}
}

func TestGetGitHubBotToken_InvalidRequestsMintNothing(t *testing.T) {
	cases := map[string]*connect.Request[reliantv1.GetGitHubBotTokenRequest]{
		"no owner":         botReq("", "", "api", nil),
		"no repo":          botReq("", "acme", "", nil),
		"path in repo":     botReq("", "acme", "api/../x", nil),
		"dot repo":         botReq("", "acme", "..", nil),
		"unknown perm":     botReq("", "acme", "api", map[string]string{"administration": "write"}),
		"admin level":      botReq("", "acme", "api", map[string]string{"contents": "admin"}),
		"workflows (wide)": botReq("", "acme", "api", map[string]string{"workflows": "write"}),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			src := &fakeBotTokens{tok: mintedBotToken()}
			svc, authority := botSvc(t, src)
			req.Header().Set("Authorization", "Bearer "+daemonBearer(t, authority))
			_, err := svc.GetGitHubBotToken(context.Background(), req)
			wantCode(t, err, connect.CodeInvalidArgument, "")
			if src.calls != 0 {
				t.Error("minted a token for an invalid request")
			}
		})
	}
}

func TestGetGitHubBotToken_ErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		src    *fakeBotTokens
		code   connect.Code
		reason string
	}{
		{"owner cannot push", &fakeBotTokens{err: gitcredentialclient.ErrOwnerCannotPush}, connect.CodePermissionDenied, "github_owner_cannot_push"},
		{"not installed", &fakeBotTokens{err: gitcredentialclient.ErrAppNotInstalled}, connect.CodeFailedPrecondition, "github_app_not_installed"},
		{"permissions", &fakeBotTokens{err: gitcredentialclient.ErrAppPermissions}, connect.CodeFailedPrecondition, "github_app_permissions"},
		{"no app key", &fakeBotTokens{err: gitcredentialclient.ErrAppNotConfigured}, connect.CodeFailedPrecondition, "github_app_not_configured"},
		{"not connected", &fakeBotTokens{err: gitcredentialclient.ErrNotConnected}, connect.CodeFailedPrecondition, "git_credential_not_connected"},
		{"needs reconnect", &fakeBotTokens{err: gitcredentialclient.ErrNeedsReconnect}, connect.CodeFailedPrecondition, "git_credential_needs_reconnect"},
		{"transient", &fakeBotTokens{err: errors.New("dial tcp: boom " + secretBotToken)}, connect.CodeUnavailable, ""},
		{"empty token", &fakeBotTokens{}, connect.CodeUnavailable, ""},
		{"self-hosted", nil, connect.CodeFailedPrecondition, "git_credential_no_control_plane"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, authority := botSvc(t, c.src)
			_, err := svc.GetGitHubBotToken(context.Background(), botReq(daemonBearer(t, authority), "acme", "api", nil))
			wantCode(t, err, c.code, c.reason)
			if strings.Contains(err.Error(), secretBotToken) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}

func TestGetGitHubBotToken_NeverLogsToken(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, src := range []*fakeBotTokens{
		{tok: mintedBotToken()},
		{err: errors.New("upstream said " + secretBotToken)},
		{err: gitcredentialclient.ErrOwnerCannotPush},
	} {
		svc, authority := botSvc(t, src)
		bearer := daemonBearer(t, authority)
		_, _ = svc.GetGitHubBotToken(context.Background(), botReq(bearer, "acme", "api", nil))
		if strings.Contains(buf.String(), bearer) {
			t.Error("logs contain the caller's bearer")
		}
	}
	if buf.Len() == 0 {
		t.Fatal("expected log output")
	}
	if strings.Contains(buf.String(), secretBotToken) {
		t.Errorf("logs contain the bot token:\n%s", buf.String())
	}
}
