// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/stretchr/testify/require"
)

func TestAPIKey_HeadersAreAllowListed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := map[string]struct{ header, want string }{
		"":             {"Authorization", "Bearer k1"},
		"bearer":       {"Authorization", "Bearer k1"},
		"x-api-key":    {"X-Api-Key", "k1"},
		"api-key":      {"Api-Key", "k1"},
		"x-auth-token": {"X-Auth-Token", "k1"},
	}
	run := e.newRun("alice")
	i := 0
	for choice, tc := range cases {
		i++
		fields := map[string]string{"api_key": "k1"}
		if choice != "" {
			fields["header"] = choice
		}
		conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
			UserID: "alice", IntegrationID: "svc", Name: "n" + string(rune('a'+i)), Kind: connections.APIKeyKindAPIKey, Fields: fields,
		})
		require.NoError(t, err, choice)
		got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
		require.NoError(t, err)
		req, _ := http.NewRequest(http.MethodGet, "https://x.example", nil)
		require.NoError(t, got.Apply(req))
		require.Equal(t, tc.want, req.Header.Get(tc.header), "header choice %q", choice)
	}
}

func TestAPIKey_RejectsDisallowedHeaderAndBadInput(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	base := connections.CreateAPIKeyParams{UserID: "alice", IntegrationID: "svc", Name: "n", Kind: connections.APIKeyKindAPIKey}
	for name, mut := range map[string]func(*connections.CreateAPIKeyParams){
		"cookie header": func(p *connections.CreateAPIKeyParams) {
			p.Fields = map[string]string{"api_key": "k", "header": "Cookie"}
		},
		"host header": func(p *connections.CreateAPIKeyParams) {
			p.Fields = map[string]string{"api_key": "k", "header": "Host"}
		},
		"missing key": func(p *connections.CreateAPIKeyParams) { p.Fields = map[string]string{} },
		"empty name":  func(p *connections.CreateAPIKeyParams) { p.Fields = map[string]string{"api_key": "k"}; p.Name = "  " },
		"bad integration id": func(p *connections.CreateAPIKeyParams) {
			p.Fields = map[string]string{"api_key": "k"}
			p.IntegrationID = "Has Space"
		},
		"integration without basic": func(p *connections.CreateAPIKeyParams) {
			p.Kind = connections.APIKeyKindBasic
			p.Fields = map[string]string{"username": "u", "password": "p"}
			p.IntegrationID = "github"
		},
		"unknown integration": func(p *connections.CreateAPIKeyParams) {
			p.Fields = map[string]string{"api_key": "k"}
			p.IntegrationID = "nope"
		},
		"header choice on a declared placement": func(p *connections.CreateAPIKeyParams) {
			p.Fields = map[string]string{"api_key": "k", "header": "x-api-key"}
			p.IntegrationID = "github"
		},
		"unspecified kind": func(p *connections.CreateAPIKeyParams) { p.Fields = map[string]string{"api_key": "k"}; p.Kind = 0 },
		"basic missing pass": func(p *connections.CreateAPIKeyParams) {
			p.Kind = connections.APIKeyKindBasic
			p.Fields = map[string]string{"username": "u"}
		},
		"basic colon in user": func(p *connections.CreateAPIKeyParams) {
			p.Kind = connections.APIKeyKindBasic
			p.Fields = map[string]string{"username": "a:b", "password": "p"}
		},
	} {
		p := base
		mut(&p)
		_, err := e.svc.CreateAPIKey(ctx, p)
		require.ErrorIs(t, err, connections.ErrInvalidArgument, name)
	}
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}

func TestAPIKey_DuplicateNameIsAlreadyExists(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := connections.CreateAPIKeyParams{UserID: "alice", IntegrationID: "svc", Name: "dup", Kind: connections.APIKeyKindAPIKey, Fields: map[string]string{"api_key": "k"}}
	_, err := e.svc.CreateAPIKey(ctx, p)
	require.NoError(t, err)
	_, err = e.svc.CreateAPIKey(ctx, p)
	require.ErrorIs(t, err, connections.ErrAlreadyExists)
	p.UserID = "bob"
	_, err = e.svc.CreateAPIKey(ctx, p)
	require.NoError(t, err, "names are per user")
}

func TestAPIKey_BasicRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "svc", Name: "j", Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"username": "me", "password": "p@ss:w0rd"},
	})
	require.NoError(t, err)
	require.Equal(t, "basic", conn.AuthKind)
	run := e.newRun("alice")
	got, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: run, Placement: connections.PlacementServer}, connections.Ref{ConnectionID: conn.ID})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://x.example", nil)
	require.NoError(t, got.Apply(req))
	u, p, ok := req.BasicAuth()
	require.True(t, ok)
	require.Equal(t, "me", u)
	require.Equal(t, "p@ss:w0rd", p)
}

func TestAPIKey_TestConnectionOpensSecret(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn, err := e.svc.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "svc", Name: "n", Kind: connections.APIKeyKindAPIKey, Fields: map[string]string{"api_key": "k"},
	})
	require.NoError(t, err)
	r, err := e.svc.Test(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.True(t, r.OK)
	require.False(t, r.Probed, "an integration without a probe only proves the stored secret opens")
}

func TestTestConnection_GitHubProbeRecordsAccountLabel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	_, err := e.raw.Exec(`UPDATE connections SET account_label = NULL WHERE id=$1`, conn.ID)
	require.NoError(t, err)
	e.gh.login = "renamed-octocat"

	r, err := e.svc.Test(ctx, "alice", conn.ID)
	require.NoError(t, err)
	require.True(t, r.OK)
	require.True(t, r.Probed)
	require.Equal(t, "renamed-octocat", r.AccountLabel)
	got, _ := e.svc.Get(ctx, "alice", conn.ID)
	require.Equal(t, "renamed-octocat", *got.AccountLabel)
	e.gh.mu.Lock()
	require.Regexp(t, `^Bearer ghu_access_`, e.gh.probeAuth)
	e.gh.mu.Unlock()
}

func TestTestConnection_NeedsReauthReportsClass(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "a")
	e.expireAccessToken(conn.ID)
	e.gh.refreshStatus, e.gh.refreshBody = 200, `{"error":"bad_refresh_token"}`
	r, err := e.svc.Test(context.Background(), "alice", conn.ID)
	require.NoError(t, err)
	require.False(t, r.OK)
	require.Equal(t, "needs_reauth", r.ErrorClass)
}

func TestRenameAndDeleteAndEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	got, err := e.svc.Rename(ctx, "alice", conn.ID, "renamed")
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.NoError(t, e.svc.Delete(ctx, "alice", conn.ID))
	_, err = e.svc.Get(ctx, "alice", conn.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)
	require.ErrorIs(t, e.svc.Delete(ctx, "alice", conn.ID), connections.ErrNotFound)

	var kinds []string
	rows, err := e.raw.Query(`SELECT kind FROM connection_events WHERE connection_id=$1 ORDER BY id`, conn.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		kinds = append(kinds, k)
	}
	require.Equal(t, []string{"created", "renamed", "deleted"}, kinds)

	// A deleted name can be reused.
	e.gh.accountID, e.gh.login = 55, "again"
	again := e.connect("alice", "renamed")
	require.NotEqual(t, conn.ID, again.ID)
}
