// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// orgMembership is control-plane's answer, scripted: live in the orgs it
// names, or an outage when down is set.
type orgMembership struct {
	mu   sync.Mutex
	live map[string]bool
	down bool
}

func memberOf(orgIDs ...string) *orgMembership {
	m := &orgMembership{live: map[string]bool{}}
	for _, org := range orgIDs {
		m.live[org] = true
	}
	return m
}

func (m *orgMembership) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return false, true, errors.New("control-plane unavailable")
	}
	return m.live[orgID], true, nil
}

func (m *orgMembership) set(down bool, orgIDs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = down
	m.live = map[string]bool{}
	for _, org := range orgIDs {
		m.live[org] = true
	}
}

// PauseTrigger is the store's pause: the first call disables the trigger
// with the reason, a call on a paused trigger writes nothing.
func (r *fakeRepo) PauseTrigger(_ context.Context, id, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.triggers[id]
	if !ok {
		return false, core.ErrTriggerNotFound
	}
	if !t.Enabled {
		return false, nil
	}
	t.Enabled, t.PausedReason = false, reason
	r.pauses++
	return true, nil
}

// fakeSchedules records which schedules were paused.
type fakeSchedules struct {
	ScheduleClient
	mu     sync.Mutex
	paused []string
}

type fakeScheduleHandle struct {
	client.ScheduleHandle
	id string
	s  *fakeSchedules
}

func (s *fakeSchedules) GetHandle(_ context.Context, id string) client.ScheduleHandle {
	return &fakeScheduleHandle{id: id, s: s}
}

func (h *fakeScheduleHandle) Pause(_ context.Context, _ client.SchedulePauseOptions) error {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	h.s.paused = append(h.s.paused, h.id)
	return nil
}

func orgTrigger(t *testing.T) *core.Trigger {
	return testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = ptrTo("acme") })
}

func fireAt(triggerID string, day int) FireRequest {
	at := time.Date(2026, 1, day, 9, 0, 0, 0, time.UTC)
	return FireRequest{TriggerID: triggerID, FireWorkflowID: FireWorkflowID(triggerID) + "-" + at.Format(time.RFC3339), ScheduledAt: at}
}

func requireNonRetryable(t *testing.T, err error) {
	t.Helper()
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.True(t, appErr.NonRetryable(), "error: %v", err)
}

func requireRetryable(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		assert.False(t, appErr.NonRetryable(), "an outage must be retried: %v", err)
	}
}

// A removed member's trigger: its first fire records one failure, pauses the
// trigger (row and schedule) with creator_left_org, and launches nothing; the
// next fire skips without launching or pausing again.
func TestFire_PausesATriggerWhoseCreatorLeftItsOrg(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgTrigger(t)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}
	schedules := &fakeSchedules{}
	firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, schedules))
	ctx := authz.WithMembership(context.Background(), memberOf("home"))

	_, err := firer.Fire(ctx, fireAt(trigger.ID, 2))
	requireNonRetryable(t, err)

	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Equal(t, creatorLeftOrgDetail, events[0].OutcomeDetail)
	assert.False(t, trigger.Enabled)
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, trigger.PausedReason)
	assert.Equal(t, 1, repo.pauses)
	assert.Equal(t, []string{ScheduleID(trigger.ID)}, schedules.paused)
	assert.Empty(t, launcher.snapshot(), "a creator who left the org launches nothing")

	out, err := firer.Fire(ctx, fireAt(trigger.ID, 3))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, creatorLeftOrgPausedDetail, out.Reason)
	events = repo.eventsFor(trigger.ID)
	require.Len(t, events, 2)
	assert.Equal(t, 1, countOutcome(events, core.TriggerEventFailed), "exactly one failure")
	assert.Equal(t, 1, repo.pauses)
	assert.Empty(t, launcher.snapshot())
}

// A membership outage is not an answer: the fire fails retryably, records
// nothing and pauses nothing.
func TestFire_AMembershipOutageDoesNotPause(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgTrigger(t)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}
	membership := memberOf("acme")
	membership.set(true)
	firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, &fakeSchedules{}))
	ctx := authz.WithMembership(context.Background(), membership)

	_, err := firer.Fire(ctx, fireAt(trigger.ID, 2))
	requireRetryable(t, err)
	assert.Empty(t, repo.eventsFor(trigger.ID))
	assert.True(t, trigger.Enabled)
	assert.Empty(t, trigger.PausedReason)
	assert.Empty(t, launcher.snapshot())

	// The retry, once control-plane answers, launches.
	membership.set(false, "acme")
	out, err := firer.Fire(ctx, fireAt(trigger.ID, 2))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
	assert.Equal(t, 0, repo.pauses)
}

// The launcher's own refusal pauses too: a trigger written before orgs has
// no org of its own, and its project's org is the one the creator left.
func TestFire_TheLaunchersRefusalPauses(t *testing.T) {
	for name, refusal := range map[string]error{
		"the project lookup's denial": &launch.NotFoundError{Reason: "project not found", Err: authz.ErrNotOrgMember},
		"a not_org_member refusal":    orgs.NotMember("acme"),
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, nil)
			repo.triggers[trigger.ID] = trigger
			launcher := &fakeLauncher{err: refusal}
			firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, nil))

			_, err := firer.Fire(context.Background(), fireAt(trigger.ID, 2))
			requireNonRetryable(t, err)
			events := repo.eventsFor(trigger.ID)
			require.Len(t, events, 1)
			assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
			assert.Equal(t, creatorLeftOrgDetail, events[0].OutcomeDetail)
			assert.Equal(t, core.TriggerPausedCreatorLeftOrg, trigger.PausedReason)
		})
	}
}

// A launch refused for an outage checking membership is retried, never
// recorded as the creator leaving.
func TestFire_ALaunchOutageIsRetried(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{err: &launch.InternalError{Reason: "could not verify", Err: authz.ErrMembershipUnavailable}}
	firer := NewFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, nil))

	_, err := firer.Fire(context.Background(), fireAt(trigger.ID, 2))
	requireRetryable(t, err)
	assert.Empty(t, repo.eventsFor(trigger.ID))
	assert.True(t, trigger.Enabled)
}

// Without an AutoPause the refusal is still recorded, and says nothing about
// a pause that did not happen.
func TestFire_WithoutAutoPauseRecordsTheRefusalOnly(t *testing.T) {
	repo := newFakeRepo()
	trigger := orgTrigger(t)
	repo.triggers[trigger.ID] = trigger
	ctx := authz.WithMembership(context.Background(), memberOf())

	_, err := NewFirer(repo, &fakeLauncher{}).Fire(ctx, fireAt(trigger.ID, 2))
	requireNonRetryable(t, err)
	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, creatorLeftOrgNoPause, events[0].OutcomeDetail)
	assert.True(t, trigger.Enabled)
}

// The inbound path: the first event fails and pauses; a concurrent event
// that finds the trigger already paused by it is skipped, so a burst records
// one failure.
func TestEventFire_PausesATriggerWhoseCreatorLeftItsOrg(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	trigger.OrgID = ptrTo("acme")
	repo.triggers[trigger.ID] = trigger
	first := pendingEvent(repo, trigger, "d1", map[string]any{})
	second := pendingEvent(repo, trigger, "d2", map[string]any{})
	launcher := &fakeLauncher{}
	firer := NewEventFirer(repo, launcher).WithAutoPause(NewAutoPause(repo, nil))
	ctx := authz.WithMembership(context.Background(), memberOf())

	_, err := firer.Fire(ctx, EventFireInput{TriggerID: trigger.ID, Kind: first.Kind, DedupeKey: first.DedupeKey})
	requireNonRetryable(t, err)
	assert.Equal(t, core.TriggerEventFailed, first.Outcome)
	assert.Equal(t, creatorLeftOrgDetail, first.OutcomeDetail)
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, trigger.PausedReason)

	// The second event was already in flight (a manual replay skips the
	// enabled check); it finds the trigger paused and records a skip.
	second.Payload[payloadManual] = true
	out, err := firer.Fire(ctx, EventFireInput{TriggerID: trigger.ID, Kind: second.Kind, DedupeKey: second.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, creatorLeftOrgPausedDetail, second.OutcomeDetail)
	assert.Equal(t, 1, countOutcome(repo.eventsFor(trigger.ID), core.TriggerEventFailed))
	assert.Equal(t, 1, repo.pauses)
	assert.Empty(t, launcher.snapshot())
}

// A schedule that is already gone (a webhook trigger has none) is not a
// failure of the pause.
func TestAutoPause_AMissingScheduleIsNotAnError(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	outcome, detail, err := NewAutoPause(repo, missingSchedules{}).Refuse(context.Background(), trigger)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventFailed, outcome)
	assert.Equal(t, creatorLeftOrgDetail, detail)
}

type missingSchedules struct{ ScheduleClient }

type missingScheduleHandle struct{ client.ScheduleHandle }

func (missingSchedules) GetHandle(context.Context, string) client.ScheduleHandle {
	return missingScheduleHandle{}
}

func (missingScheduleHandle) Pause(context.Context, client.SchedulePauseOptions) error {
	return serviceerror.NewNotFound("schedule not found")
}

func countOutcome(events []*core.TriggerEvent, outcome core.TriggerEventOutcome) int {
	n := 0
	for _, ev := range events {
		if ev.Outcome == outcome {
			n++
		}
	}
	return n
}
