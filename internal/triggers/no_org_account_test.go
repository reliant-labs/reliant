// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// noOrgAccount is the launcher's refusal of an org_only launch that none of
// its org's own accounts can run.
var noOrgAccount = &launch.NoOrgAccountError{Model: "claude-opus-4-6"}

// acmeMember is a context whose creator is a live member of acme, the org of
// every org_only trigger here.
func acmeMember() context.Context {
	return authz.WithMembership(context.Background(), memberOf("acme"))
}

func orgOnlyTrigger(t *testing.T) *core.Trigger {
	return testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.OrgID = ptrTo("acme")
		tr.UnattendedFunding = core.UnattendedFundingOrgOnly
	})
}

// The stamp reaches the launch: only an org_only trigger asks the launcher to
// spend its org's accounts only. NULL (a trigger that predates the stamp) and
// any launch exactly as before.
func TestFire_OrgAccountsOnlyFollowsTheStamp(t *testing.T) {
	for stamp, want := range map[core.UnattendedFunding]bool{
		"":                            false,
		core.UnattendedFundingAny:     false,
		core.UnattendedFundingOrgOnly: true,
	} {
		t.Run(string(stamp), func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.UnattendedFunding = stamp })
			repo.triggers[trigger.ID] = trigger
			launcher := &fakeLauncher{}

			_, err := NewFirer(repo, launcher).Fire(acmeMember(), fireAt(trigger.ID, 2))
			require.NoError(t, err)
			assert.Equal(t, want, launcher.snapshot()[0].Spec.OrgAccountsOnly, "a schedule's fire")

			webhook := webhookTrigger(t, "")
			webhook.UnattendedFunding = stamp
			repo.triggers[webhook.ID] = webhook
			ev := pendingEvent(repo, webhook, "d1", nil)
			_, err = NewEventFirer(repo, launcher).Fire(acmeMember(),
				EventFireInput{TriggerID: webhook.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
			require.NoError(t, err)
			assert.Equal(t, want, launcher.snapshot()[1].Spec.OrgAccountsOnly, "an event's fire")
		})
	}
}

// A schedule no org account can run: the first fire is recorded failed with
// the reason and who can fix it, without a retry, and the trigger keeps
// running; the second in a row also pauses it (row and schedule) with
// no_org_account; the third skips on the pause. One pause, not one per tick.
func TestFire_NoOrgAccountFailsThenPausesOnTheSecondInARow(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgOnlyTrigger(t)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{err: noOrgAccount}
	schedules := &fakeSchedules{}
	firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, schedules))

	_, err := firer.Fire(acmeMember(), fireAt(trigger.ID, 2))
	requireNonRetryable(t, err)
	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Equal(t, noOrgAccountDetail("claude-opus-4-6"), events[0].OutcomeDetail)
	assert.Contains(t, events[0].OutcomeDetail, "An admin can add an org API key or enable Reliant credits")
	assert.True(t, trigger.Enabled, "one failure does not pause")
	assert.Equal(t, 0, repo.pauses)

	_, err = firer.Fire(acmeMember(), fireAt(trigger.ID, 3))
	requireNonRetryable(t, err)
	events = repo.eventsFor(trigger.ID)
	require.Len(t, events, 2)
	assert.Equal(t, core.TriggerEventFailed, events[1].Outcome)
	assert.Equal(t, noOrgAccountDetail("claude-opus-4-6")+noOrgAccountPaused, events[1].OutcomeDetail)
	assert.False(t, trigger.Enabled)
	assert.Equal(t, core.TriggerPausedNoOrgAccount, trigger.PausedReason)
	assert.Equal(t, []string{ScheduleID(trigger.ID)}, schedules.paused)

	out, err := firer.Fire(acmeMember(), fireAt(trigger.ID, 4))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, noOrgAccountPausedDetail, out.Reason)
	assert.Equal(t, 1, repo.pauses, "paused once")
	assert.Len(t, launcher.snapshot(), 2, "the paused trigger's fire never reaches the launcher")
	for _, call := range launcher.snapshot() {
		assert.True(t, call.Spec.OrgAccountsOnly, "never retried on a personal account")
	}
}

// Only consecutive failures pause: a firing that launched in between, or a
// failure of another kind, starts the count again.
func TestFire_NoOrgAccountPausesOnlyOnConsecutiveFailures(t *testing.T) {
	for name, between := range map[string]error{
		"a launch":        nil,
		"another failure": &launch.ValidationError{Reason: "workflow input validation failed"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepo()
			trigger := orgOnlyTrigger(t)
			repo.triggers[trigger.ID] = trigger
			launcher := &fakeLauncher{err: noOrgAccount}
			firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, nil))

			_, err := firer.Fire(acmeMember(), fireAt(trigger.ID, 2))
			requireNonRetryable(t, err)
			launcher.err = between
			_, _ = firer.Fire(acmeMember(), fireAt(trigger.ID, 3))
			if between == nil {
				// The real launcher records the launched event in the
				// chat's transaction; the fake does not.
				launched := fireAt(trigger.ID, 3)
				repo.events = append(repo.events, &core.TriggerEvent{ID: "launched", TriggerID: &trigger.ID,
					Kind: core.TriggerEventKindSchedule, DedupeKey: launched.FireWorkflowID,
					OccurredAt: launched.ScheduledAt, Outcome: core.TriggerEventLaunched})
			}
			launcher.err = noOrgAccount
			_, err = firer.Fire(acmeMember(), fireAt(trigger.ID, 4))
			requireNonRetryable(t, err)

			assert.True(t, trigger.Enabled)
			assert.Equal(t, 0, repo.pauses)
			events := repo.eventsFor(trigger.ID)
			require.Len(t, events, 3)
			assert.Equal(t, noOrgAccountDetail("claude-opus-4-6"), events[2].OutcomeDetail)
		})
	}
}

// A person's "run now" of a trigger already paused for it is refused the
// same way, and pauses nothing again.
func TestFire_ManualFireOfAPausedTriggerIsRefusedWithoutPausingAgain(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgOnlyTrigger(t)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{err: noOrgAccount}
	firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, nil))
	for day := 2; day <= 3; day++ {
		_, err := firer.Fire(acmeMember(), fireAt(trigger.ID, day))
		requireNonRetryable(t, err)
	}
	require.Equal(t, core.TriggerPausedNoOrgAccount, trigger.PausedReason)

	manual := fireAt(trigger.ID, 5)
	manual.Manual = true
	_, err := firer.Fire(acmeMember(), manual)
	requireNonRetryable(t, err)
	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 3)
	assert.Equal(t, noOrgAccountDetail("claude-opus-4-6"), events[2].OutcomeDetail)
	assert.Equal(t, 1, repo.pauses)
}

// The inbound path: two events in a row that no org account can run pause
// the activation once; each is settled failed and never retried.
func TestEventFire_NoOrgAccountPausesOnTheSecondInARow(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	trigger.OrgID = ptrTo("acme")
	trigger.UnattendedFunding = core.UnattendedFundingOrgOnly
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{err: noOrgAccount}
	firer := NewEventFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, nil))

	first := pendingEvent(repo, trigger, "d1", nil)
	second := pendingEvent(repo, trigger, "d2", nil)
	second.OccurredAt = first.OccurredAt.Add(time.Minute)

	_, err := firer.Fire(acmeMember(), EventFireInput{TriggerID: trigger.ID, Kind: first.Kind, DedupeKey: first.DedupeKey})
	requireNonRetryable(t, err)
	assert.True(t, trigger.Enabled)

	_, err = firer.Fire(acmeMember(), EventFireInput{TriggerID: trigger.ID, Kind: second.Kind, DedupeKey: second.DedupeKey})
	requireNonRetryable(t, err)
	assert.False(t, trigger.Enabled)
	assert.Equal(t, core.TriggerPausedNoOrgAccount, trigger.PausedReason)
	assert.Equal(t, 1, repo.pauses)
	assert.Equal(t, core.TriggerEventFailed, first.Outcome)
	assert.Equal(t, noOrgAccountDetail("claude-opus-4-6"), first.OutcomeDetail)
	assert.Equal(t, noOrgAccountDetail("claude-opus-4-6")+noOrgAccountPaused, second.OutcomeDetail)
}

// Without an AutoPause the failure is still recorded, and claims no pause.
func TestFire_NoOrgAccountWithoutAutoPauseRecordsTheFailureOnly(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgOnlyTrigger(t)
	repo.triggers[trigger.ID] = trigger
	firer := NewFirer(repo, &fakeLauncher{err: noOrgAccount})
	for day := 2; day <= 3; day++ {
		_, err := firer.Fire(acmeMember(), fireAt(trigger.ID, day))
		requireNonRetryable(t, err)
	}
	for _, ev := range repo.eventsFor(trigger.ID) {
		assert.Equal(t, noOrgAccountDetail("claude-opus-4-6"), ev.OutcomeDetail)
	}
	assert.True(t, trigger.Enabled)
}

// The rollback contract (control-plane docs/design/org-provider-accounts.md
// §14, "Strict automations (P11)"): a release that does not
// know a paused_reason still reads the trigger as paused, because enabled is
// false. Its fires skip, on both paths, with the generic detail; the API
// carries the reason through unchanged.
func TestPausedTriggerWithAnUnknownReasonStaysPaused(t *testing.T) {
	const fromANewerRelease = "a_reason_from_a_newer_release"
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.Enabled, tr.PausedReason = false, fromANewerRelease
	})
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}

	out, err := NewFirer(repo, launcher).Fire(acmeMember(), fireAt(trigger.ID, 2))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, "trigger is disabled", out.Reason)

	webhook := webhookTrigger(t, "")
	webhook.Enabled, webhook.PausedReason = false, fromANewerRelease
	repo.triggers[webhook.ID] = webhook
	ev := pendingEvent(repo, webhook, "d1", nil)
	out, err = NewEventFirer(repo, launcher).Fire(acmeMember(),
		EventFireInput{TriggerID: webhook.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Empty(t, launcher.snapshot(), "a paused trigger launches nothing")

	msg, err := ToProto(trigger, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, msg.GetEnabled())
	assert.Equal(t, fromANewerRelease, msg.GetPausedReason())
}

// The pause's hook (the worker's email to the org's admins) is told once, when
// the second failure in a row pauses the trigger, with the refused model; not
// for the first failure, and not when a later fire finds it already paused.
func TestFire_NoOrgAccountPauseTellsItsHookOnce(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgOnlyTrigger(t)
	repo.triggers[trigger.ID] = trigger
	type call struct{ id, model string }
	var calls []call
	pause := NewAutoPause(repo, nil).WithNoOrgAccountPause(func(_ context.Context, id, model string) {
		calls = append(calls, call{id, model})
	})
	firer := NewFirer(repo, &fakeLauncher{err: noOrgAccount}).WithAutoPause(pause)
	for day := 2; day <= 4; day++ {
		_, _ = firer.Fire(acmeMember(), fireAt(trigger.ID, day))
		if day == 2 {
			assert.Empty(t, calls, "one failure pauses nothing")
		}
	}
	manual := fireAt(trigger.ID, 5)
	manual.Manual = true
	_, _ = firer.Fire(acmeMember(), manual)
	assert.Equal(t, []call{{trigger.ID, "claude-opus-4-6"}}, calls)
}
