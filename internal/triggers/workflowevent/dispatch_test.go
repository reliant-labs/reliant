// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// These tests drive Dispatch through the REAL launcher against a real
// database; only Temporal is stubbed. Each launched run's own terminal
// transition is simulated by writing its run event through runevents — the
// same write the WorkflowStatus activity performs — so a chain of triggers
// can be followed hop by hop.

type countingStarter struct {
	mu     sync.Mutex
	starts int
}

func (s *countingStarter) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{}) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, isRoot := args[0].(v2.WorkflowInput); isRoot {
		s.starts++
	}
	return &stubRun{id: options.ID, runID: "run-" + options.ID}, nil
}

func (s *countingStarter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

type stubRun struct {
	client.WorkflowRun
	id, runID string
}

func (r *stubRun) GetID() string    { return r.id }
func (r *stubRun) GetRunID() string { return r.runID }

type noopRunRecorder struct{}

func (noopRunRecorder) RecordRun(context.Context, string, string, string) {}

type fixture struct {
	t          *testing.T
	repo       *db.Repo
	starter    *countingStarter
	dispatcher *Dispatcher
	userID     string
	projectID  string
	daemonID   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a database; skipped under -short")
	}
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	f := &fixture{t: t, repo: repo, starter: &countingStarter{},
		userID: "we-user-" + uuid.NewString(), projectID: "we-project-" + uuid.NewString(), daemonID: uuid.NewString()}
	f.addUserProject(f.userID, f.projectID)
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: f.daemonID, UserID: f.userID}))

	launcher := launch.NewLauncher(repo, threads.NewService(repo), f.starter, noopRunRecorder{}, "test-queue", nil)
	f.dispatcher = NewDispatcher(repo, launcher, nil)
	return f
}

func (f *fixture) addUserProject(userID, projectID string) {
	f.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(f.t, f.repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Project", Path: f.t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(f.t, f.repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: f.t.TempDir(), Branch: "main", BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
}

// addWorkflow registers a runnable workflow under slug for userID, so a
// trigger may launch it.
func (f *fixture) addWorkflow(userID, slug string) {
	f.t.Helper()
	// A miss is (nil, nil), not an error.
	if existing, err := f.repo.GetUsableWorkflowBySlug(context.Background(), userID, slug); err == nil && existing != nil {
		return
	}
	now := time.Now().UTC()
	require.NoError(f.t, f.repo.CreateWorkflowDraft(context.Background(), &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: userID, Name: slug, Slug: slug,
		Definition: "name: " + slug + "\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n",
		Status:     db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))
}

// addTrigger stores a workflow_event trigger owned by userID that listens to
// src and launches `workflow`.
func (f *fixture) addTrigger(userID, projectID, name, workflow string, src Source) *core.Trigger {
	f.t.Helper()
	return f.addFilteredTrigger(userID, projectID, name, workflow, src, "")
}

// addFilteredTrigger is addTrigger with a CEL filter on the trigger.
func (f *fixture) addFilteredTrigger(userID, projectID, name, workflow string, src Source, filter string) *core.Trigger {
	f.t.Helper()
	if !strings.HasPrefix(workflow, "builtin://") {
		f.addWorkflow(userID, workflow)
	}
	config, err := json.Marshal(src)
	require.NoError(f.t, err)
	now := time.Now().UTC()
	tr := &core.Trigger{
		ID: uuid.NewString(), UserID: userID, ProjectID: projectID, Name: name,
		Kind: core.TriggerKindWorkflowEvent, Enabled: true, Workflow: workflow,
		Params:  map[string]any{"model": map[string]any{"id": "mock"}},
		Message: "react to the source run", DaemonID: f.daemonID, Config: config,
		Filter:    filter,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(f.t, f.repo.CreateTrigger(context.Background(), tr))
	return tr
}

// humanRun creates a chat a human started, running workflowName.
func (f *fixture) humanRun(workflowName string) string {
	f.t.Helper()
	ctx := context.Background()
	chatID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(f.t, f.repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: f.userID, ProjectID: f.projectID, Title: "human run",
		WorkflowName: &workflowName, WorkflowID: &chatID, State: db.ChatStateIdle,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return chatID
}

// finish records chatID's run reaching outcome — the outbox row the
// WorkflowStatus activity writes — and returns it.
func (f *fixture) finish(chatID string, outcome core.RunEventOutcome) *core.RunEvent {
	f.t.Helper()
	ctx := context.Background()
	chat, err := f.repo.GetChat(ctx, chatID)
	require.NoError(f.t, err)
	name := ""
	if chat.WorkflowName != nil {
		name = *chat.WorkflowName
	}
	created, err := runevents.EmitTerminal(ctx, f.repo, runevents.Terminal{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: name, RunID: "run-" + uuid.NewString(),
		Outcome: outcome, Summary: "all done",
	}, time.Now().UTC())
	require.NoError(f.t, err)
	require.True(f.t, created, "the run event must be written (owner has a workflow_event trigger)")
	evs, err := f.repo.ClaimRunEvents(ctx, time.Now().UTC(), time.Now().UTC().Add(time.Minute), 100)
	require.NoError(f.t, err)
	for _, ev := range evs {
		if ev.ChatID == chatID {
			return ev
		}
	}
	f.t.Fatalf("no run event recorded for %s", chatID)
	return nil
}

func launchedChat(t *testing.T, results []Result, triggerID string) string {
	t.Helper()
	for _, r := range results {
		if r.TriggerID == triggerID && r.Outcome == core.TriggerEventLaunched {
			require.NotEmpty(t, r.ChatID)
			return r.ChatID
		}
	}
	t.Fatalf("trigger %s did not launch: %+v", triggerID, results)
	return ""
}

func resultFor(t *testing.T, results []Result, triggerID string) Result {
	t.Helper()
	for _, r := range results {
		if r.TriggerID == triggerID {
			return r
		}
	}
	t.Fatalf("no verdict for trigger %s: %+v", triggerID, results)
	return Result{}
}

// The main use case: "when code-review finishes, run me". The launched run
// sees the source run under trigger.payload.
func TestDispatch_FinishedRunLaunchesTheMatchingTriggerWithTheSourceRunInItsPayload(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tr := f.addTrigger(f.userID, f.projectID, "on-review-done", "builtin://agent",
		Source{Workflows: []string{"code-review"}, Outcomes: []string{"finished"}})

	source := f.humanRun("code-review")
	ev := f.finish(source, core.RunEventFinished)

	results, err := f.dispatcher.Dispatch(ctx, ev)
	require.NoError(t, err)
	launched := launchedChat(t, results, tr.ID)
	assert.Equal(t, 1, f.starter.count())

	launchEv, err := f.repo.GetTriggerEventByChatID(ctx, launched)
	require.NoError(t, err)
	assert.Equal(t, runevents.TriggerEventKind, launchEv.Kind)
	assert.Equal(t, tr.ID, *launchEv.TriggerID)
	assert.Equal(t, source, launchEv.Payload["run_id"])
	assert.Equal(t, source, launchEv.Payload["chat_id"])
	assert.Equal(t, "code-review", launchEv.Payload["workflow_name"])
	assert.Equal(t, "finished", launchEv.Payload["outcome"])
	assert.Equal(t, "all done", launchEv.Payload["summary"])
	// Its sender is the workflow whose run finished, from this server's own
	// outbox: verified.
	assert.Equal(t, &core.TriggerSender{Kind: core.TriggerSenderKindWorkflow, ID: "code-review", DisplayName: "code-review", Verified: true},
		launchEv.Sender)

	// A retried dispatch launches nothing new.
	again, err := f.dispatcher.Dispatch(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, launched, launchedChat(t, again, tr.ID))
	assert.Equal(t, 1, f.starter.count(), "a retried dispatch must not start a second run")
}

// A workflow-event run is pinned to its trigger's daemon under an unattended
// launch event: what its preflight needs to wake that daemon with the stored
// token when nobody is signed in.
func TestDispatch_LaunchPinsTheTriggersDaemonUnderAnUnattendedLaunchEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tr := f.addTrigger(f.userID, f.projectID, "on-review-done", "builtin://agent",
		Source{Workflows: []string{"code-review"}, Outcomes: []string{"finished"}})

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFinished))
	require.NoError(t, err)
	launched := launchedChat(t, results, tr.ID)

	chat, err := f.repo.GetChat(ctx, launched)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, f.daemonID, *chat.ActiveDaemonID)
	assert.False(t, chat.NoMachine)

	launchEv, err := f.repo.GetTriggerEventByChatID(ctx, launched)
	require.NoError(t, err)
	assert.True(t, launchEv.Kind.Unattended(), "preflight may wake this run's daemon with the stored token")
}

// A no-machine workflow-event trigger launches a no-machine run, as every other
// trigger kind does. Before, the dispatcher dropped the choice: the run had no
// daemon pinned AND was not marked no-machine, so it fell back to the owner's
// default daemon — the machine its owner chose it should never touch — and
// preflight would wake it.
func TestDispatch_NoMachineTriggerLaunchesANoMachineRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tr := f.addTrigger(f.userID, f.projectID, "on-review-done", "builtin://agent",
		Source{Workflows: []string{"code-review"}, Outcomes: []string{"finished"}})
	tr.DaemonID, tr.NoMachine = "", true
	require.NoError(t, f.repo.UpdateTrigger(ctx, tr))

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFinished))
	require.NoError(t, err)
	launched := launchedChat(t, results, tr.ID)

	chat, err := f.repo.GetChat(ctx, launched)
	require.NoError(t, err)
	assert.True(t, chat.NoMachine, "the run has no machine, so preflight and tool time never resolve or wake one")
	assert.Nil(t, chat.ActiveDaemonID)
}

func TestDispatch_WorkflowAndOutcomeMismatchesAreNotRecorded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	otherWorkflow := f.addTrigger(f.userID, f.projectID, "other", "builtin://agent", Source{Workflows: []string{"deploy"}})
	onlyFailed := f.addTrigger(f.userID, f.projectID, "failed-only", "builtin://agent",
		Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})

	ev := f.finish(f.humanRun("code-review"), core.RunEventFinished)
	results, err := f.dispatcher.Dispatch(ctx, ev)
	require.NoError(t, err)
	assert.Empty(t, results)
	for _, tr := range []*core.Trigger{otherWorkflow, onlyFailed} {
		events, _, err := f.repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: f.userID, TriggerID: tr.ID, Limit: 10})
		require.NoError(t, err)
		assert.Empty(t, events, "a mismatched event is not this trigger's firing")
	}
	assert.Zero(t, f.starter.count())
}

func TestSource_EmptyListsMatchEverythingAndRefsCompareAsSlugs(t *testing.T) {
	src := Source{}
	for _, o := range []core.RunEventOutcome{core.RunEventFinished, core.RunEventFailed, core.RunEventBlocked} {
		assert.True(t, src.MatchesOutcome(o), "empty outcomes match %s", o)
	}
	assert.True(t, src.MatchesWorkflow("anything"))

	onlyBlocked := Source{Outcomes: []string{"Blocked"}}
	assert.True(t, onlyBlocked.MatchesOutcome(core.RunEventBlocked))
	assert.False(t, onlyBlocked.MatchesOutcome(core.RunEventFinished))

	assert.True(t, Source{Workflows: []string{"Code Review"}}.MatchesWorkflow("code-review"))
	assert.True(t, Source{Workflows: []string{"deploy", "code-review"}}.MatchesWorkflow("code-review"))
	assert.False(t, Source{Workflows: []string{"code-review"}}.MatchesWorkflow("deploy"))

	_, err := Source{Outcomes: []string{"done"}}.Validate()
	assert.Error(t, err)
	got, err := Source{Outcomes: []string{" Failed ", "failed"}, Workflows: []string{" ", "x"}}.Validate()
	require.NoError(t, err)
	assert.Equal(t, Source{Outcomes: []string{"failed"}, Workflows: []string{"x"}}, got)
}

func TestDispatch_CELFilterMissIsRecordedAsSkipped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tr := f.addFilteredTrigger(f.userID, f.projectID, "filtered", "builtin://agent",
		Source{Workflows: []string{"code-review"}}, `trigger.payload.summary.contains("LGTM")`)

	ev := f.finish(f.humanRun("code-review"), core.RunEventFinished)
	results, err := f.dispatcher.Dispatch(ctx, ev)
	require.NoError(t, err)
	got := resultFor(t, results, tr.ID)
	assert.Equal(t, core.TriggerEventSkipped, got.Outcome)
	assert.Equal(t, "filter did not match", got.Detail)
	assert.Zero(t, f.starter.count())
}

// The filter sees trigger.sender exactly as the launched run would: the
// finishing workflow.
func TestDispatch_SenderFilterReadsTheFinishingWorkflow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	fromReview := f.addFilteredTrigger(f.userID, f.projectID, "from-review", "builtin://agent",
		Source{}, `trigger.sender.verified && trigger.sender.id in ["code-review"]`)
	fromDeploy := f.addFilteredTrigger(f.userID, f.projectID, "from-deploy", "builtin://agent",
		Source{}, `trigger.sender.id == "deploy"`)

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFinished))
	require.NoError(t, err)
	launchedChat(t, results, fromReview.ID)
	skipped := resultFor(t, results, fromDeploy.ID)
	assert.Equal(t, core.TriggerEventSkipped, skipped.Outcome)
	events, _, err := f.repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: f.userID, TriggerID: fromDeploy.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "code-review", events[0].Event.Sender.ID, "a skipped firing records who it was from")
}

func TestDispatch_NeverFiresAnotherUsersTrigger(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	otherUser := "we-other-" + uuid.NewString()
	otherProject := "we-other-project-" + uuid.NewString()
	f.addUserProject(otherUser, otherProject)
	foreign := f.addTrigger(otherUser, otherProject, "snoop", "builtin://agent", Source{})
	// The owner needs a trigger of their own for the event to be emitted.
	f.addTrigger(f.userID, f.projectID, "own", "builtin://agent", Source{Workflows: []string{"unrelated"}})

	ev := f.finish(f.humanRun("code-review"), core.RunEventFinished)
	results, err := f.dispatcher.Dispatch(ctx, ev)
	require.NoError(t, err)
	for _, r := range results {
		assert.NotEqual(t, foreign.ID, r.TriggerID, "another user's trigger must never be considered")
	}
	assert.Zero(t, f.starter.count())
}

// A→A: a trigger on its own workflow fires once on the human's run, and never
// on the run it launched.
func TestDispatch_SelfLoopFiresOnceThenStops(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addTrigger(f.userID, f.projectID, "a-on-a", "code-review", Source{Workflows: []string{"code-review"}})

	first := f.finish(f.humanRun("code-review"), core.RunEventFinished)
	results, err := f.dispatcher.Dispatch(ctx, first)
	require.NoError(t, err)
	launched := launchedChat(t, results, a.ID)

	// The launched run is itself a code-review run; it finishing must not
	// launch another.
	second := f.finish(launched, core.RunEventFinished)
	results, err = f.dispatcher.Dispatch(ctx, second)
	require.NoError(t, err)
	got := resultFor(t, results, a.ID)
	assert.Equal(t, core.TriggerEventSkipped, got.Outcome)
	assert.Contains(t, got.Detail, "loop guard")
	assert.Equal(t, 1, f.starter.count(), "A→A must launch exactly once")
}

// A→B→A: two triggers that launch each other's workflow stop as soon as the
// chain would come back around.
func TestDispatch_TwoTriggerCycleStopsWhenItWouldComeBackAround(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	onA := f.addTrigger(f.userID, f.projectID, "a-then-b", "wf-b", Source{Workflows: []string{"wf-a"}})
	onB := f.addTrigger(f.userID, f.projectID, "b-then-a", "wf-a", Source{Workflows: []string{"wf-b"}})

	// Human runs A → onA launches B.
	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("wf-a"), core.RunEventFinished))
	require.NoError(t, err)
	runB := launchedChat(t, results, onA.ID)

	// B finishes → onB launches A (the chain is onA, onB so far).
	results, err = f.dispatcher.Dispatch(ctx, f.finish(runB, core.RunEventFinished))
	require.NoError(t, err)
	runA2 := launchedChat(t, results, onB.ID)

	// A (launched by the chain) finishes → onA must NOT fire again.
	results, err = f.dispatcher.Dispatch(ctx, f.finish(runA2, core.RunEventFinished))
	require.NoError(t, err)
	got := resultFor(t, results, onA.ID)
	assert.Equal(t, core.TriggerEventSkipped, got.Outcome)
	assert.Contains(t, got.Detail, "loop guard")
	assert.Equal(t, 2, f.starter.count(), "A→B→A must stop before closing the cycle")
}

// A chain of DISTINCT triggers (no cycle) is still bounded by MaxChainDepth.
func TestDispatch_ChainDepthIsCapped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const hops = MaxChainDepth + 2
	triggers := make([]*core.Trigger, hops)
	for i := 0; i < hops; i++ {
		triggers[i] = f.addTrigger(f.userID, f.projectID, "hop-"+string(rune('a'+i)),
			"wf-"+string(rune('a'+i+1)), Source{Workflows: []string{"wf-" + string(rune('a'+i))}})
	}

	run := f.humanRun("wf-a")
	for i := 0; i < MaxChainDepth; i++ {
		results, err := f.dispatcher.Dispatch(ctx, f.finish(run, core.RunEventFinished))
		require.NoError(t, err)
		run = launchedChat(t, results, triggers[i].ID)
	}
	results, err := f.dispatcher.Dispatch(ctx, f.finish(run, core.RunEventFinished))
	require.NoError(t, err)
	got := resultFor(t, results, triggers[MaxChainDepth].ID)
	assert.Equal(t, core.TriggerEventSkipped, got.Outcome)
	assert.Contains(t, got.Detail, "deep")
	assert.Equal(t, MaxChainDepth, f.starter.count())
}
