// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// Trigger routing under the org integrations policy (control-plane
// docs/design/org-policies.md §4.3): a trigger whose org does not allow its
// integration hears nothing and is paused once; other triggers are unchanged.

// orgPolicyByID answers acme's policy (the "test" integration not allowed);
// every other org has none. down is a control plane that cannot be asked.
type orgPolicyByID struct{ down bool }

func (s orgPolicyByID) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if s.down {
		return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
	}
	if orgID != "acme" {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 2, Policy: &controlplanev1.OrgPolicy{Integrations: &controlplanev1.IntegrationsPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, Allowed: []string{"slack"},
	}}}, nil
}

// pauser records pauses the way the store does: once per trigger.
type pauser struct {
	mu     sync.Mutex
	paused map[string]string
}

func (p *pauser) PauseTrigger(_ context.Context, id, reason string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.paused[id]; ok {
		return false, nil
	}
	p.paused[id] = reason
	return true, nil
}

func setIntegrationsSwitch(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointIntegrations)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, prev) })
}

func orgIntegrationTrigger(t *testing.T, id, org string) *core.Trigger {
	tr := integrationTrigger(t, id, "alice", core.IntegrationConfig{Events: []string{"issues.opened"}}, "")
	if org != "" {
		tr.OrgID = &org
	}
	return tr
}

// deliverWithPolicy is deliver with the org policy resolver on the request's
// context, as the API listener installs it.
func (e *eventsEnv) deliverWithPolicy(t *testing.T, src orgpolicy.Source, ev TestDelivery) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(ev)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/integrations/test/events", strings.NewReader(string(body)))
	req.Header.Set(TestSignatureHeader, SignTestDelivery("test-secret", "https://api.example.com/integrations/test/events", body))
	req = req.WithContext(orgpolicy.WithResolver(req.Context(), orgpolicy.New(src)))
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func newPolicyEventsEnv(t *testing.T) (*eventsEnv, *pauser) {
	env := newEventsEnv(t, "https://api.example.com")
	p := &pauser{paused: map[string]string{}}
	receiver := NewEventsReceiver(EventsOptions{Store: env.store, Intake: env.intake, Registry: env.registry,
		PublicURL: "https://api.example.com", AutoPause: triggers.NewAutoPause(p, nil)})
	mux := http.NewServeMux()
	receiver.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	env.handler = mux
	env.route(orgIntegrationTrigger(t, "acme-issues", "acme"), "100", core.ConnectionStatusActive)
	env.route(orgIntegrationTrigger(t, "globex-issues", "globex"), "100", core.ConnectionStatusActive)
	env.route(orgIntegrationTrigger(t, "no-org-issues", ""), "100", core.ConnectionStatusActive)
	return env, p
}

func TestEventsPolicy_ADisallowedIntegrationIsNotRoutedAndItsTriggerPaused(t *testing.T) {
	setIntegrationsSwitch(t, orgpolicy.EnforcementEnforce)
	env, p := newPolicyEventsEnv(t)

	for _, delivery := range []string{"d1", "d2"} {
		rec := env.deliverWithPolicy(t, orgPolicyByID{}, issueOpened(delivery, "100", "acme/app"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	assert.Equal(t, []string{"globex-issues", "globex-issues", "no-org-issues", "no-org-issues"}, env.intake.triggerIDs(),
		"an org with no policy, and a trigger of no org, are unchanged")
	assert.Equal(t, map[string]string{"acme-issues": core.TriggerPausedOrgPolicy}, p.paused, "paused once, with org_policy")
}

func TestEventsPolicy_AuditRoutesEverything(t *testing.T) {
	setIntegrationsSwitch(t, orgpolicy.EnforcementAudit)
	env, p := newPolicyEventsEnv(t)
	rec := env.deliverWithPolicy(t, orgPolicyByID{}, issueOpened("d1", "100", "acme/app"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"acme-issues", "globex-issues", "no-org-issues"}, env.intake.triggerIDs())
	assert.Empty(t, p.paused)
}

// An enforcing point that cannot read the policy refuses the delivery
// retryably (503): the sender redelivers, the routed half dedupes.
func TestEventsPolicy_AnUnreadablePolicyUnderEnforceIsRetried(t *testing.T) {
	setIntegrationsSwitch(t, orgpolicy.EnforcementEnforce)
	env, p := newPolicyEventsEnv(t)
	rec := env.deliverWithPolicy(t, orgPolicyByID{down: true}, issueOpened("d1", "100", "acme/app"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, []string{"no-org-issues"}, env.intake.triggerIDs(), "a trigger of no org needs no policy")
	assert.Empty(t, p.paused, "an outage decides nothing")
}
