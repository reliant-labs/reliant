// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/triggers/lineage"
)

// These drive Intake.Accept with a real declared workflow, a real enricher over
// a fake ledger, and the real filters: the whole decision "does this trigger want
// an event Reliant itself caused?", and what the firing history says about it.

// reviewWorkflow is pr-review: it reacts to pull request pushes. originFilter is
// spliced into its synchronize trigger.
func reviewWorkflow(originFilter string) string {
	line := ""
	if originFilter != "" {
		line = fmt.Sprintf("    origin_filter: %q\n", originFilter)
	}
	return `name: pr-review
triggers:
  - name: on-push
    integration:
      integration: github
      events: [pull_request.synchronize]
` + line + `entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
}

func prReviewActivation() *core.Trigger {
	name := "on-push"
	return &core.Trigger{
		ID: "trig-review", UserID: "user-1", ProjectID: "p1", Name: "pr-review", Workflow: "pr-review",
		Kind: core.TriggerKindIntegration, Enabled: true, WorkflowTrigger: &name,
		Config: []byte(`{"integration":"github","events":["pull_request.synchronize"]}`),
	}
}

// synchronizeEvent is the webhook for a push to a pull request's branch.
func synchronizeEvent(trigger *core.Trigger, delivery, headSHA string) InboundEvent {
	return InboundEvent{
		Kind:      core.TriggerEventKindIntegration,
		DedupeKey: trigger.ID + ":" + delivery,
		Payload:   map[string]any{"integration": "github", "event": "pull_request.synchronize", "data": map[string]any{"number": int64(7)}},
		Sender:    &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: "1", DisplayName: "reliant-01", Verified: true},
		Echo: &provenance.Echo{
			EventType: "pull_request.synchronize", Scope: "github:acme/app", Target: "github:acme/app#7",
			Correlation: provenance.Correlation{ObjectKind: provenance.KindPullRequest, Branch: "fix-save", HeadSHA: headSHA},
			ObjectID:    "7", SenderType: "User", SenderLogin: "reliant-01",
		},
	}
}

// pushWrite is the ledger entry a responder's git_publish leaves: a branch push.
func pushWrite(chat, workflow, triggerName, headSHA string) *core.IntegrationWrite {
	return &core.IntegrationWrite{
		ID: "w-push", UserID: "user-1", ChatID: chat, WorkflowName: workflow, TriggerName: triggerName,
		Integration: "github", Action: "pr.update", Actor: core.IntegrationActorApp,
		Scope: "github:acme/app", Target: "github:acme/app#7", ObjectID: "7",
		Correlation: provenance.Correlation{
			ObjectKind: provenance.KindPullRequest, Events: []string{"pull_request.synchronize"},
			Branch: "fix-save", HeadSHA: headSHA,
		}.Map(),
		Status: core.IntegrationWriteDone, CreatedAt: originNow.Add(-30_000_000_000),
	}
}

type originFixture struct {
	repo    *fakeRepo
	starter *recordingStarter
	intake  *Intake
	trigger *core.Trigger
	ledger  *fakeLedger
	runs    fakeRuns
}

func newOriginFixture(t *testing.T, workflows map[string]string) *originFixture {
	t.Helper()
	f := &originFixture{repo: newFakeRepo(), starter: &recordingStarter{}, trigger: prReviewActivation(), ledger: &fakeLedger{}}
	f.runs = fakeRuns{byChat: map[string]*core.TriggerEvent{}}
	f.repo.triggers[f.trigger.ID] = f.trigger
	enricher := newEnricher(f.ledger, f.runs, "reliant-labs")
	f.intake = NewIntake(f.repo, f.starter, "").
		WithWorkflows(fakeWorkflows{yaml: workflows}).
		WithEnrichers(enricher)
	return f
}

func (f *originFixture) accept(t *testing.T, ev InboundEvent) *AcceptResult {
	t.Helper()
	res, err := f.intake.Accept(context.Background(), f.trigger, ev, AcceptOptions{})
	require.NoError(t, err)
	return res
}

func (f *originFixture) row(t *testing.T, res *AcceptResult) *core.TriggerEvent {
	t.Helper()
	for _, ev := range f.repo.events {
		if ev.ID == res.EventID {
			return ev
		}
	}
	t.Fatalf("no row %s", res.EventID)
	return nil
}

// A responder (a different workflow of the same owner) pushes to the PR branch
// after pr-review reviewed it. The push's `synchronize` is pr-review's own
// subject, caused by someone else's run: it must fire.
func TestAnotherWorkflowsPushStillFiresTheDefault(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("")})
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-resp", "review-responder", "responder-push", "cccc")}

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventPending, res.Outcome, "the default drops only a workflow's OWN echoes")
	o := f.row(t, res).Origin
	require.NotNil(t, o)
	assert.True(t, o.CausedByReliant)
	assert.False(t, o.SameWorkflow)
	assert.Equal(t, "review-responder", o.Workflow)
}

// pr-review reviews; its own write comes back as an event it listens to.
func TestADefaultTriggerIgnoresItsOwnWorkflowsEchoAndSaysWhy(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("")})
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-review-1", "pr-review", "on-push", "cccc")}

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Contains(t, res.Detail, "origin_filter rejected")
	assert.Contains(t, res.Detail, "caused by run chat-review-1 of this workflow", "the history names the run")
	assert.Contains(t, res.Detail, "exact", "and how sure it is")
	assert.Empty(t, f.starter.snapshot(), "nothing launched")

	stored := f.row(t, res)
	require.NotNil(t, stored.Origin, "the evidence is frozen on the row, after the ledger is pruned")
	assert.Equal(t, "chat-review-1", stored.Origin.RunID)
	assert.Equal(t, core.TriggerEventSkipped, stored.Outcome)
}

func TestOriginFilterTrueReceivesEverythingIncludingItsOwnEchoes(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("true")})
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-review-1", "pr-review", "on-push", "cccc")}

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
	assert.True(t, f.row(t, res).Origin.SameWorkflow, "it is its own echo, and it asked for it")
	assert.Len(t, f.starter.snapshot(), 1)
}

func TestACustomOriginFilterAllowsOneDeliberateRetrigger(t *testing.T) {
	const expr = "!trigger.origin.same_workflow || trigger.origin.trigger_name == 'responder-push'"
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow(expr)})

	// Its own echo from the deliberate trigger: allowed.
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-1", "pr-review", "responder-push", "cccc")}
	allowed := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventPending, allowed.Outcome)

	// Its own echo from any other trigger: still ignored.
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-2", "pr-review", "on-push", "dddd")}
	blocked := f.accept(t, synchronizeEvent(f.trigger, "d2", "dddd"))
	assert.Equal(t, core.TriggerEventSkipped, blocked.Outcome)
	assert.Contains(t, blocked.Detail, expr, "the reason quotes the expression that decided")
}

func TestOriginFilterReadsEveryDocumentedField(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow(
		"trigger.origin.caused_by_reliant && trigger.origin.confidence == 'exact' && trigger.origin.run_id == 'chat-x' && " +
			"trigger.origin.workflow == 'review-responder' && trigger.origin.trigger_name == 'responder-push' && " +
			"trigger.origin.actor == 'app' && trigger.origin.lineage_depth == 0 && !trigger.origin.same_workflow")})
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-x", "review-responder", "responder-push", "cccc")}

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)
}

// Every key is always present, so a filter never trips on an event nothing
// attributed.
func TestOriginFieldsAreAlwaysPresentForAnUnattributedEvent(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow(
		"!trigger.origin.caused_by_reliant && trigger.origin.confidence == 'none' && trigger.origin.run_id == '' && " +
			"trigger.origin.workflow == '' && trigger.origin.trigger_name == '' && !trigger.origin.same_workflow && " +
			"trigger.origin.actor == 'unknown' && trigger.origin.lineage_depth == 0")})

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "zzzz"))
	assert.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)
}

func TestAnAppOriginIsNotAnOwnEchoSoTheDefaultLetsItThrough(t *testing.T) {
	// `app` knows Reliant did it, not which run: not provably this workflow's own.
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("")})
	ev := synchronizeEvent(f.trigger, "d1", "cccc")
	ev.Echo.SenderType, ev.Echo.SenderLogin = "Bot", "reliant-labs[bot]"

	res := f.accept(t, ev)
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
	assert.Equal(t, core.OriginApp, f.row(t, res).Origin.Confidence)
}

// ---- the hard loop guard -------------------------------------------------

// Two workflows that each react to the other's writes, with origin_filter "true"
// on both, would loop forever. The lineage cap stops them, and nothing the
// author writes reaches it.
func TestTheLineageCapStopsALoopingPairEvenWhenOriginFilterIsTrue(t *testing.T) {
	const a, b = "workflow-a", "workflow-b"
	wf := func(name string) string {
		return `name: ` + name + `
triggers:
  - name: on-push
    integration: {integration: github, events: [pull_request.synchronize]}
    origin_filter: "true"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	}
	ctx := context.Background()
	repo := newFakeRepo()
	starter := &recordingStarter{}
	ledger := &fakeLedger{}
	runs := fakeRuns{byChat: map[string]*core.TriggerEvent{}}
	intake := NewIntake(repo, starter, "").
		WithWorkflows(fakeWorkflows{yaml: map[string]string{a: wf(a), b: wf(b)}}).
		WithEnrichers(newEnricher(ledger, runs, "reliant-labs"))

	activation := func(id, workflow string) *core.Trigger {
		name := "on-push"
		return &core.Trigger{ID: id, UserID: "user-1", ProjectID: "p1", Name: workflow, Workflow: workflow,
			Kind: core.TriggerKindIntegration, Enabled: true, WorkflowTrigger: &name,
			Config: []byte(`{"integration":"github","events":["pull_request.synchronize"]}`)}
	}
	trigA, trigB := activation("trig-a", a), activation("trig-b", b)

	// The loop: A's run writes, B's trigger fires; B's run writes, A's fires; ...
	// Each launched run is recorded the way the launcher records one (its launch
	// event carries the origin it was fired with), so the next hop reads its depth.
	writer, who, next := "run-0", a, b
	launched := 0
	for hop := 1; hop <= 12; hop++ {
		sha := fmt.Sprintf("%040d", hop)
		ledger.writes = []*core.IntegrationWrite{pushWrite(writer, who, "on-push", sha)}
		target := trigB
		if next == a {
			target = trigA
		}
		res, err := intake.Accept(ctx, target, synchronizeEvent(target, fmt.Sprintf("d%d", hop), sha), AcceptOptions{})
		require.NoError(t, err)
		if res.Outcome != core.TriggerEventPending {
			require.Contains(t, res.Detail, "loop guard", "hop %d stopped for the wrong reason: %s", hop, res.Detail)
			break
		}
		launched++
		// The launched run becomes the next writer; its launch event is the row.
		ev, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, target.ID+":"+fmt.Sprintf("d%d", hop))
		require.NoError(t, err)
		chat := fmt.Sprintf("run-%d", hop)
		ev.ChatID = &chat
		ev.Outcome = core.TriggerEventLaunched
		runs.byChat[chat] = ev
		writer, who, next = chat, next, who
	}
	assert.Equal(t, lineage.MaxDepth, launched, "the loop ran exactly to the cap and no further")
	assert.Len(t, starter.snapshot(), lineage.MaxDepth)
}

func TestTheCapCountsAnotherUsersRunsToo(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("true")})
	w := pushWrite("their-chat", "their-workflow", "t", "cccc")
	w.UserID = "someone-else"
	f.ledger.writes = []*core.IntegrationWrite{w}
	deep := make(lineage.Lineage, lineage.MaxDepth)
	for i := range deep {
		deep[i] = lineage.Link{TriggerID: fmt.Sprintf("t%d", i)}
	}
	f.runs.byChat["their-chat"] = launchEventWithLineage(deep)

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Contains(t, res.Detail, "loop guard", "a cross-user chain is a chain")
	assert.NotContains(t, res.Detail, "their-chat", "and the reason does not name their run")
}

func TestAChainJustUnderTheCapStillFires(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("true")})
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-d", "other", "t", "cccc")}
	shallow := make(lineage.Lineage, lineage.MaxDepth-1)
	for i := range shallow {
		shallow[i] = lineage.Link{TriggerID: fmt.Sprintf("t%d", i)}
	}
	f.runs.byChat["chat-d"] = launchEventWithLineage(shallow)

	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)
}

// ---- validation and degradation ------------------------------------------

func TestABrokenOriginFilterIsRecordedAsFailedNotFiredOn(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("trigger.origin.nope == 1")})
	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	// The declaration fails to resolve (it does not compile against the root
	// as far as a missing key goes only at evaluation, so this is a failed row
	// either way), and nothing launches.
	assert.NotEqual(t, core.TriggerEventPending, res.Outcome)
	assert.Empty(t, f.starter.snapshot())
}

func TestOriginFilterThatDoesNotCompileBreaksTheActivation(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("trigger.origin.same_workflow &&")})
	res := f.accept(t, synchronizeEvent(f.trigger, "d1", "cccc"))
	assert.Equal(t, core.TriggerEventFailed, res.Outcome)
	assert.Contains(t, res.Detail, "origin_filter")
	assert.Empty(t, f.starter.snapshot())
}

func TestOriginFilterOnAScheduleIsRefused(t *testing.T) {
	yaml := `name: pr-review
triggers:
  - name: on-push
    schedule: {cron: "0 9 * * *"}
    origin_filter: "true"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	name := "on-push"
	tr := &core.Trigger{ID: "t", UserID: "u", Workflow: "pr-review", Kind: core.TriggerKindSchedule, WorkflowTrigger: &name}
	_, err := ResolveDeclaration(context.Background(), fakeWorkflows{yaml: map[string]string{"pr-review": yaml}}, tr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "origin_filter")
}

func TestManualRunNowBypassesTheOriginGuards(t *testing.T) {
	f := newOriginFixture(t, map[string]string{"pr-review": reviewWorkflow("")})
	f.ledger.writes = []*core.IntegrationWrite{pushWrite("chat-review-1", "pr-review", "on-push", "cccc")}
	res, err := f.intake.Accept(context.Background(), f.trigger, synchronizeEvent(f.trigger, "d1", "cccc"), AcceptOptions{Manual: true})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, res.Outcome, "a person pressing run now is deciding for themselves")
}

func TestEventsOfOtherKindsAreUnaffected(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "").WithEnrichers(newEnricher(&fakeLedger{}, fakeRuns{}, "reliant-labs"))

	res, err := intake.Accept(context.Background(), trigger, inbound(trigger, "d1", map[string]any{"body": "x"}), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
	assert.Nil(t, repo.events[0].Origin, "a webhook has no origin to record")
}
