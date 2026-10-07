// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// doerFunc is an HTTPDoer from a function: the provider is never called.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// "Only from: Me" on GitHub is the connection's sender_id, which the REAL
// catalog manifest's identity probe reads from GET /user: the numeric user
// id, never the login. A login can be renamed and then registered by someone
// else; an allowlist written from it would admit them.
func TestGitHubProbeRecordsTheNumericUserIDAsTheSender(t *testing.T) {
	reg, err := ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), func(string) string { return "" })
	require.NoError(t, err)
	prov, ok := reg.Get("github")
	require.True(t, ok)

	var asked string
	gitHub := doerFunc(func(r *http.Request) (*http.Response, error) {
		asked = r.Method + " " + r.URL.String()
		body := `{"login":"OctoCat","id":583231,"type":"User","name":"The Octocat"}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	who, err := prov.identify(context.Background(), gitHub, nil, func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer ghu_test")
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "GET https://api.github.com/user", asked)
	assert.Equal(t, "583231", who.SenderID, "Me is the user id")
	assert.Equal(t, "OctoCat", who.AccountLabel, "the login is only for display")
	assert.Equal(t, "583231", who.ExternalAccountID)
}

// The token response an oauth2 sender_id is evaluated over has every token
// removed, at any depth: the expression only needs to name a person.
func TestWithoutTokensDropsEveryTokenField(t *testing.T) {
	raw := map[string]any{
		"ok": true, "access_token": "xoxb-bot", "refresh_token": "xoxe-1", "token_type": "bot", "id_token": "eyJ",
		"team": map[string]any{"id": "T0ACME"},
		"authed_user": map[string]any{
			"id": "U0HUMAN", "scope": "search:read", "access_token": "xoxp-user", "refresh_token": "xoxe-user",
		},
	}
	assert.Equal(t, map[string]any{
		"ok":          true,
		"team":        map[string]any{"id": "T0ACME"},
		"authed_user": map[string]any{"id": "U0HUMAN", "scope": "search:read"},
	}, withoutTokens(raw))
	assert.Equal(t, "xoxp-user", raw["authed_user"].(map[string]any)["access_token"], "the original is not modified")
}
