// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/stretchr/testify/require"
)

const canary = "CANARY-tok-9f3a7c21-do-not-leak"

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A canary credential never appears in logs, errors, formatted values or the
// scrubbed output of a call, including when the provider echoes the request.
func TestCanaryNeverLeaks(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "svc", Name: "lin", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": canary, "header": "x-api-key"},
	})
	require.NoError(t, err)
	run := e.newRun("alice")

	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)

	// The credential reaches the wire (only ever over https)...
	var seen string
	echo := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-API-Key")
		// ...and a hostile provider echoes it back in an error body.
		http.Error(w, "bad request; you sent X-API-Key: "+seen, http.StatusBadRequest)
	}))
	defer echo.Close()
	req, _ := http.NewRequest(http.MethodGet, echo.URL, nil)
	require.NoError(t, got.Apply(req))
	resp, err := echo.Client().Do(req)
	require.NoError(t, err)
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	require.Equal(t, canary, seen, "sanity: the header carried the credential")
	require.Contains(t, body.String(), canary, "sanity: the provider echoed it")

	// ...but everything that would enter history is scrubbed first.
	scrubbed := got.Redactor.Scrub(body.String())
	require.NotContains(t, scrubbed, canary)
	require.Contains(t, scrubbed, vault.Redacted)
	callErr := got.Redactor.ScrubError(errors.New("upstream said: " + body.String()))
	require.NotContains(t, callErr.Error(), canary)

	// Formatting a Resolved or a Secret by any path cannot reveal it.
	tok, err := e.tokens.Token(ctx, "alice", conn.ID)
	require.NoError(t, err)
	for _, v := range []any{tok, got, *got} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			require.NotContains(t, fmt.Sprintf(f, v), canary, f)
		}
	}
	slog.Info("resolved", "secret", tok, "resolved", got.ConnectionID)

	// The service surface and stored metadata never carry it.
	list, err := e.svc.List(ctx, "alice", "")
	require.NoError(t, err)
	require.NotContains(t, fmt.Sprintf("%+v", list), canary)
	require.Zero(t, e.count(`SELECT count(*) FROM connection_events WHERE actor LIKE '%CANARY%'`))
	require.Zero(t, e.count(`SELECT count(*) FROM connections WHERE name LIKE '%CANARY%' OR status_reason LIKE '%CANARY%' OR account_label LIKE '%CANARY%'`))

	require.NotContains(t, logs.String(), canary, "the canary must not appear in captured logs")
}

func TestCanaryNeverLeaksFromOAuthPaths(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	ctx := context.Background()
	e.gh.nextAccess, e.gh.nextRefresh = "ghu_"+canary, "ghr_"+canary
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	_, err := e.tokens.Token(ctx, "alice", conn.ID) // refresh path with canary tokens
	require.NoError(t, err)

	state := startFlow(t, e, connections.StartParams{})
	_, err = e.broker.Complete(ctx, connections.CompleteParams{State: state, Code: "AUTHCODE-" + canary, UserID: "alice"})
	require.NoError(t, err)

	require.NotContains(t, logs.String(), canary)
	require.NotContains(t, logs.String(), state, "the raw state is never logged")
	require.NotContains(t, logs.String(), clientSecret)
}

func TestRedactorScrubsBasicAuthEncodings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "svc", Name: "j", Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"username": "me@example.com", "password": canary},
	})
	require.NoError(t, err)
	run := e.newRun("alice")
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, "https://jira.example/x", nil)
	require.NoError(t, got.Apply(req))
	user, pass, ok := req.BasicAuth()
	require.True(t, ok)
	require.Equal(t, "me@example.com", user)
	require.Equal(t, canary, pass)

	echoed := "Authorization: " + req.Header.Get("Authorization")
	require.NotContains(t, got.Redactor.Scrub(echoed), strings.TrimPrefix(req.Header.Get("Authorization"), "Basic "),
		"the base64 form of a basic credential is scrubbed too")
}

// A failed code exchange is the path most likely to log something it should
// not: the provider echoes the code back, and the code is a one-time credential.
func TestCanaryNeverLeaksFromFailedExchange(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	e.gh.exchangeFail = true
	state := startFlow(t, e, connections.StartParams{})
	_, err := e.broker.Complete(context.Background(), connections.CompleteParams{State: state, Code: "AUTHCODE-" + canary, UserID: "alice"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.NotContains(t, err.Error(), canary)
	require.NotContains(t, err.Error(), "incorrect", "the provider's error description is not surfaced")
	require.NotContains(t, logs.String(), canary)
	require.NotContains(t, logs.String(), "incorrect")
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}
