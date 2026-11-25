package triggers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// stoppingRuns is the run lifecycle as the firer sees it: Terminate closes the
// chain Temporal reports for the chat (the cooperative cancel and its finally:
// are the run service's, proven in internal/runs and the scenario runner's
// TestFinally_RunsWhenTheRunIsCancelled), and writes no run_status, as a
// hard-terminated run does not, so the firer's own settle is exercised.
type stoppingRuns struct {
	starter *describedStarter
	mu      sync.Mutex
	stopped []string
}

func (r *stoppingRuns) Terminate(_ context.Context, chatID string) error {
	r.mu.Lock()
	r.stopped = append(r.stopped, chatID)
	r.mu.Unlock()
	r.starter.mu.Lock()
	defer r.starter.mu.Unlock()
	if desc := r.starter.descriptions[chatID]; desc != nil {
		desc.WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_CANCELED
	}
	return nil
}

func (r *stoppingRuns) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stopped...)
}

func cancelLauncher(f *fireFixture, starter *describedStarter) *launch.Launcher {
	return launch.New(launch.Deps{Store: f.repo, Chats: store.NewChatsWithActivityExtStore(f.repo.ORM()), Workflows: store.NewWorkflowExtStore(f.repo.ORM()), Worktrees: store.NewWorktreeExtStore(f.repo.ORM()), ProjectRows: store.NewProjectExtStore(f.repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(f.repo.ORM()), Projects: projectaccess.NewFromORM(f.repo.ORM()), Selections: store.NewChatSelectionExtStore(f.repo.ORM()), Threads: threads.New(threads.Deps{Repo: f.repo, Messages: store.NewMessageExtStore(f.repo.ORM()), Senders: store.NewThreadExtStore(f.repo.ORM())}), Temporal: starter, Runs: noopRunRecorder{}, TaskQueue: "test-queue"})
}

// cancelScheduleFixture is a schedule trigger whose every firing shares one
// key, capped at one running run per key.
func cancelScheduleFixture(t *testing.T, mode reliantv1.WorkflowConcurrencyCancelRunning) (*fireFixture, *describedStarter, *stoppingRuns) {
	f := newFireFixture(t, nil)
	starter := &describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}
	runs := &stoppingRuns{starter: starter}
	policy := &reliantv1.WorkflowConcurrency{
		MaxRunningPerKey: 1, Key: "{{ trigger.payload.trigger_name }}",
		Waiting: reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST, CancelRunning: mode,
	}
	f.firer = NewFirer(NewStores(f.repo), cancelLauncher(f, starter)).WithWorkflows(capacityResolver{wf: &reliantv1.Workflow{Concurrency: policy}}).
		WithCapacityDescriptions(starter).WithRunCanceller(runs)
	return f, starter, runs
}

// cancelRow is what cancel_running leaves on a firing's row.
type cancelRow struct {
	Outcome   core.TriggerEventOutcome `bun:"outcome"`
	Detail    string                   `bun:"outcome_detail"`
	RunStatus string                   `bun:"run_status"`
	Released  bool                     `bun:"capacity_released"`
	ChatID    string                   `bun:"chat_id"`
}

func (f *fireFixture) cancelRow(t *testing.T, id string) cancelRow {
	t.Helper()
	var row cancelRow
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT outcome, outcome_detail, COALESCE(run_status, '') AS run_status, capacity_released, COALESCE(chat_id, '') AS chat_id
FROM trigger_events WHERE id = ?`, id).Scan(context.Background(), &row))
	return row
}

// on_new_key_event: a newer firing of the key cancels the running run through
// the run lifecycle, records which firing superseded it, settles its firing
// cancelled, and takes the slot in the same fire.
func TestCancelRunningOnNewKeyEventSupersedesTheRunningRun(t *testing.T) {
	f, _, runs := cancelScheduleFixture(t, reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT)
	ctx := context.Background()

	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
	require.Empty(t, runs.calls(), "the first firing has nothing to supersede")

	second, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), second.Outcome, "the cancelled run's slot is freed and the new firing takes it")
	require.Equal(t, []string{first.ChatID}, runs.calls())

	superseded := f.cancelRow(t, first.EventID)
	require.Equal(t, core.TriggerEventLaunched, superseded.Outcome)
	require.Equal(t, store.TriggerCapacitySupersededPrefix+second.EventID, superseded.Detail)
	require.Equal(t, core.TriggerRunCancelled, superseded.RunStatus)
	require.True(t, superseded.Released, "the slot is released on Temporal's word that the chain closed")

	// A redelivered fire of the new firing cancels nothing more.
	again, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), again.Outcome)
	require.Len(t, runs.calls(), 1)
}

// Unset (and never): the running run is never touched; the firing waits.
func TestCancelRunningUnsetLeavesTheRunningRunAlone(t *testing.T) {
	for _, mode := range []reliantv1.WorkflowConcurrencyCancelRunning{
		reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_UNSPECIFIED,
		reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_NEVER,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			f, starter, runs := cancelScheduleFixture(t, mode)
			ctx := context.Background()
			first, err := f.firer.Fire(ctx, f.fireAt(2))
			require.NoError(t, err)
			require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)
			second, err := f.firer.Fire(ctx, f.fireAt(3))
			require.NoError(t, err)
			require.Equal(t, string(core.TriggerEventPending), second.Outcome)
			require.Empty(t, runs.calls())
			running := f.cancelRow(t, first.EventID)
			require.Empty(t, running.Detail)
			require.Empty(t, running.RunStatus)

			starter.complete(first.ChatID)
			second, err = f.firer.Fire(ctx, f.fireAt(3))
			require.NoError(t, err)
			require.Equal(t, string(core.TriggerEventLaunched), second.Outcome, "it runs once the key's run ends by itself")
		})
	}
}

// A later chain on the chat's workflow id is a person's own turn: it is never
// cancelled for an automation's firing.
func TestCancelRunningNeverCancelsALaterChainOnTheChat(t *testing.T) {
	f, starter, runs := cancelScheduleFixture(t, reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT)
	ctx := context.Background()
	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)

	// The admitted chain is still open in this test's view of the slot, but
	// Temporal reports a different chain under the workflow id.
	starter.mu.Lock()
	info := starter.descriptions[first.ChatID].WorkflowExecutionInfo
	info.FirstRunId = "a-later-turn"
	starter.mu.Unlock()

	second, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	require.Empty(t, runs.calls(), "a person's turn is not the slot's run")
	require.Equal(t, string(core.TriggerEventLaunched), second.Outcome, "a different chain proves the admitted one closed; its slot frees as before")
	require.Empty(t, f.cancelRow(t, first.EventID).Detail)
}

// Two firings of the key arrive together while a run holds it. Neither may
// cancel the other's newer work, and the slot is never held twice: when both
// fires (and one later fire of the newest, the redriver's) are done, the
// newest firing's run alone holds the key, and the original run is cancelled.
func TestCancelRunningConcurrentFiringsHandOffTheSlotOnce(t *testing.T) {
	f, starter, runs := cancelScheduleFixture(t, reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT)
	ctx := context.Background()
	first, err := f.firer.Fire(ctx, f.fireAt(2))
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, day := range []int{3, 4} {
		wg.Add(1)
		go func(i, day int) {
			defer wg.Done()
			_, errs[i] = f.firer.Fire(ctx, f.fireAt(day))
		}(i, day)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	a, b := f.eventOf(t, 3), f.eventOf(t, 4)
	newest, older := a, b
	if *b.CapacityOrder > *a.CapacityOrder {
		newest, older = b, a
	}
	// The redriver's pass: the newest firing's next fire finishes any hand-off
	// the race left (a run whose start was not yet visible to cancel).
	day := 3
	if newest.ID == b.ID {
		day = 4
	}
	_, err = f.firer.Fire(ctx, f.fireAt(day))
	require.NoError(t, err)

	require.Equal(t, core.TriggerRunCancelled, f.cancelRow(t, first.EventID).RunStatus)
	require.Equal(t, core.TriggerEventLaunched, f.eventOf(t, day).Outcome, "the newest firing runs")
	switch olderNow := f.cancelRow(t, older.ID); olderNow.Outcome {
	case core.TriggerEventSkipped:
		require.Equal(t, store.TriggerCapacitySuperseded, olderNow.Detail, "superseded while it waited")
	case core.TriggerEventLaunched:
		require.Equal(t, store.TriggerCapacitySupersededPrefix+newest.ID, olderNow.Detail, "it launched first, and the newest cancelled it")
		require.Equal(t, core.TriggerRunCancelled, olderNow.RunStatus)
	default:
		t.Fatalf("the older firing is %s; it must be superseded or cancelled", olderNow.Outcome)
	}

	occupied, err := NewCapacityStores(f.repo.ORM()).ListTriggerCapacityExecutions(ctx, store.TriggerCapacityScope{UserID: f.trigger.UserID, ProjectID: f.trigger.ProjectID, WorkflowRef: workflowref.RefKey(f.trigger.Workflow)})
	require.NoError(t, err)
	live := 0
	for _, o := range occupied {
		starter.mu.Lock()
		desc := starter.descriptions[o.WorkflowID]
		running := desc != nil && desc.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_RUNNING
		starter.mu.Unlock()
		if running {
			live++
			require.Equal(t, newest.ID, o.EventID)
		}
	}
	require.Equal(t, 1, live, "one running run holds the key")
	for _, chat := range runs.calls() {
		require.NotEqual(t, f.cancelRow(t, newest.ID).ChatID, chat, "the newest firing's run is never cancelled")
	}
}

// on_new_head compares the GitHub pull request head of the firing with the
// running run's: the same head, or a firing with no head (a comment), never
// cancels; a moved head does.
func TestCancelRunningOnNewHeadCancelsOnlyForAMovedHead(t *testing.T) {
	f := newFireFixture(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Kind = core.TriggerKindWebhook })
	f.trigger.Config = json.RawMessage(`{}`)
	ctx := context.Background()
	require.NoError(t, triggertest.Update(ctx, f.repo, f.trigger))
	starter := &describedStarter{flakyStarter: f.starter, descriptions: map[string]*workflowservice.DescribeWorkflowExecutionResponse{}}
	runs := &stoppingRuns{starter: starter}
	policy := &reliantv1.WorkflowConcurrency{
		MaxRunningPerKey: 1, Key: "{{ trigger.payload.data.number }}",
		Waiting:       reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST,
		CancelRunning: reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD,
	}
	firer := NewEventFirer(NewStores(f.repo), cancelLauncher(f, starter)).WithWorkflows(capacityResolver{wf: &reliantv1.Workflow{Concurrency: policy}}).
		WithCapacityDescriptions(starter).WithRunCanceller(runs)
	intake := NewIntake(NewStores(f.repo), &recordingStarter{}, "test-queue")
	fire := func(key string, data map[string]any) *FireOutput {
		t.Helper()
		ev := inbound(f.trigger, key, map[string]any{"data": data})
		accepted, err := intake.Accept(ctx, f.trigger, ev, AcceptOptions{})
		require.NoError(t, err)
		require.Equal(t, core.TriggerEventPending, accepted.Outcome, accepted.Detail)
		out, err := firer.Fire(ctx, EventFireInput{TriggerID: f.trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
		require.NoError(t, err)
		return out
	}
	push := func(sha string) map[string]any {
		return map[string]any{"number": 7, "pull_request": map[string]any{"number": 7, "head": map[string]any{"sha": sha}}}
	}

	first := fire("opened", push("aaaa"))
	require.Equal(t, string(core.TriggerEventLaunched), first.Outcome)

	same := fire("ready", push("aaaa"))
	require.Equal(t, string(core.TriggerEventPending), same.Outcome, "the same head is no reason to stop")
	comment := fire("comment", map[string]any{"number": 7, "comment": map[string]any{"body": "lgtm?"}})
	require.Equal(t, string(core.TriggerEventPending), comment.Outcome, "a firing with no head never cancels")
	require.Empty(t, runs.calls())

	moved := fire("synchronize", push("bbbb"))
	require.Equal(t, string(core.TriggerEventLaunched), moved.Outcome, "a moved head cancels the stale run and takes the slot")
	require.Equal(t, []string{first.ChatID}, runs.calls())
	stale := f.cancelRow(t, first.EventID)
	require.Equal(t, store.TriggerCapacitySupersededPrefix+moved.EventID, stale.Detail)
	require.Equal(t, core.TriggerRunCancelled, stale.RunStatus)
	require.Equal(t, core.TriggerEventSkipped, f.cancelRow(t, same.EventID).Outcome, "waiting: latest still supersedes the waiting firings")
	require.Equal(t, core.TriggerEventSkipped, f.cancelRow(t, comment.EventID).Outcome)

	// A comment on the moved head waits behind its run; it never cancels.
	again := fire("comment-2", map[string]any{"number": 7, "comment": map[string]any{"body": "and now?"}})
	require.Equal(t, string(core.TriggerEventPending), again.Outcome)
	require.Len(t, runs.calls(), 1)
}

func TestPullRequestHead(t *testing.T) {
	for name, tc := range map[string]struct {
		payload map[string]any
		want    string
	}{
		"pull_request":  {map[string]any{"data": map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": "abc"}}}}, "abc"},
		"issue_comment": {map[string]any{"data": map[string]any{"issue": map[string]any{"number": 1}}}, ""},
		"no data":       {map[string]any{"scheduled_for": "x"}, ""},
		"sha not text":  {map[string]any{"data": map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": 12}}}}, ""},
		"nil":           {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, pullRequestHead(tc.payload))
		})
	}
}
