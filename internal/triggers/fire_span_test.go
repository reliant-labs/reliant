// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// recordFireSpans swaps the global tracer provider for a recording one for
// the test, so these tests are never parallel.
func recordFireSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// causeSpan is a span standing in for whatever caused a firing (the fire
// activity, a webhook request, a redrive pass); it is still open. Its ctx
// knows the trigger owner is a member of org-1, the org the tests' triggers
// belong to.
func causeSpan(t *testing.T) (context.Context, trace.SpanContext) {
	t.Helper()
	member := authz.WithMembership(context.Background(), memberOf("org-1"))
	ctx, span := otel.Tracer("test").Start(member, "cause")
	t.Cleanup(func() { span.End() })
	return ctx, span.SpanContext()
}

// fireSpans are the ended trigger.fire spans of kind.
func fireSpans(rec *tracetest.SpanRecorder, kind core.TriggerEventKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == telemetry.SpanNameTriggerFire+" "+string(kind) {
			out = append(out, s)
		}
	}
	return out
}

func oneFireSpan(t *testing.T, rec *tracetest.SpanRecorder, kind core.TriggerEventKind) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := fireSpans(rec, kind)
	require.Len(t, spans, 1, "one trigger.fire %s span", kind)
	return spans[0]
}

func fireAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value.Emit()
	}
	return out
}

// assertRootLinkedTo checks s starts a trace of its own and links to cause.
func assertRootLinkedTo(t *testing.T, s sdktrace.ReadOnlySpan, cause trace.SpanContext) {
	t.Helper()
	assert.False(t, s.Parent().IsValid(), "a trigger.fire span is a root")
	assert.NotEqual(t, cause.TraceID(), s.SpanContext().TraceID(), "never in its cause's trace")
	require.Len(t, s.Links(), 1)
	assert.Equal(t, cause.SpanID(), s.Links()[0].SpanContext.SpanID(), "linked to its cause")
}

// assertNoText checks no attribute of s, and not its status, carries text
// the firing saw: an error, a filter, a payload.
func assertNoText(t *testing.T, s sdktrace.ReadOnlySpan, texts ...string) {
	t.Helper()
	assert.Empty(t, s.Events(), "no recorded error, whose text could carry a response")
	for _, text := range texts {
		assert.NotContains(t, s.Status().Description, text)
		for key, value := range fireAttrs(s) {
			assert.NotContains(t, value, text, "attribute %s", key)
		}
	}
}

// A schedule's fire that launches: its own root linked to the activity, the
// trigger's org and owner, the run it launched, and the launch names this
// span as the one its run links to.
func TestFireSpan_ScheduleLaunch(t *testing.T) {
	rec := recordFireSpans(t)
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.OrgID = ptrTo("org-1") })
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}
	ctx, cause := causeSpan(t)

	out, err := NewFirer(repo, launcher).Fire(ctx, fireReq(trigger.ID))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), out.Outcome)

	s := oneFireSpan(t, rec, core.TriggerEventKindSchedule)
	assertRootLinkedTo(t, s, cause)
	a := fireAttrs(s)
	assert.Equal(t, trigger.ID, a[telemetry.AttrTriggerID])
	assert.Equal(t, string(core.TriggerKindSchedule), a[telemetry.AttrTriggerKind])
	assert.Equal(t, "org-1", a[telemetry.AttrOrgID], "the trigger's org")
	assert.Equal(t, trigger.UserID, a[telemetry.AttrUserID], "the trigger's owner")
	assert.Equal(t, string(core.TriggerEventLaunched), a[telemetry.AttrTriggerOutcome])
	assert.Equal(t, out.EventID, a[telemetry.AttrTriggerEventID])
	assert.Equal(t, out.ChatID, a[telemetry.AttrGenAIConversationID])
	assert.Equal(t, dedupeKeyHash(fireReq(trigger.ID).FireWorkflowID), a[telemetry.AttrTriggerDedupeKeyHash])
	assert.Len(t, a[telemetry.AttrTriggerDedupeKeyHash], 16)
	assert.Equal(t, codes.Unset, s.Status().Code)

	calls := launcher.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, telemetry.TraceParentOf(s.SpanContext()), calls[0].Spec.FiringTraceParent,
		"the run links to this firing, not to the activity")
}

// A skip is a span too: the case that was invisible.
func TestFireSpan_ScheduleSkip(t *testing.T) {
	rec := recordFireSpans(t)
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.Enabled = false
		tr.OrgID = ptrTo("org-1")
	})
	repo.triggers[trigger.ID] = trigger
	ctx, cause := causeSpan(t)

	_, err := NewFirer(repo, &fakeLauncher{}).Fire(ctx, fireReq(trigger.ID))
	require.NoError(t, err)

	s := oneFireSpan(t, rec, core.TriggerEventKindSchedule)
	assertRootLinkedTo(t, s, cause)
	a := fireAttrs(s)
	assert.Equal(t, string(core.TriggerEventSkipped), a[telemetry.AttrTriggerOutcome])
	assert.Equal(t, telemetry.TriggerReasonTriggerPaused, a[telemetry.AttrTriggerReason])
	assert.Equal(t, "org-1", a[telemetry.AttrOrgID])
	assert.Equal(t, repo.eventsFor(trigger.ID)[0].ID, a[telemetry.AttrTriggerEventID])
	assert.Equal(t, codes.Unset, s.Status().Code, "a skip is not an error")
}

// An inbound event's fire that launches, and one that fails: Error status
// with a code for a description, never the launcher's text.
func TestFireSpan_EventFire(t *testing.T) {
	t.Run("launched", func(t *testing.T) {
		rec := recordFireSpans(t)
		repo := newFakeRepo()
		trigger := webhookTrigger(t, "")
		trigger.OrgID = ptrTo("org-1")
		repo.triggers[trigger.ID] = trigger
		ev := pendingEvent(repo, trigger, "d1", map[string]any{"body": "secret payload"})
		launcher := &fakeLauncher{result: &launch.Result{Chat: &db.Chat{ID: "chat-1"}, WorkflowID: "chat-1", RunID: "run-1", EventID: ev.ID}}
		ctx, cause := causeSpan(t)

		out, err := NewEventFirer(repo, launcher).Fire(ctx, EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
		require.NoError(t, err)

		s := oneFireSpan(t, rec, core.TriggerEventKindWebhook)
		assertRootLinkedTo(t, s, cause)
		a := fireAttrs(s)
		assert.Equal(t, string(core.TriggerEventLaunched), a[telemetry.AttrTriggerOutcome])
		assert.Equal(t, "org-1", a[telemetry.AttrOrgID])
		assert.Equal(t, "chat-1", a[telemetry.AttrGenAIConversationID])
		assert.Equal(t, "run-1", a[telemetry.AttrAIRunID])
		assert.Equal(t, ev.ID, a[telemetry.AttrTriggerEventID])
		assert.Equal(t, out.EventID, ev.ID)
		assertNoText(t, s, "secret payload")
		assert.Equal(t, telemetry.TraceParentOf(s.SpanContext()), launcher.snapshot()[0].Spec.FiringTraceParent)
	})
	t.Run("failed", func(t *testing.T) {
		rec := recordFireSpans(t)
		repo := newFakeRepo()
		trigger := webhookTrigger(t, "")
		repo.triggers[trigger.ID] = trigger
		ev := pendingEvent(repo, trigger, "d1", nil)
		launcher := &fakeLauncher{err: &launch.ValidationError{Reason: "workflow wf-secret-name not found"}}

		_, err := NewEventFirer(repo, launcher).Fire(context.Background(), EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
		require.Error(t, err)

		s := oneFireSpan(t, rec, core.TriggerEventKindWebhook)
		a := fireAttrs(s)
		assert.Equal(t, string(core.TriggerEventFailed), a[telemetry.AttrTriggerOutcome])
		assert.Equal(t, ev.ID, a[telemetry.AttrTriggerEventID])
		assert.Equal(t, telemetry.TriggerReasonOther, a[telemetry.AttrTriggerReason])
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "failed", s.Status().Description)
		assertNoText(t, s, "wf-secret-name")
		assert.Empty(t, s.Links(), "no cause, no link")
	})
	t.Run("error before an outcome", func(t *testing.T) {
		rec := recordFireSpans(t)
		repo := newFakeRepo()
		trigger := webhookTrigger(t, "")
		repo.triggers[trigger.ID] = trigger
		ev := pendingEvent(repo, trigger, "d1", nil)
		repo.getTriggerErr = assert.AnError

		_, err := NewEventFirer(repo, &fakeLauncher{}).Fire(context.Background(), EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
		require.Error(t, err)

		s := oneFireSpan(t, rec, core.TriggerEventKindWebhook)
		assert.NotContains(t, fireAttrs(s), attribute.Key(telemetry.AttrTriggerOutcome), "retried: nothing recorded")
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "error", s.Status().Description)
		assertNoText(t, s, assert.AnError.Error())
	})
}

// Settled retries retain their recorded result, but still attribute their
// span when the event's trigger can be read. Missing triggers and read errors
// are telemetry-only: neither may launch again or fail the retry.
func TestFireSpan_SettledRetry(t *testing.T) {
	for _, kind := range []core.TriggerEventKind{core.TriggerEventKindSchedule, core.TriggerEventKindWebhook} {
		for _, outcome := range []core.TriggerEventOutcome{core.TriggerEventSkipped, core.TriggerEventFailed} {
			for _, lookup := range []string{"found", "deleted", "unavailable"} {
				t.Run(string(kind)+"/"+string(outcome)+"/"+lookup, func(t *testing.T) {
					rec := recordFireSpans(t)
					repo := newFakeRepo()
					trigger := webhookTrigger(t, "")
					trigger.OrgID = ptrTo("org-1")
					if lookup != "deleted" {
						repo.triggers[trigger.ID] = trigger
					}
					if lookup == "unavailable" {
						repo.getTriggerErr = assert.AnError
					}
					ev := pendingEvent(repo, trigger, "settled", nil)
					ev.Kind, ev.Outcome = kind, outcome
					ev.OutcomeDetail = "recorded result"
					launcher := &fakeLauncher{}
					ctx, cause := causeSpan(t)
					var out *FireOutput
					var err error
					if kind == core.TriggerEventKindSchedule {
						out, err = NewFirer(repo, launcher).Fire(ctx, FireRequest{TriggerID: trigger.ID, FireWorkflowID: ev.DedupeKey})
					} else {
						out, err = NewEventFirer(repo, launcher).Fire(ctx, EventFireInput{TriggerID: trigger.ID, Kind: kind, DedupeKey: ev.DedupeKey})
					}
					require.NoError(t, err)
					assert.Equal(t, outputFromEvent(ev), out)
					assert.Empty(t, launcher.snapshot())
					s := oneFireSpan(t, rec, kind)
					assertRootLinkedTo(t, s, cause)
					a := fireAttrs(s)
					assert.Equal(t, ev.ID, a[telemetry.AttrTriggerEventID])
					assert.Equal(t, string(outcome), a[telemetry.AttrTriggerOutcome])
					if lookup == "found" {
						assert.Equal(t, trigger.ID, a[telemetry.AttrTriggerID])
						assert.Equal(t, trigger.UserID, a[telemetry.AttrUserID])
						assert.Equal(t, "org-1", a[telemetry.AttrOrgID])
					} else {
						assert.Empty(t, a[telemetry.AttrTriggerID])
						assert.Empty(t, a[telemetry.AttrUserID])
						assert.Empty(t, a[telemetry.AttrOrgID])
					}
				})
			}
		}
	}
}

// An event the intake settles is a span; an event it hands to a fire is the
// fire's span, and a duplicate delivery records nothing.
func TestFireSpan_IntakeSettle(t *testing.T) {
	rec := recordFireSpans(t)
	repo := newFakeRepo()
	trigger := webhookTrigger(t, `trigger.payload.body == "deploy-me"`)
	trigger.OrgID = ptrTo("org-1")
	repo.triggers[trigger.ID] = trigger
	intake := NewIntake(repo, &recordingStarter{}, "")
	ctx, cause := causeSpan(t)

	skipped, err := intake.Accept(ctx, trigger, inbound(trigger, "miss", map[string]any{"body": "nope"}), AcceptOptions{})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventSkipped, skipped.Outcome)
	_, err = intake.Accept(ctx, trigger, inbound(trigger, "miss", map[string]any{"body": "nope"}), AcceptOptions{})
	require.NoError(t, err)
	pending, err := intake.Accept(ctx, trigger, inbound(trigger, "hit", map[string]any{"body": "deploy-me"}), AcceptOptions{})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, pending.Outcome)

	s := oneFireSpan(t, rec, core.TriggerEventKindWebhook)
	assertRootLinkedTo(t, s, cause)
	a := fireAttrs(s)
	assert.Equal(t, skipped.EventID, a[telemetry.AttrTriggerEventID])
	assert.Equal(t, string(core.TriggerEventSkipped), a[telemetry.AttrTriggerOutcome])
	assert.Equal(t, telemetry.TriggerReasonFilterNoMatch, a[telemetry.AttrTriggerReason])
	assert.Equal(t, "org-1", a[telemetry.AttrOrgID])
	assertNoText(t, s, "deploy-me", "nope")
}

// The redriver's own settles are spans, each naming its trigger when it still
// exists, and linked to the pass.
func TestFireSpan_RedriveSettles(t *testing.T) {
	rec := recordFireSpans(t)
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	trigger.OrgID = ptrTo("org-1")
	repo.triggers[trigger.ID] = trigger
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ancient := pendingEvent(repo, trigger, "ancient", nil)
	ancient.CreatedAt = now.Add(-48 * time.Hour)
	orphan := pendingEvent(repo, trigger, "orphan", nil)
	orphan.TriggerID = nil
	orphan.CreatedAt = now.Add(-5 * time.Minute)
	ctx, cause := causeSpan(t)

	redriver := NewRedriver(repo, &recordingStarter{}, "")
	redriver.now = func() time.Time { return now }
	require.NoError(t, redriver.RedriveOnce(ctx))

	byEvent := map[string]map[attribute.Key]string{}
	for _, s := range fireSpans(rec, core.TriggerEventKindWebhook) {
		assertRootLinkedTo(t, s, cause)
		byEvent[fireAttrs(s)[telemetry.AttrTriggerEventID]] = fireAttrs(s)
	}
	require.Len(t, byEvent, 2)
	assert.Equal(t, string(core.TriggerEventFailed), byEvent[ancient.ID][telemetry.AttrTriggerOutcome])
	assert.Equal(t, telemetry.TriggerReasonExpired, byEvent[ancient.ID][telemetry.AttrTriggerReason])
	assert.Equal(t, "org-1", byEvent[ancient.ID][telemetry.AttrOrgID], "read from the trigger")
	assert.Equal(t, string(core.TriggerEventSkipped), byEvent[orphan.ID][telemetry.AttrTriggerOutcome])
	assert.Equal(t, telemetry.TriggerReasonTriggerDeleted, byEvent[orphan.ID][telemetry.AttrTriggerReason])
	assert.NotContains(t, byEvent[orphan.ID], attribute.Key(telemetry.AttrOrgID), "no trigger, no org")
}

// The capacity expiry sweep settles in bulk; each event it settled is a span.
func TestFireSpan_CapacityExpirySweep(t *testing.T) {
	rec := recordFireSpans(t)
	triggerID := "activation"
	repo := &capacityExpiryRepo{capacityRedriveRepo: &capacityRedriveRepo{}, expired: []*core.TriggerEvent{
		{ID: "e1", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "k1", Outcome: core.TriggerEventFailed, OutcomeDetail: capacityExpiredDetail},
		{ID: "e2", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "k2", Outcome: core.TriggerEventFailed, OutcomeDetail: capacityExpiredDetail},
	}}

	require.NoError(t, NewRedriver(repo, &recordingStarter{}, "").RedriveOnce(context.Background()))

	spans := fireSpans(rec, core.TriggerEventKindIntegration)
	require.Len(t, spans, 2)
	for _, s := range spans {
		a := fireAttrs(s)
		assert.Equal(t, string(core.TriggerEventFailed), a[telemetry.AttrTriggerOutcome])
		assert.Equal(t, telemetry.TriggerReasonExpired, a[telemetry.AttrTriggerReason])
		assert.Equal(t, codes.Error, s.Status().Code)
	}
}

// traceSuperseded: one span per superseded event, each linked to the firing
// that superseded it and naming its own trigger.
func TestFireSpan_Superseded(t *testing.T) {
	rec := recordFireSpans(t)
	repo := newFakeRepo()
	firing := webhookTrigger(t, "")
	firing.OrgID = ptrTo("org-1")
	other := webhookTrigger(t, "")
	other.OrgID = ptrTo("org-1")
	repo.triggers[other.ID] = other
	ctx, span := StartFireSpan(context.Background(), core.TriggerEventKindWebhook)

	superseded := []*core.TriggerEvent{
		{ID: "s1", TriggerID: &firing.ID, Kind: core.TriggerEventKindWebhook, DedupeKey: "k1", Outcome: core.TriggerEventSkipped, OutcomeDetail: store.TriggerCapacitySuperseded},
		{ID: "s2", TriggerID: &other.ID, Kind: core.TriggerEventKindWebhook, DedupeKey: "k2", Outcome: core.TriggerEventSkipped, OutcomeDetail: store.TriggerCapacitySuperseded},
	}
	traceSuperseded(ctx, repo, firing, superseded)
	span.End(nil)

	spans := fireSpans(rec, core.TriggerEventKindWebhook)
	require.Len(t, spans, 3, "the firing and its two superseded events")
	triggerOf := map[string]string{}
	for _, s := range spans {
		a := fireAttrs(s)
		if a[telemetry.AttrTriggerEventID] == "" {
			continue // the firing itself
		}
		assertRootLinkedTo(t, s, span.span.SpanContext())
		assert.Equal(t, telemetry.TriggerReasonCapacitySuperseded, a[telemetry.AttrTriggerReason])
		assert.Equal(t, "org-1", a[telemetry.AttrOrgID])
		triggerOf[a[telemetry.AttrTriggerEventID]] = a[telemetry.AttrTriggerID]
	}
	assert.Equal(t, map[string]string{"s1": firing.ID, "s2": other.ID}, triggerOf)
}

// fireReason knows every detail a firing writes from a constant or a fixed
// prefix, and calls anything else "other", never its text.
func TestFireReason(t *testing.T) {
	for detail, want := range map[string]string{
		"":                                                "",
		"trigger deleted":                                 telemetry.TriggerReasonTriggerDeleted,
		"trigger is disabled":                             telemetry.TriggerReasonTriggerPaused,
		creatorLeftOrgPausedDetail:                        telemetry.TriggerReasonTriggerPaused,
		orgPolicyPausedDetail:                             telemetry.TriggerReasonTriggerPaused,
		noOrgAccountPausedDetail:                          telemetry.TriggerReasonTriggerPaused,
		projectDeletedPausedDetail:                        telemetry.TriggerReasonTriggerPaused,
		creatorLeftOrgNoPause:                             telemetry.TriggerReasonCreatorLeftOrg,
		noOrgAccountDetail("claude-x"):                    telemetry.TriggerReasonNoOrgAccount,
		capacityWaiting:                                   telemetry.TriggerReasonCapacityWaiting,
		store.TriggerCapacitySuperseded:                   telemetry.TriggerReasonCapacitySuperseded,
		capacityExpiredDetail:                             telemetry.TriggerReasonExpired,
		machinepool.WaitingPrefix + "gpu (x)":             telemetry.TriggerReasonPoolWaiting,
		"filter did not match: a == 1":                    telemetry.TriggerReasonFilterNoMatch,
		"filter did not match":                            telemetry.TriggerReasonFilterNoMatch,
		"the trigger's filter does not compile: x":        telemetry.TriggerReasonFilterError,
		"filter error: no such key":                       telemetry.TriggerReasonFilterError,
		originSkipReason(nil, "true"):                     telemetry.TriggerReasonOriginFilterRejected,
		"the trigger's origin_filter does not compile: x": telemetry.TriggerReasonOriginFilterError,
		"loop guard: this run descends from a run this trigger launched":       telemetry.TriggerReasonLoopGuard,
		"needs setup: repo is required":                                        telemetry.TriggerReasonNeedsSetup,
		RefusalDetail(&core.TriggerEventDefinition{Workflow: "w", Error: "e"}): telemetry.TriggerReasonDefinitionBroken,
		"trigger config is invalid: bad json":                                  telemetry.TriggerReasonConfigInvalid,
		"queued workflow capacity policy was removed":                          telemetry.TriggerReasonCapacityPolicyRemoved,
		"already launched":                      telemetry.TriggerReasonAlreadyLaunched,
		daemonMissingDetail:                     telemetry.TriggerReasonDaemonMissing,
		"something new about alice@example.com": telemetry.TriggerReasonOther,
	} {
		assert.Equal(t, want, fireReason(detail), detail)
	}
}
