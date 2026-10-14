// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/triggers/lineage"
)

// fakeLedger is an in-memory write ledger.
type fakeLedger struct {
	writes []*core.IntegrationWrite
	err    error
}

func (l *fakeLedger) GetIntegrationWriteByObject(_ context.Context, integration, scope, kind, id string) (*core.IntegrationWrite, error) {
	if l.err != nil {
		return nil, l.err
	}
	for _, w := range l.writes {
		if w.Integration == integration && w.Scope == scope && w.ObjectID == id &&
			w.Correlation["object_kind"] == kind && w.Status == core.IntegrationWriteDone {
			return w, nil
		}
	}
	return nil, core.ErrIntegrationWriteNotFound
}

func (l *fakeLedger) ListRecentIntegrationWrites(_ context.Context, integration, scope string, since time.Time, _ int) ([]*core.IntegrationWrite, error) {
	if l.err != nil {
		return nil, l.err
	}
	var out []*core.IntegrationWrite
	for _, w := range l.writes {
		if w.Integration != integration || w.Scope != scope || w.CreatedAt.Before(since) {
			continue
		}
		if w.Status == core.IntegrationWriteFailed && !w.Ambiguous {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

// fakeRuns hands back launch events by chat, for the lineage of a writing run.
type fakeRuns struct {
	byChat map[string]*core.TriggerEvent
	err    error
}

func (r fakeRuns) GetTriggerEventByChatID(_ context.Context, chatID string) (*core.TriggerEvent, error) {
	if r.err != nil {
		return nil, r.err
	}
	if ev, ok := r.byChat[chatID]; ok {
		return ev, nil
	}
	return nil, core.ErrTriggerEventNotFound
}

var originNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func newEnricher(ledger LedgerReader, runs RunLineage, appSlug string) *OriginEnricher {
	e := NewOriginEnricher(ledger, runs, appSlug)
	e.now = func() time.Time { return originNow }
	return e
}

func commentWrite(id, user, chat, workflow, objectID, body string) *core.IntegrationWrite {
	return &core.IntegrationWrite{
		ID: id, UserID: user, ChatID: chat, WorkflowName: workflow, TriggerName: "responder-push",
		Integration: "github", Action: "issue.comment", Actor: core.IntegrationActorApp,
		Scope: "github:acme/app", Target: "github:acme/app#7", ObjectID: objectID,
		Correlation: provenance.Correlation{
			ObjectKind: provenance.KindComment, Events: []string{"issue_comment.created"},
			BodySHA256: provenance.BodyDigest(body),
		}.Map(),
		Status: core.IntegrationWriteDone, CreatedAt: originNow.Add(-time.Minute),
	}
}

func commentEvent(commentID, body string) InboundEvent {
	return InboundEvent{
		Kind:    core.TriggerEventKindIntegration,
		Payload: map[string]any{"integration": "github", "event": "issue_comment.created"},
		Echo: &provenance.Echo{
			EventType: "issue_comment.created", Scope: "github:acme/app", Target: "github:acme/app#7",
			Correlation: provenance.Correlation{ObjectKind: provenance.KindComment, BodySHA256: provenance.BodyDigest(body)},
			ObjectID:    commentID, SenderType: "User", SenderLogin: "reliant-01",
		},
	}
}

func ownTrigger(workflow string) *core.Trigger {
	return &core.Trigger{ID: "trig-1", UserID: "user-1", Workflow: workflow, Kind: core.TriggerKindIntegration}
}

func enrich(t *testing.T, e *OriginEnricher, trigger *core.Trigger, ev InboundEvent) *core.TriggerOrigin {
	t.Helper()
	out, err := e.Enrich(context.Background(), trigger, ev)
	require.NoError(t, err)
	return out.Origin
}

func TestAnEventWhoseObjectIDTheLedgerRecordedIsAnExactMatch(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "Looks good")
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{}, "reliant-labs")

	o := enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "different body entirely"))
	require.NotNil(t, o)
	assert.True(t, o.CausedByReliant)
	assert.Equal(t, core.OriginExact, o.Confidence, "the id decides, whatever the body says")
	assert.Equal(t, "chat-9", o.RunID)
	assert.Equal(t, "pr-review", o.Workflow)
	assert.Equal(t, "responder-push", o.TriggerName)
	assert.True(t, o.SameWorkflow)
	assert.Equal(t, core.OriginActorApp, o.Actor)
	assert.Equal(t, "w1", o.WriteID)
}

func TestAnExactMatchOnTheWrongKindOfEventIsNotOurs(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "Looks good")
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{}, "")

	// A review with the same number is not our comment.
	ev := commentEvent("4242", "x")
	ev.Echo.EventType = "pull_request_review.submitted"
	ev.Echo.ObjectKind = provenance.KindReview
	o := enrich(t, e, ownTrigger("pr-review"), ev)
	assert.False(t, o.CausedByReliant)
	assert.Equal(t, core.OriginNone, o.Confidence)
}

func TestAnEventTheWebhookBeatsTheResponseToIsAnIntentMatch(t *testing.T) {
	// The ledger row is still an intent: the id is not known yet.
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "", "Looks good")
	w.Status = core.IntegrationWriteIntent
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{}, "")

	o := enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "Looks good"))
	assert.True(t, o.CausedByReliant)
	assert.Equal(t, core.OriginIntent, o.Confidence)
	assert.True(t, o.SameWorkflow)
}

func TestIntentNeedsContentToAgree(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "", "Looks good")
	w.Status = core.IntegrationWriteIntent
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{}, "")

	// Same place, same kind of event, different words: a person, not us.
	o := enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "I disagree"))
	assert.False(t, o.CausedByReliant)

	// Right words, wrong pull request.
	ev := commentEvent("4242", "Looks good")
	ev.Echo.Target = "github:acme/app#8"
	assert.False(t, enrich(t, e, ownTrigger("pr-review"), ev).CausedByReliant)

	// The window has passed: a person saying the same thing an hour later.
	w.CreatedAt = originNow.Add(-time.Hour)
	assert.False(t, enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "Looks good")).CausedByReliant)
}

func TestAWriteThatProvablyChangedNothingCausedNothing(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "", "Looks good")
	w.Status, w.Ambiguous = core.IntegrationWriteFailed, false // a 422
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{}, "")
	assert.False(t, enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "Looks good")).CausedByReliant)

	w.Ambiguous = true // a timeout: GitHub may have applied it
	assert.True(t, enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "Looks good")).CausedByReliant)
}

func TestTheAppsOwnObjectsAreAttributedToItWithoutARun(t *testing.T) {
	e := newEnricher(&fakeLedger{}, fakeRuns{}, "Reliant-Labs")

	ev := commentEvent("1", "x")
	ev.Echo.AppSlug = "reliant-labs"
	o := enrich(t, e, ownTrigger("pr-review"), ev)
	assert.True(t, o.CausedByReliant)
	assert.Equal(t, core.OriginApp, o.Confidence)
	assert.Equal(t, core.OriginActorApp, o.Actor)
	assert.Empty(t, o.RunID, "the app tier knows Reliant did it, not which run")
	assert.False(t, o.SameWorkflow, "so it can never be the workflow's own echo")

	ev = commentEvent("2", "x")
	ev.Echo.SenderType, ev.Echo.SenderLogin = "Bot", "reliant-labs[bot]"
	assert.Equal(t, core.OriginApp, enrich(t, e, ownTrigger("pr-review"), ev).Confidence)

	// Another bot is not us.
	ev = commentEvent("3", "x")
	ev.Echo.SenderType, ev.Echo.SenderLogin = "Bot", "dependabot[bot]"
	assert.False(t, enrich(t, e, ownTrigger("pr-review"), ev).CausedByReliant)

	// Without a configured app, nothing is attributed to it.
	none := newEnricher(&fakeLedger{}, fakeRuns{}, "")
	ev = commentEvent("4", "x")
	ev.Echo.AppSlug = "reliant-labs"
	assert.False(t, enrich(t, none, ownTrigger("pr-review"), ev).CausedByReliant)
}

func TestARunTrailerIsAClaimAndOnlyAClaim(t *testing.T) {
	e := newEnricher(&fakeLedger{}, fakeRuns{}, "reliant-labs")
	ev := InboundEvent{Kind: core.TriggerEventKindIntegration, Payload: map[string]any{"integration": "github"},
		Echo: &provenance.Echo{EventType: "push", Scope: "github:acme/app", RunTrailer: "6e1c0b1e"}}

	o := enrich(t, e, ownTrigger("pr-review"), ev)
	assert.True(t, o.CausedByReliant)
	assert.Equal(t, core.OriginClaimed, o.Confidence)
	assert.Empty(t, o.RunID, "a claim carries no run identity: anyone can write a trailer")
	assert.False(t, o.SameWorkflow)
	assert.Equal(t, core.OriginActorUnknown, o.Actor)
}

func TestAnotherUsersRunIsNotRevealedButItsDepthStillCounts(t *testing.T) {
	w := commentWrite("w1", "someone-else", "their-chat", "their-workflow", "4242", "x")
	l := lineage.Lineage{{TriggerID: "a", SourceRunID: "r1"}, {TriggerID: "b", SourceRunID: "r2"}}
	runs := fakeRuns{byChat: map[string]*core.TriggerEvent{"their-chat": launchEventWithLineage(l)}}
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, runs, "")

	o := enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "x"))
	assert.True(t, o.CausedByReliant)
	assert.True(t, o.Foreign)
	assert.Empty(t, o.RunID)
	assert.Empty(t, o.Workflow)
	assert.Empty(t, o.TriggerName)
	assert.Empty(t, o.WriteID)
	assert.False(t, o.SameWorkflow)
	assert.Empty(t, o.Lineage, "whose triggers chained is not ours to know")
	assert.Equal(t, 2, o.LineageDepth(), "but a cross-user loop is still a loop, so the depth is counted")
}

func TestSameWorkflowComparesTheWayTheLauncherDoes(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "PR Review", "4242", "x")
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{}, "")

	assert.True(t, enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "x")).SameWorkflow, "a display name and its slug are one workflow")
	assert.False(t, enrich(t, e, ownTrigger("review-responder"), commentEvent("4242", "x")).SameWorkflow)
	assert.False(t, enrich(t, e, ownTrigger(""), commentEvent("4242", "x")).SameWorkflow, "an unnamed workflow is not the same as anything")
}

func TestTheWritingRunsLineageRidesOnTheOrigin(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "x")
	l := lineage.Lineage{{TriggerID: "t-a", SourceRunID: "r1"}}
	runs := fakeRuns{byChat: map[string]*core.TriggerEvent{"chat-9": launchEventWithLineage(l)}}
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, runs, "")

	o := enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "x"))
	assert.Equal(t, 1, o.LineageDepth())
	assert.Equal(t, "t-a", o.Lineage[0].TriggerID)
}

func TestAnUnreadableLineageIsTreatedAsAtTheCapNeverAsZero(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "x")
	e := newEnricher(&fakeLedger{writes: []*core.IntegrationWrite{w}}, fakeRuns{err: errors.New("db down")}, "")

	o := enrich(t, e, ownTrigger("pr-review"), commentEvent("4242", "x"))
	assert.Equal(t, lineage.MaxDepth, o.LineageDepth(), "an unknown depth must never let a chain grow")
}

func TestALedgerOutageDegradesToNotOursAndNeverFailsTheEvent(t *testing.T) {
	e := newEnricher(&fakeLedger{err: errors.New("db down")}, fakeRuns{}, "reliant-labs")
	out, err := e.Enrich(context.Background(), ownTrigger("pr-review"), commentEvent("4242", "x"))
	require.NoError(t, err)
	assert.False(t, out.Origin.CausedByReliant)
}

func TestEventsWithoutAnEchoAreNotLookedAt(t *testing.T) {
	e := newEnricher(&fakeLedger{}, fakeRuns{}, "reliant-labs")
	out, err := e.Enrich(context.Background(), ownTrigger("w"), InboundEvent{Kind: core.TriggerEventKindWebhook})
	require.NoError(t, err)
	assert.Nil(t, out.Origin, "no origin is not the same as an origin of none")

	var nilEnricher *OriginEnricher
	out, err = nilEnricher.Enrich(context.Background(), ownTrigger("w"), commentEvent("1", "x"))
	require.NoError(t, err)
	assert.Nil(t, out.Origin)
}

func launchEventWithLineage(l lineage.Lineage) *core.TriggerEvent {
	trigger := "t-last"
	return &core.TriggerEvent{
		Kind: core.TriggerEventKindWorkflowEvent, TriggerID: &trigger,
		Payload: map[string]any{lineage.PayloadKey: l.ToPayload()},
	}
}
