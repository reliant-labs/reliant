// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/stretchr/testify/require"
)

// texterManifest is a pasted-credential integration whose events route by
// account (it declares a trigger), in Twilio's shape: HTTP Basic whose
// username is the account id param, and an identity probe that names the
// account.
const texterManifest = `
id: texter
version: 1
display_name: Texter
connection:
  base_url: "https://api.texter.example.com/v1/Accounts/{{ connection.params.account }}"
  connection_params:
    - { name: account, display_name: Account, pattern: "AC[0-9a-f]{4}" }
  auth:
    - basic: { username_param: account, password_label: Auth Token }
  probe:
    url: "https://api.texter.example.com/v1/Accounts/{{ connection.params.account }}.json"
    external_id: response.sid
    label: response.friendly_name
    routes_events: true
actions:
  - id: message.send
    placement: server
    params: { type: object }
    request: { method: POST, path: /Messages.json }
triggers:
  - id: message.received
    events: [message.received]
    data: { type: object }
`

// fakeTexter answers the account probe for the one account it knows, with
// the one token it accepts.
type fakeTexter struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls int
	sid   string
}

func newFakeTexter(t *testing.T) *fakeTexter {
	t.Helper()
	f := &fakeTexter{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		sid := f.sid
		f.mu.Unlock()
		user, pass, ok := r.BasicAuth()
		if !ok || user != "AC0001" || pass != "right-token" || r.URL.Path != "/v1/Accounts/AC0001.json" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code": 20003, "message": "Authenticate"}`))
			return
		}
		if sid == "" {
			sid = user
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sid": sid, "friendly_name": "Acme Texting"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func texterEnv(t *testing.T) (*env, *fakeTexter) {
	t.Helper()
	e := newEnv(t)
	f := newFakeTexter(t)
	providers, err := connections.ProvidersFromCatalog(testManifests(t, texterManifest), oauthEnv(nil))
	require.NoError(t, err)
	doer := hostRouter{"texter.example.com": f.srv, "": e.gh.srv}
	e.providers = providers
	e.tokens = connections.NewTokenSource(e.store, e.vault, providers, doer)
	e.broker = connections.NewBroker(e.store, e.vault, providers, doer, "https://reliant.example").WithAppOrigins([]string{testAppOrigin})
	e.svc = connections.NewService(e.store, e.vault, providers, e.tokens, e.broker, doer)
	e.resolver = connections.NewResolver(e.repo, e.store, e.tokens)
	return e, f
}

func basicTexter(userID, name, account, token string) connections.CreateAPIKeyParams {
	return connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: "texter", Name: name, Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"password": token}, Params: map[string]string{"account": account},
	}
}

// A pasted credential for an integration whose events route by account is
// probed when it is saved, and the account it records is the one the
// PROVIDER names. That account is what inbound events route on, so it must
// never come from what the user typed alone.
func TestRoutedIntegrationProbesAPastedCredentialAtCreate(t *testing.T) {
	e, f := texterEnv(t)
	ctx := context.Background()

	conn, err := e.svc.CreateAPIKey(ctx, basicTexter("alice", "texts", "AC0001", "right-token"))
	require.NoError(t, err)
	require.NotNil(t, conn.ExternalAccountID, "the probe recorded the account")
	require.Equal(t, "AC0001", *conn.ExternalAccountID)
	require.NotNil(t, conn.AccountLabel)
	require.Equal(t, "Acme Texting", *conn.AccountLabel)
	require.Equal(t, 1, f.calls)
}

// A credential the provider refuses is not saved: a connection that cannot
// prove which account it is could never receive that account's events, and
// a typo should be caught while the user is still looking at the form.
func TestRoutedIntegrationRefusesACredentialTheProviderRejects(t *testing.T) {
	e, _ := texterEnv(t)
	ctx := context.Background()

	_, err := e.svc.CreateAPIKey(ctx, basicTexter("alice", "wrong", "AC0001", "wrong-token"))
	require.ErrorIs(t, err, connections.ErrInvalidArgument)
	require.Contains(t, err.Error(), "Texter")

	// The sid of an account the user holds no token for: refused the same way.
	_, err = e.svc.CreateAPIKey(ctx, basicTexter("mallory", "theirs", "AC0002", "right-token"))
	require.ErrorIs(t, err, connections.ErrInvalidArgument)
	require.Zero(t, e.count(`SELECT count(*) FROM connections`))
}

// The account recorded is the provider's answer, not the param: a probe that
// names a different account than the one typed is what the connection is.
func TestRoutedIntegrationRecordsTheProvidersAccountNotTheParam(t *testing.T) {
	e, f := texterEnv(t)
	f.sid = "AC0001-canonical"
	conn, err := e.svc.CreateAPIKey(context.Background(), basicTexter("alice", "t", "AC0001", "right-token"))
	require.NoError(t, err)
	require.Equal(t, "AC0001-canonical", *conn.ExternalAccountID)
}

// Declaring triggers is not what opts in: GitHub declares triggers but routes
// them by access grants, not by the connection's account, so a saved PAT is
// not probed (and not refused) at create. Only probe.routes_events opts in.
func TestTriggersWithoutRoutesEventsDoNotProbeAtCreate(t *testing.T) {
	e, f := texterEnv(t)
	accessGated := strings.Replace(texterManifest, "    routes_events: true\n", "", 1)
	require.NotEqual(t, texterManifest, accessGated)
	providers, err := connections.ProvidersFromCatalog(testManifests(t, accessGated), oauthEnv(nil))
	require.NoError(t, err)
	doer := hostRouter{"texter.example.com": f.srv, "": e.gh.srv}
	tokens := connections.NewTokenSource(e.store, e.vault, providers, doer)
	broker := connections.NewBroker(e.store, e.vault, providers, doer, "https://reliant.example")
	svc := connections.NewService(e.store, e.vault, providers, tokens, broker, doer)

	conn, err := svc.CreateAPIKey(context.Background(), basicTexter("alice", "t", "AC0001", "any-token"))
	require.NoError(t, err)
	require.Nil(t, conn.ExternalAccountID)
	require.Zero(t, f.calls, "no provider call at create")
}

// An integration that routes nothing by account keeps today's behaviour:
// saving a pasted key makes no provider call.
func TestUnroutedIntegrationDoesNotProbeAtCreate(t *testing.T) {
	e, _ := acmeEnv(t, nil)
	conn, err := e.svc.CreateAPIKey(context.Background(), connections.CreateAPIKeyParams{
		UserID: "alice", IntegrationID: "acme", Name: "k", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "k1"}, Params: map[string]string{"tenant": "red"},
	})
	require.NoError(t, err)
	require.Nil(t, conn.ExternalAccountID)
}
