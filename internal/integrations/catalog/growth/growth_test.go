// Copyright (c) 2025 Reliant Labs

// Package growth_test pins the embedded `growth` manifest. Its point is that a
// Growth key is only ever sent to Growth: the base URL is a literal in the
// manifest, and these tests try every override surface the catalog offers
// (connection params, action params, a request URL, a path, a redirect, a
// credential's own params, a user-authored manifest) and show none moves it.
package growth_test

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

const (
	shippedBase = "https://lunar-elk.sites.reliantlabs.dev/_api"
	growthKey   = "fk_growth-key-canary-7c1e"
)

func shipped(t *testing.T) *reliantv1.IntegrationManifest {
	t.Helper()
	m, err := catalog.MustBuiltin().Manifest("growth", 1)
	require.NoError(t, err)
	return m
}

func TestGrowthBaseURLIsAFixedLiteral(t *testing.T) {
	conn := shipped(t).GetConnection()
	assert.Equal(t, shippedBase, conn.GetBaseUrl())
	assert.NotContains(t, conn.GetBaseUrl(), "{{", "no template: no connection param can reach the origin")
	assert.Empty(t, conn.GetConnectionParams(), "a connection supplies no setting that could name a host")
	assert.Empty(t, conn.GetAllowedHosts(), "no second host a credential may be sent to")
	assert.False(t, conn.GetAllowAnyPublicHost(), "not the generic any-host integration")
	assert.False(t, conn.GetAuthOptional(), "every call carries the key")
	require.Len(t, conn.GetAuth(), 1)
	k, ok := manifest.Method(conn, manifest.AuthAPIKey)
	require.True(t, ok, "api_key is the only way to connect")
	assert.Equal(t, "header", k.GetApiKey().GetIn())
	assert.Equal(t, "Authorization", k.GetApiKey().GetName())
	assert.Equal(t, "Bearer ", k.GetApiKey().GetPrefix())
}

// No action may take a URL or path from the caller, and none may declare a
// param that reads like a host: a workflow author or model supplies params,
// and every one is closed (additionalProperties: false).
func TestGrowthNoActionParamCanNameAHost(t *testing.T) {
	m := shipped(t)
	require.NotEmpty(t, m.GetActions())
	for _, a := range m.GetActions() {
		where := "growth/" + a.GetId()
		req := a.GetRequest()
		require.NotNil(t, req, where)
		assert.Empty(t, req.GetUrl(), "%s: requests are paths under the fixed base, never a url", where)
		assert.NotEmpty(t, req.GetPath(), where)
		assert.True(t, strings.HasPrefix(req.GetPath(), "/services."), "%s: path %q is a literal RPC", where, req.GetPath())
		assert.NotContains(t, req.GetPath(), "{{", "%s: the path takes no caller value", where)
		assert.Empty(t, a.GetExecutor(), "%s: an executor could dial anywhere", where)
		assert.Empty(t, req.GetHeadersExpr(), "%s: no caller-chosen headers", where)
		assert.Empty(t, req.GetHeaders(), where)
		assert.Empty(t, req.GetQueryExpr(), where)
		assert.Equal(t, "POST", req.GetMethod(), where)

		params := a.GetParams().AsMap()
		assert.Equal(t, false, params["additionalProperties"], "%s: unknown params are refused", where)
		props, _ := params["properties"].(map[string]any)
		for name := range props {
			for _, bad := range []string{"url", "uri", "host", "origin", "base", "endpoint", "domain", "server", "path"} {
				assert.NotContains(t, strings.ToLower(name), bad, "%s: param %q could steer the request", where, name)
			}
		}
	}
}

// A workflow author cannot supply a manifest that reuses the id with another
// host: user manifests are validated untrusted, so the curated id and its
// placement are not theirs to claim, and a catalog lookup only ever returns
// the embedded one.
func TestGrowthCannotBeShadowedByAnAuthorManifest(t *testing.T) {
	evil := proto.Clone(shipped(t)).(*reliantv1.IntegrationManifest)
	evil.Connection.BaseUrl = "https://evil.example.com/_api"
	require.Error(t, manifest.Validate(evil, manifest.TrustUser),
		"server placement is curated-only, so an author's copy of growth is refused")

	got, err := catalog.MustBuiltin().Manifest("growth", 1)
	require.NoError(t, err)
	assert.Equal(t, shippedBase, got.GetConnection().GetBaseUrl())
	a, err := catalog.MustBuiltin().Resolve("growth/inbox.status@1")
	require.NoError(t, err)
	assert.Equal(t, shippedBase, a.Manifest.GetConnection().GetBaseUrl())
}

// A templated base_url is how the catalog lets a connection steer a host. The
// loader must keep refusing the shapes that would let a param take the whole
// origin, so a later edit cannot reopen it.
func TestGrowthBaseURLCannotBeTurnedIntoAParam(t *testing.T) {
	for name, base := range map[string]string{
		"whole origin":   "{{ connection.params.base_url }}",
		"scheme":         "{{ connection.params.scheme }}://lunar-elk.sites.reliantlabs.dev/_api",
		"two-label host": "https://{{ connection.params.host }}/_api",
		"http":           "http://lunar-elk.sites.reliantlabs.dev/_api",
		"userinfo":       "https://u@evil.example.com/_api",
	} {
		t.Run(name, func(t *testing.T) {
			m := proto.Clone(shipped(t)).(*reliantv1.IntegrationManifest)
			m.Connection.BaseUrl = base
			m.Connection.ConnectionParams = []*reliantv1.ConnectionParam{{Name: "base_url"}, {Name: "scheme"}, {Name: "host"}}
			assert.Error(t, manifest.Validate(m, manifest.TrustCurated))
		})
	}
}

type keyCred struct{ params map[string]string }

func (keyCred) ConnectionID() string        { return "conn_growth" }
func (c keyCred) Params() map[string]string { return c.params }
func (keyCred) Scrub(s string) string       { return strings.ReplaceAll(s, growthKey, "[redacted]") }
func (keyCred) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+growthKey)
	return nil
}

type credSource struct{ cred keyCred }

func (s credSource) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return s.cred, nil
}

type seen struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (s *seen) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, r)
	s.body = append(s.body, string(b))
}

func (s *seen) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// harness points a clone of the shipped manifest at a TLS fake, keeping the
// shipped path prefix, so the request that reaches the fake is the one the
// shipped manifest builds.
type harness struct {
	t      *testing.T
	fake   *httptest.Server
	other  *httptest.Server
	got    *seen
	gotOth *seen
}

func newHarness(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, otherURL string)) *harness {
	t.Helper()
	hn := &harness{t: t, got: &seen{}, gotOth: &seen{}}
	hn.other = netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hn.gotOth.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	hn.fake = netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hn.got.record(r)
		handler(w, r, hn.other.URL)
	}))
	return hn
}

func (hn *harness) run(actionID string, params map[string]any, credParams map[string]string) (*httpaction.Result, error) {
	hn.t.Helper()
	m := proto.Clone(shipped(hn.t)).(*reliantv1.IntegrationManifest)
	require.True(hn.t, strings.HasPrefix(m.Connection.BaseUrl, "https://lunar-elk.sites.reliantlabs.dev/"))
	m.Connection.BaseUrl = hn.fake.URL + strings.TrimPrefix(m.Connection.BaseUrl, "https://lunar-elk.sites.reliantlabs.dev")
	var act *reliantv1.ActionSpec
	for _, a := range m.GetActions() {
		if a.GetId() == actionID {
			act = a
		}
	}
	require.NotNil(hn.t, act, actionID)
	pool := x509.NewCertPool()
	pool.AddCert(hn.fake.Certificate())
	pool.AddCert(hn.other.Certificate())
	runner := httpaction.NewRunner(netguardtest.Guard()).WithRootCAs(pool)
	return runner.RunAuthenticated(context.Background(), m, act, params,
		credSource{keyCred{params: credParams}}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

func ok(body string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestGrowthSendsTheKeyToTheFixedOriginOnly(t *testing.T) {
	hn := newHarness(t, ok(`{"pending": 2, "expired": 0, "approved": 5, "rejected": 1,
		"pendingItems": [{"id": "a1", "operation": "REVIEW_PLAY_PLAN", "playName": "Launch"}]}`))
	res, err := hn.run("inbox.status", map[string]any{}, nil)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	require.Equal(t, 1, hn.got.count())
	r := hn.got.reqs[0]
	assert.Equal(t, "POST", r.Method)
	assert.Equal(t, "/_api/services.actions.v1.ActionsService/GetInboxStatus", r.URL.Path)
	assert.Equal(t, "Bearer "+growthKey, r.Header.Get("Authorization"))
	assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
	assert.JSONEq(t, `{}`, hn.got.body[0])
	assert.Equal(t, 0, hn.gotOth.count())

	assert.EqualValues(t, 2, res.Data["pending"])
	assert.EqualValues(t, 5, res.Data["approved"])
	items, _ := res.Data["pending_items"].([]any)
	require.Len(t, items, 1)
}

// Every override surface, one by one. Each attempt either is refused or has no
// effect: the request that reaches the network is always the pinned origin.
func TestGrowthBaseURLCannotBeOverridden(t *testing.T) {
	const evil = "https://evil.example.com/_api"
	t.Run("action params naming a url, base_url, host or path", func(t *testing.T) {
		for _, name := range []string{"url", "base_url", "baseUrl", "host", "origin", "endpoint", "path", "headers", "allowed_hosts"} {
			hn := newHarness(t, ok(`{}`))
			_, err := hn.run("inbox.status", map[string]any{name: evil}, nil)
			require.Error(t, err, "param %q must be refused as unknown", name)
			assert.Equal(t, 0, hn.got.count(), name)
			assert.Equal(t, 0, hn.gotOth.count(), name)
		}
	})
	t.Run("the same on every action", func(t *testing.T) {
		for _, a := range shipped(t).GetActions() {
			hn := newHarness(t, ok(`{}`))
			p := map[string]any{"url": evil}
			if a.GetId() == "agent_run.create" {
				p["workflow"] = "w"
			}
			_, err := hn.run(a.GetId(), p, nil)
			require.Error(t, err, a.GetId())
			assert.Equal(t, 0, hn.got.count()+hn.gotOth.count(), a.GetId())
		}
	})
	t.Run("a credential's connection params cannot move the origin", func(t *testing.T) {
		hn := newHarness(t, ok(`{}`))
		res, err := hn.run("inbox.status", map[string]any{}, map[string]string{
			"base_url": evil, "host": "evil.example.com", "url": evil, "tenant": "evil", "region": "evil",
		})
		require.NoError(t, err)
		require.False(t, res.IsError, res.Content)
		require.Equal(t, 1, hn.got.count(), "the request still went to the (test-pinned) origin")
		assert.Equal(t, "/_api/services.actions.v1.ActionsService/GetInboxStatus", hn.got.reqs[0].URL.Path)
		assert.Equal(t, 0, hn.gotOth.count())
	})
	t.Run("a search value is data, never a path", func(t *testing.T) {
		hn := newHarness(t, ok(`{"agentRuns": []}`))
		res, err := hn.run("agent_run.list", map[string]any{"search": "../../" + evil}, nil)
		require.NoError(t, err)
		require.False(t, res.IsError, res.Content)
		require.Equal(t, 1, hn.got.count())
		assert.Equal(t, "/_api/services.agents.v1.AgentsService/ListAgentRuns", hn.got.reqs[0].URL.Path)
		assert.Contains(t, hn.got.body[0], "evil.example.com")
	})
	t.Run("a redirect to another host is refused and the key is not sent there", func(t *testing.T) {
		hn := newHarness(t, func(w http.ResponseWriter, r *http.Request, otherURL string) {
			http.Redirect(w, r, otherURL+"/steal", http.StatusTemporaryRedirect)
		})
		res, err := hn.run("inbox.status", map[string]any{}, nil)
		if err == nil {
			require.True(t, res.IsError, "a cross-host redirect must not succeed")
		}
		assert.Equal(t, 0, hn.gotOth.count(), "the key never reached the second host")
	})
}

func TestGrowthErrorsAreClassifiedAndNeverEchoTheKey(t *testing.T) {
	for name, tc := range map[string]struct {
		status    int
		body      string
		retryable bool
		contains  string
	}{
		"revoked key": {401, `{"code":"unauthenticated","message":"key ` + growthKey + ` revoked"}`, false, "Reconnect Growth"},
		"forbidden":   {403, `{"code":"permission_denied","message":"members only"}`, false, "may not call"},
		"rate limit":  {429, `{}`, true, "rate limit"},
		"server":      {503, `{}`, true, "retry"},
	} {
		t.Run(name, func(t *testing.T) {
			hn := newHarness(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			res, err := hn.run("inbox.status", map[string]any{}, nil)
			require.NoError(t, err)
			require.True(t, res.IsError)
			assert.Equal(t, tc.retryable, res.Retryable)
			assert.Contains(t, res.Content, tc.contains)
			assert.NotContains(t, res.Content, growthKey)
		})
	}
}

func TestGrowthAgentRunCreateWithoutRecord(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{}`,
		"null":   `{"agentRun": null}`,
		"empty":  `{"agentRun": {}}`,
	} {
		t.Run(name, func(t *testing.T) {
			hn := newHarness(t, ok(body))
			res, err := hn.run("agent_run.create", map[string]any{"workflow": "community-watch"}, nil)
			require.NoError(t, err)
			require.False(t, res.IsError, res.Content)
			assert.Equal(t, map[string]any{"id": "", "workflow": "", "status": ""}, res.Data)
			assert.Equal(t, 1, hn.got.count())
		})
	}
}

func TestGrowthAgentRunRoundTrip(t *testing.T) {
	hn := newHarness(t, ok(`{"agentRun": {"id": "run_1", "workflow": "community-watch", "status": "AGENT_RUN_STATUS_SUCCEEDED"}}`))
	res, err := hn.run("agent_run.create", map[string]any{
		"workflow": "community-watch", "reason": "Community watch", "output_summary": "3 posted",
	}, nil)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, "/_api/services.agents.v1.AgentsService/CreateAgentRun", hn.got.reqs[0].URL.Path)
	assert.JSONEq(t, `{"workflow":"community-watch","status":"AGENT_RUN_STATUS_SUCCEEDED","reason":"Community watch","outputSummary":"3 posted"}`, hn.got.body[0])
	assert.Equal(t, "run_1", res.Data["id"])

	hn = newHarness(t, ok(`{"agentRuns": [{"id": "r1", "workflow": "community-watch"}], "nextPageToken": "t2", "totalCount": 7}`))
	res, err = hn.run("agent_run.list", map[string]any{"search": "community-watch", "page_size": 20}, nil)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.JSONEq(t, `{"pageSize":20,"orderBy":"created_at","descending":true,"search":"community-watch"}`, hn.got.body[0])
	assert.Equal(t, "t2", res.Data["next_page_token"])
	assert.EqualValues(t, 7, res.Data["total_count"])
}
