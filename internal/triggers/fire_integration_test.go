// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
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

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// These tests drive the Firer through the REAL launcher against a real
// database. The fakes in fire_test.go pin the fire's decisions; what they
// cannot show is how those decisions interact with the launcher's
// transaction, which is where the half-launch wedge and the overlap race live.

// flakyStarter fails root-workflow starts while down is set, standing in for
// Temporal being unreachable after the launch committed.
type flakyStarter struct {
	mu     sync.Mutex
	down   bool
	starts int
}

func (s *flakyStarter) setDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

func (s *flakyStarter) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, isRoot := args[0].(v2.WorkflowInput); isRoot {
		if s.down {
			return nil, errors.New("temporal unavailable")
		}
		s.starts++
	}
	return &stubRun{id: options.ID, runID: "run-" + options.ID}, nil
}

func (s *flakyStarter) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

type stubRun struct {
	client.WorkflowRun
	id    string
	runID string
}

func (r *stubRun) GetID() string    { return r.id }
func (r *stubRun) GetRunID() string { return r.runID }

type noopRunRecorder struct{}

func (noopRunRecorder) RecordRun(context.Context, string, string, string) {}

type fireFixture struct {
	repo    *db.Repo
	starter *flakyStarter
	firer   *Firer
	trigger *core.Trigger
}

func newFireFixture(t *testing.T, mutate func(*core.Trigger, *core.ScheduleConfig)) *fireFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a database; skipped under -short")
	}
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	userID := "fire-user-" + uuid.NewString()
	projectID := "fire-project-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Fire Project", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	trigger := testTrigger(t, func(tr *core.Trigger, cfg *core.ScheduleConfig) {
		tr.UserID = userID
		tr.ProjectID = projectID
		tr.DaemonID = daemonID
		tr.Presets = nil
		// The launch validates inputs, which need a resolvable model; "mock"
		// is the driver the launch tests use.
		tr.Params = map[string]any{"model": map[string]any{"id": "mock"}}
		if mutate != nil {
			mutate(tr, cfg)
		}
	})
	require.NoError(t, repo.CreateTrigger(ctx, trigger))

	starter := &flakyStarter{}
	launcher := launch.NewLauncher(repo, threads.NewService(repo), starter, noopRunRecorder{}, "test-queue")
	return &fireFixture{repo: repo, starter: starter, firer: NewFirer(repo, launcher), trigger: trigger}
}

func (f *fireFixture) fireAt(day int) FireRequest {
	return FireRequest{
		TriggerID:      f.trigger.ID,
		FireWorkflowID: FireWorkflowID(f.trigger.ID) + "-2026-01-0" + string(rune('0'+day)) + "T09:00:00Z",
		ScheduledAt:    time.Date(2026, 1, day, 9, 0, 0, 0, time.UTC),
	}
}

func (f *fireFixture) rootStatus(t *testing.T, chatID string) core.WorkflowStatus {
	t.Helper()
	statuses, err := f.repo.GetRootWorkflowStatusForChats(context.Background(), []string{chatID})
	require.NoError(t, err)
	return statuses[chatID]
}

// B1: Temporal fails after the launch committed, the activity retries, and the
// retry must RESUME its own half-launched fire. Before the fix the retry ran
// overlap against its own PENDING chat, skipped itself, and the next fire was
// then skipped behind that never-started chat, wedging the trigger for good.
func TestFireRetryResumesAHalfLaunchedFireInsteadOfWedgingTheTrigger(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()
	first := f.fireAt(2)

	f.starter.setDown(true)
	_, err := f.firer.Fire(ctx, first)
	require.Error(t, err, "the first attempt cannot start Temporal")

	// Attempts 2.. while Temporal is still down keep failing — and keep
	// leaving the one event row, never a recorded skip.
	_, err = f.firer.Fire(ctx, first)
	require.Error(t, err)

	f.starter.setDown(false)
	out, err := f.firer.Fire(ctx, first)
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), out.Outcome,
		"the retry must finish its own launch, not skip itself: %+v", out)
	require.NotEmpty(t, out.ChatID)
	assert.Equal(t, core.Active(), f.rootStatus(t, out.ChatID), "the resumed fire must have started its run")
	assert.Equal(t, 1, f.starter.startCount())

	stored, err := f.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, first.FireWorkflowID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventLaunched, stored.Outcome)
	events, _, err := f.repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: f.trigger.UserID, TriggerID: f.trigger.ID, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, events, 1, "retries must not accumulate event rows")

	// The retry of an already-launched fire is a no-op success.
	again, err := f.firer.Fire(ctx, first)
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), again.Outcome)
	assert.Equal(t, 1, f.starter.startCount(), "a completed fire must not start a second run")
}

// B1, the wedge itself: fire #1 is half-launched and never retried; fire #2
// must not be blocked behind a root that never started.
func TestFireIsNotBlockedByAPreviousFiresNeverStartedChat(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()

	f.starter.setDown(true)
	_, err := f.firer.Fire(ctx, f.fireAt(2))
	require.Error(t, err)

	f.starter.setDown(false)
	out, err := f.firer.Fire(ctx, f.fireAt(3))
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome,
		"a stranded PENDING predecessor must not wedge the trigger: %+v", out)
}

// B1: a retry of a fire that already recorded a skip returns that recorded
// outcome rather than re-deciding against whatever the world looks like now.
func TestFireRetryOfARecordedSkipReturnsTheRecordedOutcome(t *testing.T) {
	f := newFireFixture(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	ctx := context.Background()
	req := f.fireAt(2)

	first, err := f.firer.Fire(ctx, req)
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventSkipped), first.Outcome)

	// Enabling the trigger afterwards must not turn the already-skipped fire
	// into a launch: that fire's slot is spent.
	require.NoError(t, f.repo.SetTriggerEnabled(ctx, f.trigger.ID, true))
	retry, err := f.firer.Fire(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), retry.Outcome)
	assert.Equal(t, first.EventID, retry.EventID, "the retry must report the row that was written, not a fresh id")
	assert.Zero(t, f.starter.startCount())
}

// M2: overlap is decided inside the launch transaction under a row lock on the
// trigger. Fires released together after an outage must produce ONE run, with
// the others recorded as skipped.
func TestConcurrentFiresRespectOverlapSkip(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()

	const fires = 4
	outcomes := make([]*FireOutput, fires)
	errs := make([]error, fires)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < fires; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = f.firer.Fire(ctx, f.fireAt(i+2))
		}(i)
	}
	close(start)
	wg.Wait()

	launched, skipped := 0, 0
	for i := 0; i < fires; i++ {
		require.NoError(t, errs[i], "fire %d", i)
		switch outcomes[i].Outcome {
		case string(core.TriggerEventLaunched):
			launched++
		case string(core.TriggerEventSkipped):
			skipped++
		}
	}
	assert.Equal(t, 1, launched, "overlap=skip must let exactly one concurrent fire launch")
	assert.Equal(t, fires-1, skipped)
	assert.Equal(t, 1, f.starter.startCount())
}

// M3: a config the schedule parser rejects must leave a failed event row, so
// the owner can see why the trigger stopped producing runs.
func TestFireRecordsAFailedEventForABrokenConfig(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	trigger.Config = []byte(`{"cron": "not a list"}`)
	repo.triggers[trigger.ID] = trigger

	out, err := NewFirer(repo, &fakeLauncher{}).Fire(context.Background(), fireReq(trigger.ID))
	require.Error(t, err)
	assert.Nil(t, out)

	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1, "a broken config must be recorded, not silently dropped")
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, "config")
}

// M3: params that cannot be represented are a permanent, recorded failure.
func TestFireRecordsAFailedEventForUnrepresentableParams(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	trigger.Params = map[string]any{"bad": make(chan int)}
	repo.triggers[trigger.ID] = trigger

	_, err := NewFirer(repo, &fakeLauncher{}).Fire(context.Background(), fireReq(trigger.ID))
	require.Error(t, err)

	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, "params")
}

// A retried fire keeps the first attempt's fired_at: the event row is written
// once, by the attempt that launched, and the retry only finishes the start.
func TestFireRetryKeepsTheFirstFiredAt(t *testing.T) {
	f := newFireFixture(t, nil)
	ctx := context.Background()
	req := f.fireAt(2)

	first := req.ScheduledAt.Add(4 * time.Minute)
	f.firer.now = func() time.Time { return first }
	f.starter.setDown(true)
	_, err := f.firer.Fire(ctx, req)
	require.Error(t, err)

	f.starter.setDown(false)
	f.firer.now = func() time.Time { return first.Add(time.Hour) }
	out, err := f.firer.Fire(ctx, req)
	require.NoError(t, err)
	require.Equal(t, string(core.TriggerEventLaunched), out.Outcome)

	ev, err := f.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, req.FireWorkflowID)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02T09:04:00Z", ev.Payload["fired_at"])
	start, _ := ev.Payload["start"].(map[string]any)
	assert.NotEmpty(t, start["prompt"], "a schedule fire records its prompt too")
}
