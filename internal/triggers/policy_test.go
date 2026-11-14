// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The org integrations policy on integration triggers (control-plane
// docs/design/org-policies.md §4.3, §5): a trigger on a now-disallowed
// integration stops routing and is paused once, with reason org_policy.

// acmeOnlySlack is org acme enforcing an integrations list without GitHub.
type acmeOnlySlack struct{}

func (acmeOnlySlack) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if orgID != "acme" {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 4, Policy: &controlplanev1.OrgPolicy{Integrations: &controlplanev1.IntegrationsPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, Allowed: []string{"slack"},
	}}}, nil
}

func withIntegrationsSwitch(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointIntegrations)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, prev) })
}

func policyContext() context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeOnlySlack{}))
}

func TestIntegrationRefused_PausesOnceWithOrgPolicy(t *testing.T) {
	withIntegrationsSwitch(t, orgpolicy.EnforcementEnforce)
	repo := newFakeRepo()
	trigger := orgTrigger(t)
	repo.triggers[trigger.ID] = trigger
	schedules := &fakeSchedules{}
	pause := NewAutoPause(repo, schedules)
	ctx := policyContext()

	for i := 0; i < 3; i++ {
		refused, err := pause.IntegrationRefused(ctx, trigger, "github")
		require.NoError(t, err)
		assert.True(t, refused, "every event of a disallowed integration is refused")
	}
	assert.Equal(t, 1, repo.pauses, "paused once, not once per event")
	assert.False(t, repo.triggers[trigger.ID].Enabled)
	assert.Equal(t, core.TriggerPausedOrgPolicy, repo.triggers[trigger.ID].PausedReason)
	assert.Equal(t, []string{ScheduleID(trigger.ID)}, schedules.paused)

	refused, err := pause.IntegrationRefused(ctx, trigger, "slack")
	require.NoError(t, err)
	assert.False(t, refused, "an allowed integration is never refused")
}

func TestIntegrationRefused_UnaffectedWithoutAnEnforcedPolicy(t *testing.T) {
	ctx := policyContext()
	noOrg := testTrigger(t, nil)
	otherOrg := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = ptrTo("globex") })
	for name, tc := range map[string]struct {
		sw      orgpolicy.Enforcement
		trigger *core.Trigger
	}{
		"a trigger of no org":          {orgpolicy.EnforcementEnforce, noOrg},
		"an org with no policy":        {orgpolicy.EnforcementEnforce, otherOrg},
		"the platform switch in audit": {orgpolicy.EnforcementAudit, orgTrigger(t)},
		"the platform switch off":      {orgpolicy.EnforcementOff, orgTrigger(t)},
	} {
		t.Run(name, func(t *testing.T) {
			withIntegrationsSwitch(t, tc.sw)
			repo := newFakeRepo()
			repo.triggers[tc.trigger.ID] = tc.trigger
			refused, err := NewAutoPause(repo, nil).IntegrationRefused(ctx, tc.trigger, "github")
			require.NoError(t, err)
			assert.False(t, refused)
			assert.Zero(t, repo.pauses)
		})
	}
}

// Without an AutoPause the refusal still stops the event; nothing is paused.
func TestIntegrationRefused_NilAutoPauseJudgesOnly(t *testing.T) {
	withIntegrationsSwitch(t, orgpolicy.EnforcementEnforce)
	var pause *AutoPause
	refused, err := pause.IntegrationRefused(policyContext(), orgTrigger(t), "github")
	require.NoError(t, err)
	assert.True(t, refused)
}
