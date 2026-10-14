// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/threads"
)

// The scenario this whole change exists for, end to end, with nothing faked but
// GitHub and Temporal: a pr-review run posts a review through the ledger'd
// runner; a review-responder run pushes a fix; the push's `synchronize` webhook,
// parsed from a real GitHub payload, arrives at pr-review's trigger.
//
// By default the synchronize is NOT pr-review's own echo (the responder pushed,
// not pr-review), so it fires; but pr-review's own comment, echoing back, is
// skipped, and the Automations history says so.

type scenario struct {
	repo     *db.Repo
	starter  *flakyStarter
	intake   *Intake
	firer    *EventFirer
	recorder writeledger.Service
	trigger  *core.Trigger
	userID   string
	chats    map[string]string
	// nextWriterChat, when set, is a chat that already exists (a run the
	// scenario launched for real): the next write is made as that run instead
	// of as a fresh one.
	nextWriterChat string
}

func newScenario(t *testing.T, workflow string) *scenario {
	t.Helper()
	f := newFireFixture(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.Kind = core.TriggerKindIntegration
		tr.Workflow = scenarioWorkflow
		tr.Name = "pr-review on push"
		wt := "on-push"
		tr.WorkflowTrigger = &wt
		tr.Config = json.RawMessage(`{"integration":"github","events":["pull_request.synchronize","issue_comment.created"]}`)
	})
	ctx := context.Background()

	launcher := launch.NewLauncher(f.repo, threads.NewService(f.repo), f.starter, noopRunRecorder{}, "test-queue")
	workflows := launchableDeclarations{fakeWorkflows: fakeWorkflows{yaml: map[string]string{scenarioWorkflow: workflow}}}
	enricher := NewOriginEnricher(f.repo.IntegrationWrites(), f.repo, "reliant-labs")
	starter := &recordingStarter{}
	intake := NewIntake(f.repo, starter, "").WithWorkflows(workflows).WithEnrichers(enricher)
	firer := NewEventFirer(f.repo, launcher).WithWorkflows(workflows)

	require.NoError(t, f.repo.UpdateTrigger(ctx, f.trigger))
	return &scenario{
		repo: f.repo, starter: f.starter, intake: intake, firer: firer, trigger: f.trigger, userID: f.trigger.UserID,
		recorder: writeledger.New(writeledger.Deps{Store: f.repo.IntegrationWrites(), Runs: f.repo}),
		chats:    map[string]string{},
	}
}

// writeAsRun makes an integration call as if from a run of workflow, through
// the real recorder over a fake HTTP runner, and returns the run's chat id.
func (s *scenario) writeAsRun(t *testing.T, workflow, action string, params map[string]any, result map[string]any) string {
	t.Helper()
	ctx := context.Background()
	chatID := s.nextWriterChat
	s.nextWriterChat = ""
	if chatID == "" {
		chatID = uuid.NewString()
		wfID := uuid.NewString()
		now := time.Now().UTC()
		name := workflow
		require.NoError(t, s.repo.CreateChat(ctx, &db.Chat{
			ID: chatID, Title: workflow, ProjectID: s.trigger.ProjectID, UserID: s.userID, WorkflowName: &name, WorkflowID: &wfID,
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}
	runner := writeledgerRunner{result: &httpaction.Result{StatusCode: 200, Data: result}}
	_, err := s.recorder.Wrap(runner).RunAuthenticated(ctx, githubIntegration(), mutatingAction(action), params, nil, httpaction.CallSite{RunID: chatID})
	require.NoError(t, err)
	s.chats[workflow] = chatID
	return chatID
}

func (s *scenario) deliver(t *testing.T, event, fixture, delivery string, tweak func(map[string]any)) *AcceptResult {
	t.Helper()
	body := fixtureBody(t, fixture)
	if tweak != nil {
		var m map[string]any
		require.NoError(t, json.Unmarshal(body, &m))
		tweak(m)
		var err error
		body, err = json.Marshal(m)
		require.NoError(t, err)
	}
	parsed, err := github.Parse(event, body, time.Now())
	require.NoError(t, err)
	require.Len(t, parsed.Events, 1)
	ev := parsed.Events[0]
	attrs := make(map[string]any, len(ev.Attributes))
	for k, v := range ev.Attributes {
		attrs[k] = v
	}
	res, err := s.intake.Accept(context.Background(), s.trigger, InboundEvent{
		Kind: core.TriggerEventKindIntegration, DedupeKey: s.trigger.ID + ":" + delivery, OccurredAt: ev.OccurredAt,
		Payload: map[string]any{"integration": "github", "event": ev.Type, "account": ev.AccountKey, "delivery_id": delivery, "attributes": attrs, "data": ev.Data},
		Sender:  ev.Sender, Echo: ev.Echo,
	}, AcceptOptions{})
	require.NoError(t, err)
	return res
}

// scenarioWorkflow is the workflow the activation runs. It is a builtin so the
// real launcher can start it; the TRIGGER declarations under test are served in
// its place by launchableDeclarations, and "writer" runs are named for it.
const scenarioWorkflow = "builtin://agent"

// launchableDeclarations serves a test's declaring YAML for the activation's
// workflow ref and nothing else: the launcher reads the real builtin.
type launchableDeclarations struct{ fakeWorkflows }

const defaultOriginWorkflow = `name: pr-review
triggers:
  - name: on-push
    integration:
      integration: github
      events: [pull_request.synchronize, issue_comment.created]
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func TestPRReviewResponderPushSynchronizeIsSkippedWhenItsOwnAndRecordedWhy(t *testing.T) {
	s := newScenario(t, defaultOriginWorkflow)
	ctx := context.Background()

	// 1. pr-review reviews pull request #7 (a comment on the conversation).
	reviewText := "**Review**\n\nOne nit in client.go."
	reviewRun := s.writeAsRun(t, scenarioWorkflow, "issue.comment",
		map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(7), "body": reviewText},
		map[string]any{"id": int64(555), "html_url": "https://github.com/acme/app/pull/7#issuecomment-555"})

	// 2. The review comes back as an issue_comment.created webhook, parsed from a
	// real payload. It is pr-review's OWN echo: skipped, with the run named.
	echo := s.deliver(t, "issue_comment", "issue_comment.created", "d-comment", func(m map[string]any) {
		c := m["comment"].(map[string]any)
		c["id"] = 555
		c["body"] = reviewText
		m["issue"].(map[string]any)["number"] = 7
	})
	assert.Equal(t, core.TriggerEventSkipped, echo.Outcome, "pr-review does not react to its own review")
	assert.Contains(t, echo.Detail, "origin_filter rejected")
	assert.Contains(t, echo.Detail, "caused by run "+reviewRun+" of this workflow")

	// The reason and the evidence are on the stored row, which is what the
	// Automations history reads.
	row, err := s.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, s.trigger.ID+":d-comment")
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, row.Outcome)
	assert.Equal(t, echo.Detail, row.OutcomeDetail)
	require.NotNil(t, row.Origin)
	assert.True(t, row.Origin.CausedByReliant)
	assert.Equal(t, core.OriginExact, row.Origin.Confidence, "matched by the comment's own id")
	assert.Equal(t, reviewRun, row.Origin.RunID)
	assert.True(t, row.Origin.SameWorkflow)

	// 3. A review-responder run (ANOTHER workflow of the same owner) pushes a fix
	// to the PR branch. The push is machine-side work that git_publish records in
	// the ledger as an intent carrying the head sha it is about to produce; until
	// that node exists the scenario records the same entry directly.
	respRun := s.recordPush(t, "review-responder", "responder-push", "fix-save", "cccccccccccccccccccccccccccccccccccccccc")

	// 4. The push's `synchronize` arrives. The responder caused it, not pr-review,
	// so by default it is NOT pr-review's own echo and it fires.
	sync := s.deliver(t, "pull_request", "pull_request.synchronize", "d-sync", nil)
	assert.Equal(t, core.TriggerEventPending, sync.Outcome, "another workflow's push is something pr-review wants: %s", sync.Detail)
	syncRow, err := s.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, s.trigger.ID+":d-sync")
	require.NoError(t, err)
	require.NotNil(t, syncRow.Origin)
	assert.Equal(t, core.OriginIntent, syncRow.Origin.Confidence, "the push had no object id yet: matched by target, branch and head sha")
	assert.False(t, syncRow.Origin.SameWorkflow)

	// 5. And the launched run carries the origin it was fired with.
	fired, err := s.firer.Fire(ctx, EventFireInput{TriggerID: s.trigger.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: s.trigger.ID + ":d-sync"})
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), fired.Outcome, fired.Reason)
	launchedRow, err := s.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, s.trigger.ID+":d-sync")
	require.NoError(t, err)
	require.NotNil(t, launchedRow.Origin, "the origin survives the launcher adopting the pending row")
	assert.Equal(t, respRun, launchedRow.Origin.RunID)
	assert.Equal(t, "review-responder", launchedRow.Origin.Workflow)
	assert.False(t, launchedRow.Origin.SameWorkflow)
	assert.Equal(t, core.TriggerEventLaunched, launchedRow.Outcome)

	info := launch.TriggerInfoFromEvent(launchedRow)
	cel := info.CELValue()["origin"].(map[string]interface{})
	assert.Equal(t, true, cel["caused_by_reliant"], "the run's own `trigger.origin` says who caused it")
	assert.Equal(t, "review-responder", cel["workflow"])
}

func TestOriginFilterTrueLetsItsOwnEchoThroughEndToEnd(t *testing.T) {
	const wf = `name: pr-review
triggers:
  - name: on-push
    integration:
      integration: github
      events: [pull_request.synchronize, issue_comment.created]
    origin_filter: "true"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	s := newScenario(t, wf)
	s.writeAsRun(t, scenarioWorkflow, "issue.comment",
		map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(7), "body": "hello"},
		map[string]any{"id": int64(556)})
	res := s.deliver(t, "issue_comment", "issue_comment.created", "d-1", func(m map[string]any) {
		m["comment"].(map[string]any)["id"] = 556
		m["comment"].(map[string]any)["body"] = "hello"
		m["issue"].(map[string]any)["number"] = 7
	})
	assert.Equal(t, core.TriggerEventPending, res.Outcome, res.Detail)
}

func TestTheOriginIsReadBackFromTheDatabaseIntact(t *testing.T) {
	s := newScenario(t, defaultOriginWorkflow)
	ctx := context.Background()
	want := &core.TriggerOrigin{
		CausedByReliant: true, Confidence: core.OriginIntent, RunID: "r", Workflow: "w", TriggerName: "tn",
		SameWorkflow: true, Actor: core.OriginActorApp, Depth: 2, WriteID: "wid",
		Lineage: []core.OriginLink{{TriggerID: "t1", SourceRunID: "r1"}, {TriggerID: "t2", SourceRunID: "r2"}},
	}
	key := "round-trip"
	created, err := s.repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &s.trigger.ID, UserID: s.userID, Kind: core.TriggerEventKindIntegration, DedupeKey: key,
		OccurredAt: time.Now().UTC(), Payload: map[string]any{}, Outcome: core.TriggerEventPending, CreatedAt: time.Now().UTC(), Origin: want,
	})
	require.NoError(t, err)
	require.True(t, created)
	got, err := s.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, key)
	require.NoError(t, err)
	assert.Equal(t, want, got.Origin)

	// An event with no origin reads back as no origin, not a zero one.
	_, err = s.repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &s.trigger.ID, UserID: s.userID, Kind: core.TriggerEventKindIntegration, DedupeKey: "none",
		OccurredAt: time.Now().UTC(), Payload: map[string]any{}, Outcome: core.TriggerEventPending, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	got, err = s.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, "none")
	require.NoError(t, err)
	assert.Nil(t, got.Origin)
}

func TestACrossWorkflowLoopThroughTheRealLedgerStopsAtTheCap(t *testing.T) {
	const wfA = `name: pr-review
triggers:
  - name: on-push
    integration: {integration: github, events: [issue_comment.created]}
    origin_filter: "true"
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`
	s := newScenario(t, wfA)
	ctx := context.Background()

	launched := 0
	writer := scenarioWorkflow
	for hop := 1; hop <= 12; hop++ {
		body := fmt.Sprintf("hop %d", hop)
		commentID := int64(9000 + hop)
		s.writeAsRun(t, writer, "issue.comment",
			map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(7), "body": body},
			map[string]any{"id": commentID})
		delivery := fmt.Sprintf("d-%d", hop)
		res := s.deliver(t, "issue_comment", "issue_comment.created", delivery, func(m map[string]any) {
			m["comment"].(map[string]any)["id"] = commentID
			m["comment"].(map[string]any)["body"] = body
			m["issue"].(map[string]any)["number"] = 7
		})
		if res.Outcome != core.TriggerEventPending {
			assert.Contains(t, res.Detail, "loop guard", "hop %d: %s", hop, res.Detail)
			break
		}
		// Launch it for real, so the NEXT hop's writer has a launch event whose
		// origin the lineage reads.
		out, err := s.firer.Fire(ctx, EventFireInput{TriggerID: s.trigger.ID, Kind: core.TriggerEventKindIntegration, DedupeKey: s.trigger.ID + ":" + delivery})
		require.NoError(t, err)
		require.Equal(t, string(core.TriggerEventLaunched), out.Outcome, out.Reason)
		launched++
		// The launched run is a run of the same workflow, and it writes next.
		s.nextWriterChat = out.ChatID
	}
	assert.Equal(t, 5, launched, "origin_filter is true, so only the lineage cap stops the loop")
}

// writeledgerRunner stands in for GitHub: it answers every call with result.
type writeledgerRunner struct{ result *httpaction.Result }

func (r writeledgerRunner) RunAuthenticated(context.Context, *reliantv1.IntegrationManifest, *reliantv1.ActionSpec, map[string]any, httpaction.CredentialSource, httpaction.CallSite) (*httpaction.Result, error) {
	return r.result, nil
}

func githubIntegration() *reliantv1.IntegrationManifest {
	return &reliantv1.IntegrationManifest{Id: "github"}
}

func mutatingAction(id string) *reliantv1.ActionSpec {
	return &reliantv1.ActionSpec{Id: id, Mutates: true}
}

// fixtureBody reads a GitHub webhook fixture the parser's own tests use.
func fixtureBody(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "integrations", "webhook", "github", "testdata", name+".json"))
	require.NoError(t, err)
	return b
}

// recordPush records the ledger entry a branch push leaves, as a run of
// workflow, and returns that run's chat id.
func (s *scenario) recordPush(t *testing.T, workflow, triggerName, branch, headSHA string) string {
	t.Helper()
	ctx := context.Background()
	chatID, wfID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	name := workflow
	require.NoError(t, s.repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: workflow, ProjectID: s.trigger.ProjectID, UserID: s.userID, WorkflowName: &name, WorkflowID: &wfID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, s.repo.IntegrationWrites().CreateIntegrationWrite(ctx, &core.IntegrationWrite{
		ID: uuid.NewString(), UserID: s.userID, ChatID: chatID, WorkflowName: workflow, TriggerName: triggerName,
		Integration: "github", Action: "git.publish", Actor: core.IntegrationActorApp,
		Scope: "github:acme/app", Target: "github:acme/app#7",
		Correlation: provenance.Correlation{
			ObjectKind: provenance.KindPullRequest, Events: []string{"pull_request.synchronize"},
			Branch: branch, HeadSHA: headSHA,
		}.Map(),
		Status: core.IntegrationWriteIntent, CreatedAt: now,
	}))
	return chatID
}
