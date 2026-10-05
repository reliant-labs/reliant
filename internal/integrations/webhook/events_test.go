// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func integrationTrigger(t *testing.T, id, userID string, cfg core.IntegrationConfig, filter string) *core.Trigger {
	t.Helper()
	cfg.Integration = "test"
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	return &core.Trigger{ID: id, UserID: userID, Kind: core.TriggerKindIntegration, Enabled: true, Config: raw, Filter: filter}
}

type eventsEnv struct {
	store    *fakeStore
	intake   *fakeIntake
	registry *Registry
	handler  http.Handler
	provider *TestProvider
}

func newEventsEnv(t *testing.T, publicURL string) *eventsEnv {
	t.Helper()
	store := newFakeStore()
	intake := &fakeIntake{}
	provider := NewTestProvider("test-secret")
	registry := NewRegistry()
	require.NoError(t, registry.Register(provider))
	receiver := NewEventsReceiver(EventsOptions{Store: store, Intake: intake, Registry: registry, PublicURL: publicURL})
	mux := http.NewServeMux()
	receiver.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	return &eventsEnv{store: store, intake: intake, registry: registry, handler: mux, provider: provider}
}

func (e *eventsEnv) route(t *core.Trigger, account, status string) {
	e.store.routes["test"] = append(e.store.routes["test"], &core.IntegrationTriggerRoute{
		Trigger: t, ConnectionAccount: account, ConnectionStatus: status,
	})
}

func (e *eventsEnv) deliver(t *testing.T, ev TestDelivery) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(ev)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/integrations/test/events", strings.NewReader(string(body)))
	req.Header.Set(TestSignatureHeader, SignTestDelivery("test-secret", "https://api.example.com/integrations/test/events", body))
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func issueOpened(delivery, account, repo string) TestDelivery {
	return TestDelivery{
		ID: delivery, Account: account, Type: "issues.opened",
		Attributes: map[string]string{"repository": repo},
		Data:       map[string]any{"issue": map[string]any{"number": float64(42)}, "repository": repo},
	}
}

func TestEventsRouteToEveryCoveredTriggerAndNoOther(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	// Alice and Bob both have connections into installation 100; Carol's is
	// a different installation. Bob's is waiting on a reconnect.
	alice := integrationTrigger(t, "alice-issues", "alice", core.IntegrationConfig{Events: []string{"issues.*"}}, "")
	aliceRepo := integrationTrigger(t, "alice-other-repo", "alice", core.IntegrationConfig{
		Events: []string{"issues.opened"}, Match: map[string]string{"repository": "acme/other"},
	}, "")
	aliceFiltered := integrationTrigger(t, "alice-filtered", "alice", core.IntegrationConfig{Events: []string{"issues.opened"}},
		"trigger.payload.data.issue.number > 100")
	bob := integrationTrigger(t, "bob-issues", "bob", core.IntegrationConfig{Events: []string{"issues.opened"}}, "")
	carol := integrationTrigger(t, "carol-issues", "carol", core.IntegrationConfig{Events: []string{"issues.opened"}}, "")
	dave := integrationTrigger(t, "dave-prs", "dave", core.IntegrationConfig{Events: []string{"pull_request.*"}}, "")
	env.route(alice, "100", core.ConnectionStatusActive)
	env.route(aliceRepo, "100", core.ConnectionStatusActive)
	env.route(aliceFiltered, "100", core.ConnectionStatusActive)
	env.route(bob, "100", core.ConnectionStatusNeedsReauth)
	env.route(carol, "200", core.ConnectionStatusActive)
	env.route(dave, "100", core.ConnectionStatusActive)

	rec := env.deliver(t, issueOpened("delivery-1", "100", "acme/app"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// alice-filtered reaches the intake (its CEL filter is recorded there as
	// a skip); the others are pre-row mismatches and write nothing.
	assert.Equal(t, []string{"alice-filtered", "alice-issues"}, env.intake.triggerIDs())
	for _, a := range env.intake.all() {
		assert.Equal(t, a.TriggerID+":delivery-1", a.Event.DedupeKey, "one delivery fans out once per trigger")
		assert.Equal(t, "issues.opened", a.Event.Payload["event"])
		assert.Equal(t, "100", a.Event.Payload["account"])
	}
}

func TestEventsRedeliveryIsDeduped(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	env.route(integrationTrigger(t, "t1", "alice", core.IntegrationConfig{Events: []string{"issues.opened"}}, ""), "100", core.ConnectionStatusActive)

	for i := 0; i < 3; i++ {
		rec := env.deliver(t, issueOpened("delivery-1", "100", "acme/app"))
		require.Equal(t, http.StatusOK, rec.Code)
	}
	assert.Len(t, env.intake.all(), 1)
}

func TestEventsRejectsABadSignature(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	env.route(integrationTrigger(t, "t1", "alice", core.IntegrationConfig{Events: []string{"issues.opened"}}, ""), "100", core.ConnectionStatusActive)

	body, _ := json.Marshal(issueOpened("d", "100", "acme/app"))
	req := httptest.NewRequest(http.MethodPost, "/integrations/test/events", strings.NewReader(string(body)))
	req.Header.Set(TestSignatureHeader, SignTestDelivery("test-secret", "https://api.example.com/integrations/test/events", []byte(`{"tampered":true}`)))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, env.intake.all())
}

// The verifier sees the PUBLIC url — the one the sender signed — not the
// request's own host and scheme, which the ingress rewrote.
func TestEventsVerifierSeesThePublicURLWithQuery(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com/")
	env.route(integrationTrigger(t, "t1", "alice", core.IntegrationConfig{Events: []string{"issues.opened"}}, ""), "100", core.ConnectionStatusActive)

	body, _ := json.Marshal(issueOpened("d", "100", "acme/app"))
	req := httptest.NewRequest(http.MethodPost, "http://10.0.0.7:8080/integrations/test/events?b=2&a=1", strings.NewReader(string(body)))
	req.Header.Set(TestSignatureHeader, SignTestDelivery("test-secret", "https://api.example.com/integrations/test/events?b=2&a=1", body))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestEventsHandshakeIsAnsweredWithoutAnEvent(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	env.route(integrationTrigger(t, "t1", "alice", core.IntegrationConfig{Events: []string{"*"}}, ""), "100", core.ConnectionStatusActive)

	rec := env.deliver(t, TestDelivery{Challenge: "c-123"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "c-123", rec.Body.String(), "the provider's handshake reply is written verbatim")
	assert.Empty(t, env.intake.all())
}

func TestEventsUnknownProviderIsNotFound(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	req := httptest.NewRequest(http.MethodPost, "/integrations/nope/events", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestEventsWithoutAPublicURLRefuseToVerify(t *testing.T) {
	env := newEventsEnv(t, "")
	rec := env.deliver(t, issueOpened("d", "100", "acme/app"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"a provider that signs the URL cannot be verified against a guessed one")
}

func TestRegistryRejectsDuplicatesAndAnswersCatalogQueries(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(NewTestProvider("a")))
	assert.Error(t, r.Register(NewTestProvider("b")))
	assert.True(t, r.HasInboundSource("test"))
	assert.False(t, r.HasInboundSource("github"))
	_, ok := r.Provider("test")
	assert.True(t, ok)
}

// A provider that reports a non-empty account must never fire a trigger
// whose connection does not record that account — including one that records
// none at all.
func TestEventsNeverRouteToAConnectionWithNoAccount(t *testing.T) {
	env := newEventsEnv(t, "https://api.example.com")
	env.route(integrationTrigger(t, "t1", "alice", core.IntegrationConfig{Events: []string{"*"}}, ""), "", core.ConnectionStatusActive)
	rec := env.deliver(t, issueOpened("d", "100", "acme/app"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, env.intake.all())
}

var _ = context.Background
