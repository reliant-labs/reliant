// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// A launch the org's provider policy refused (control-plane
// docs/design/org-policies.md §5, §5.1): the firing is recorded failed with
// the policy's reason, without a retry, and the trigger is paused once with
// org_policy; its next fire skips on the pause instead of failing every tick.
func TestFire_ProviderPolicyRefusalFailsAndPausesOnce(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = ptrTo("acme") })
	repo.triggers[trigger.ID] = trigger
	refused := &launch.PolicyRefusedError{Input: "model", Refusal: &orgpolicy.Refusal{Reason: orgpolicy.ReasonProviderNotAllowed}}
	launcher := &fakeLauncher{err: refused}
	schedules := &fakeSchedules{}
	firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, schedules))

	_, err := firer.Fire(acmeMember(), fireAt(trigger.ID, 2))
	requireNonRetryable(t, err)
	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, "Your organization's policy does not allow this provider")
	assert.Contains(t, events[0].OutcomeDetail, "The automation was paused")
	assert.False(t, trigger.Enabled)
	assert.Equal(t, core.TriggerPausedOrgPolicy, trigger.PausedReason)
	assert.Equal(t, []string{ScheduleID(trigger.ID)}, schedules.paused)

	out, err := firer.Fire(acmeMember(), fireAt(trigger.ID, 3))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, 1, repo.pauses, "paused once")
	assert.Len(t, launcher.snapshot(), 1, "the paused trigger's fire never reaches the launcher")
}

// With no AutoPause wired the refusal is still recorded, never retried.
func TestFire_ProviderPolicyRefusalWithoutAutoPauseRecordsOnly(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = ptrTo("acme") })
	repo.triggers[trigger.ID] = trigger
	refused := &launch.PolicyRefusedError{Input: "model", Refusal: &orgpolicy.Refusal{Reason: orgpolicy.ReasonModelNotAllowed}}

	_, err := NewFirer(repo, &fakeLauncher{err: refused}).Fire(acmeMember(), fireAt(trigger.ID, 2))
	requireNonRetryable(t, err)
	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, "does not allow this model")
	assert.True(t, trigger.Enabled)
}
