// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/forgecred"
)

// fakeBotTokenService answers GetGitHubBotToken per bearer: a refusal named in
// refusals, else a token. It records which bearers asked.
type fakeBotTokenService struct {
	reliantv1connect.UnimplementedTokenServiceHandler
	refusals map[string]error
	asked    []string
}

func (f *fakeBotTokenService) GetGitHubBotToken(_ context.Context, r *connect.Request[reliantv1.GetGitHubBotTokenRequest]) (*connect.Response[reliantv1.GetGitHubBotTokenResponse], error) {
	bearer := strings.TrimPrefix(r.Header().Get("Authorization"), "Bearer ")
	f.asked = append(f.asked, bearer)
	if err := f.refusals[bearer]; err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.GetGitHubBotTokenResponse{
		Token: ghBotTestToken, BotLogin: "reliant-labs[bot]", ActingForLogin: bearer, Repository: "acme/app",
	}), nil
}

// serveBotTokens runs fake behind forge's svcerr interceptor, the one that
// puts a refusal's reason on the wire, as the api-server does.
func serveBotTokens(t *testing.T, fake *fakeBotTokenService) string {
	t.Helper()
	mux := http.NewServeMux()
	path, h := reliantv1connect.NewTokenServiceHandler(fake, connect.WithInterceptors(svcerr.Interceptor()))
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func daemonSessions(server string, tokens ...string) []forgecred.Session {
	out := make([]forgecred.Session, 0, len(tokens))
	for _, tok := range tokens {
		out = append(out, forgecred.Session{Server: server, Token: tok, Kind: sessionKindDaemon})
	}
	return out
}

var botTokenRequest = githubTokenRequest{As: githubAsApp, Owner: "acme", Repo: "app"}

// A machine holds the daemon sessions of every account ever signed in on it.
// One whose account has no GitHub connection must not end the search: the
// next session's account may be connected.
func TestFetchBotToken_AnAccountWithoutGitHubTriesTheNextSession(t *testing.T) {
	fake := &fakeBotTokenService{refusals: map[string]error{
		"stale":       svcerr.WithReason(svcerr.FailedPrecondition("connect your GitHub account in Reliant first"), "git_credential_not_connected"),
		"reconnect":   svcerr.WithReason(svcerr.FailedPrecondition("your GitHub connection expired; reconnect it in Reliant"), "git_credential_needs_reconnect"),
		"unconnected": svcerr.WithReason(svcerr.FailedPrecondition("connect your GitHub account in Reliant first"), "git_credential_not_connected"),
	}}
	server := serveBotTokens(t, fake)

	tok, err := fetchBotTokenFromSessions(context.Background(), daemonSessions(server, "stale", "reconnect", "connected"), botTokenRequest)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if tok.ActingForLogin != "connected" {
		t.Fatalf("token acts for %q, want the connected account", tok.ActingForLogin)
	}

	// With no connected account at all, the refusal says what to do.
	fake.asked = nil
	_, err = fetchBotTokenFromSessions(context.Background(), daemonSessions(server, "stale", "unconnected"), botTokenRequest)
	if err == nil || !strings.Contains(err.Error(), "connect your GitHub account") {
		t.Fatalf("err = %v, want the not-connected refusal", err)
	}
	if len(fake.asked) != 2 {
		t.Fatalf("asked %v, want both sessions", fake.asked)
	}
}

// A refusal of the repository is the same from every session, so it is
// reported at once rather than asked of every other account.
func TestFetchBotToken_ARepositoryRefusalStopsTheSearch(t *testing.T) {
	fake := &fakeBotTokenService{refusals: map[string]error{
		"first": svcerr.WithReason(svcerr.FailedPrecondition("the Reliant GitHub App is not installed on acme/app"), "github_app_not_installed"),
	}}
	server := serveBotTokens(t, fake)

	_, err := fetchBotTokenFromSessions(context.Background(), daemonSessions(server, "first", "second"), botTokenRequest)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want the not-installed refusal", err)
	}
	if len(fake.asked) != 1 {
		t.Fatalf("asked %v, want only the first session", fake.asked)
	}
}
