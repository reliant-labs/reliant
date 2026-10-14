// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

const dedupeWorkflow = `name: alerts
triggers:
  - name: alertmanager
    webhook: {}
    dedupe_key: "{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}"
    dedupe_window: 30m
  - name: plain
    webhook: {}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func alertActivation(declared string) *core.Trigger {
	return &core.Trigger{
		ID: "t-alert", UserID: "user-1", ProjectID: "project-1", Name: "alerts",
		Kind: core.TriggerKindWebhook, Enabled: true, Workflow: "alerts", WorkflowTrigger: &declared,
		Message: "Triage the alert.", DaemonID: testDaemonID, Config: json.RawMessage(`{}`),
	}
}

// alertDelivery is one Alertmanager notification: no idempotency header, and a
// body that differs between deliveries of the same group (the alert list and
// timestamps move on every repeat_interval).
func alertDelivery(trig *core.Trigger, receivedMinute int, status, groupKey, extra string) InboundEvent {
	at := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC).Add(time.Duration(receivedMinute) * time.Minute)
	return InboundEvent{
		Kind: core.TriggerEventKindWebhook,
		// The receiver's default key: the body hash in a one-minute bucket, so
		// each of these is, by default, a distinct event.
		DedupeKey:  trig.ID + ":body:" + extra + ":" + at.Format(time.RFC3339),
		OccurredAt: at,
		Payload: map[string]any{"body": map[string]any{
			"groupKey": groupKey, "status": status, "extra": extra,
		}},
		Sender: &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: trig.ID, Verified: true},
	}
}

// distinctFires counts the fire workflows started. A repeat of a still-pending
// event re-issues the start of the SAME fire id (the intake's repair path;
// Temporal attaches to the running execution), so executions, not calls, are
// what "runs" means.
func distinctFires(s *recordingStarter) int {
	ids := map[string]bool{}
	for _, st := range s.snapshot() {
		ids[st.ID] = true
	}
	return len(ids)
}

func newDedupeIntake(t *testing.T, trig *core.Trigger, now time.Time) (*Intake, *recordingStarter, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	repo.triggers[trig.ID] = trig
	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "q").WithWorkflows(fakeWorkflows{yaml: map[string]string{"alerts": dedupeWorkflow}})
	intake.now = func() time.Time { return now }
	return intake, starter, repo
}

func TestDedupeKey_RepeatDeliveriesOfOneAlertGroupAreOneRun(t *testing.T) {
	trig := alertActivation("alertmanager")
	now := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)
	intake, starter, _ := newDedupeIntake(t, trig, now)
	ctx := context.Background()
	const group = `{}:{alertname="HighLatency"}`

	first, err := intake.Accept(ctx, trig, alertDelivery(trig, 0, "firing", group, "v1"), AcceptOptions{})
	require.NoError(t, err)
	require.False(t, first.Duplicate)
	require.Equal(t, core.TriggerEventPending, first.Outcome, first.Detail)

	// Re-sent with a different body and a different default key: same group,
	// same status, inside the window.
	for i, extra := range []string{"v2", "v3"} {
		now = now.Add(4 * time.Minute)
		intake.now = func() time.Time { return now }
		again, err := intake.Accept(ctx, trig, alertDelivery(trig, 4*(i+1), "firing", group, extra), AcceptOptions{})
		require.NoError(t, err)
		assert.True(t, again.Duplicate, "delivery %q is the same event", extra)
		assert.Equal(t, first.EventID, again.EventID, "recorded against the first event")
	}
	assert.Equal(t, 1, distinctFires(starter), "one run for three deliveries")

	// The resolution of the group is a different event.
	resolved, err := intake.Accept(ctx, trig, alertDelivery(trig, 14, "resolved", group, "v4"), AcceptOptions{})
	require.NoError(t, err)
	assert.False(t, resolved.Duplicate)
	assert.NotEqual(t, first.EventID, resolved.EventID)

	// A different group is a different event.
	other, err := intake.Accept(ctx, trig, alertDelivery(trig, 15, "firing", `{}:{alertname="DiskFull"}`, "v1"), AcceptOptions{})
	require.NoError(t, err)
	assert.False(t, other.Duplicate)
	assert.Equal(t, 3, distinctFires(starter))
}

func TestDedupeKey_TheWindowIsExplicitAndEndsAtItsBoundary(t *testing.T) {
	trig := alertActivation("alertmanager") // dedupe_window: 30m, so buckets end at :00 and :30
	ctx := context.Background()
	const group = `{}:{alertname="HighLatency"}`

	at := func(h, m int) time.Time { return time.Date(2026, 10, 14, h, m, 0, 0, time.UTC) }
	intake, starter, _ := newDedupeIntake(t, trig, at(10, 1))
	_, err := intake.Accept(ctx, trig, alertDelivery(trig, 0, "firing", group, "a"), AcceptOptions{})
	require.NoError(t, err)

	intake.now = func() time.Time { return at(10, 29) }
	dup, err := intake.Accept(ctx, trig, alertDelivery(trig, 28, "firing", group, "b"), AcceptOptions{})
	require.NoError(t, err)
	assert.True(t, dup.Duplicate, "10:29 is in the 10:00-10:30 window")

	intake.now = func() time.Time { return at(10, 31) }
	next, err := intake.Accept(ctx, trig, alertDelivery(trig, 30, "firing", group, "c"), AcceptOptions{})
	require.NoError(t, err)
	assert.False(t, next.Duplicate, "10:31 is the next window: the alert is still firing, so it is reported again")
	assert.Equal(t, 2, distinctFires(starter))
}

func TestDedupeKey_AKeyThatDoesNotRenderFallsBackToTheReceiversKey(t *testing.T) {
	trig := alertActivation("alertmanager")
	intake, starter, _ := newDedupeIntake(t, trig, time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC))
	ctx := context.Background()

	// No groupKey in the body: the key cannot render. Both deliveries are
	// recorded under the receiver's own (different) keys rather than being
	// merged into one event or dropped.
	for i, extra := range []string{"x", "y"} {
		ev := alertDelivery(trig, i, "firing", "", extra)
		ev.Payload = map[string]any{"body": map[string]any{"status": "firing", "extra": extra}}
		res, err := intake.Accept(ctx, trig, ev, AcceptOptions{})
		require.NoError(t, err)
		assert.False(t, res.Duplicate, "an unusable key must not suppress anything")
		assert.Equal(t, core.TriggerEventPending, res.Outcome)
	}
	assert.Len(t, starter.snapshot(), 2)
}

func TestDedupeKey_OnlyADeclaredWebhookKeyChangesTheKey(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)

	// A webhook trigger that declares no dedupe_key keeps the receiver's key:
	// distinct default keys stay distinct events.
	plain := alertActivation("plain")
	intake, starter, _ := newDedupeIntake(t, plain, now)
	a, err := intake.Accept(ctx, plain, alertDelivery(plain, 0, "firing", "g", "a"), AcceptOptions{})
	require.NoError(t, err)
	b, err := intake.Accept(ctx, plain, alertDelivery(plain, 1, "firing", "g", "b"), AcceptOptions{})
	require.NoError(t, err)
	assert.False(t, a.Duplicate)
	assert.False(t, b.Duplicate)
	assert.Equal(t, 2, distinctFires(starter))

	// An integration event is never rekeyed, whatever the declaration says.
	trig := alertActivation("alertmanager")
	intake, _, repo := newDedupeIntake(t, trig, now)
	ev := alertDelivery(trig, 0, "firing", "g", "a")
	ev.Kind = core.TriggerEventKindIntegration
	ev.DedupeKey = trig.ID + ":delivery-1"
	_, err = intake.Accept(ctx, trig, ev, AcceptOptions{})
	require.NoError(t, err)
	rows := repo.eventsFor(trig.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, trig.ID+":delivery-1", rows[0].DedupeKey)
}

func TestDedupeKey_AnAdHocWebhookTriggerIsUntouched(t *testing.T) {
	trig := webhookTrigger(t, "")
	repo := newFakeRepo()
	repo.triggers[trig.ID] = trig
	intake := NewIntake(repo, &recordingStarter{}, "q")
	res, err := intake.Accept(context.Background(), trig, inbound(trig, "k1", map[string]any{"body": map[string]any{}}), AcceptOptions{})
	require.NoError(t, err)
	assert.False(t, res.Duplicate)
	rows := repo.eventsFor(trig.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, trig.ID+":k1", rows[0].DedupeKey)
}
