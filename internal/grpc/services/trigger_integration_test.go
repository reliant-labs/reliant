// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/triggers"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

// The whole path, with nothing faked but the agent itself: a trigger created
// through the real TriggerService, converged onto a real Temporal Schedule,
// fired, launched by the real launch.Launcher, producing a real chat row whose
// root workflow Temporal has actually started.
//
// Every piece has unit tests; this is the one that would have caught a wiring
// mistake between them — a task queue mismatch, an unregistered activity, a
// launcher whose deps were nil.
func TestTriggerFiresEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}

	repo := db.NewTestRepo(t)
	ctx := context.Background()

	temporalClient := startTriggerDevServer(t)

	// The fire workflow and the chat's root workflow run on this queue. It is
	// test-scoped so a parallel run cannot steal our fires.
	taskQueue := "trigger-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(
		repo,
		temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)),
		taskQueue,
		nil, // no daemon prober: triggers never request a greenfield probe
	)

	w := worker.New(temporalClient, taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(triggers.TriggerFireWorkflow,
		workflow.RegisterOptions{Name: triggers.FireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewFirer(repo, launcher).Fire,
		activity.RegisterOptions{Name: triggers.FireActivityName})
	// A stand-in for the agent: the real DynamicWorkflow needs the whole
	// activity registry, and what this test is proving is that the run was
	// STARTED, not what the agent then did.
	w.RegisterWorkflowWithOptions(
		func(ctx workflow.Context, _ any) error { return nil },
		workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic},
	)
	require.NoError(t, w.Start())
	defer w.Stop()

	// The real service, over the real schedule and workflow clients.
	backend := triggers.NewBackend(temporalClient.ScheduleClient(), temporalClient, repo, taskQueue)
	svc := NewTriggerService(repo, backend, backend)

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Trigger E2E", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	createMainWorktree(t, repo, projectID, now)
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, userID)

	// A real 2s interval. The 1m floor exists because a whole agent run cannot
	// finish faster; the stand-in workflow returns immediately.
	restore := triggers.SetMinIntervalForTest(time.Second)
	defer restore()

	every := "2s"
	created, err := svc.CreateTrigger(authCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: &reliantv1.TriggerDefinition{
			Name:      "e2e sweep",
			ProjectId: projectID,
			DaemonId:  daemonID,
			Workflow:  "builtin://agent",
			Message:   "Sweep the dependency tree.",
			// The run's model comes from the trigger's params, which is the
			// real path — a trigger with no resolvable model is correctly
			// rejected as a validation failure, so the fixture has to name
			// one. "mock" is the driver the launch tests use.
			Params: mockModelTriggerParams(t),
			Source: &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{
				Interval: &every,
				// ALLOW, so a slow first run cannot make every later fire a
				// skip and leave this test waiting on nothing.
				Overlap: reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW,
			}},
		},
	}))
	require.NoError(t, err)
	triggerID := created.Msg.GetTrigger().GetId()
	t.Cleanup(func() { _ = backend.Delete(context.Background(), triggerID) })

	// Wait for a LAUNCHED event. The event row is the durable record of
	// intent, so its existence is the assertion — not a log line or a timer.
	launchedOutcome := core.TriggerEventLaunched
	var launched *core.TriggerEvent
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := repo.GetLatestTriggerEvent(ctx, triggerID, &launchedOutcome)
		if err == nil && ev != nil {
			launched = ev
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.NotNil(t, launched, "no launched trigger event within 90s")

	// The dedupe key is the fire workflow id, which carries the scheduled
	// time — that is what makes each scheduled time launch exactly once.
	assert.Contains(t, launched.DedupeKey, triggers.FireWorkflowID(triggerID))
	require.NotNil(t, launched.ChatID, "a launched event must name the chat it started")

	// The real launcher created a real chat, owned by the TRIGGER's user.
	chat, err := repo.GetChat(ctx, *launched.ChatID)
	require.NoError(t, err)
	assert.Equal(t, userID, chat.UserID, "the run executes as the trigger's owner")
	assert.Equal(t, projectID, chat.ProjectID)
	assert.Contains(t, chat.Title, "e2e sweep", "the title carries the trigger's name")

	// The chat id IS the deterministic id the fire derived, so a retried fire
	// would resolve to this same chat rather than a second one.
	assert.Equal(t, *launched.ChatID, chat.ID)

	// And Temporal actually started the root run — the step that turns a row
	// into a running session.
	require.NotNil(t, chat.WorkflowID, "the chat must record its root workflow id")
	desc, err := temporalClient.DescribeWorkflowExecution(ctx, *chat.WorkflowID, "")
	require.NoError(t, err, "the chat's root workflow was never started in Temporal")
	assert.Equal(t, *chat.WorkflowID, desc.GetWorkflowExecutionInfo().GetExecution().GetWorkflowId())

	// The seed messages reached the thread: the prompt, plus the hidden note
	// telling the agent nobody is watching.
	messages, err := repo.ListMessages(ctx, chat.ID, core.MessageListOptions{})
	require.NoError(t, err)
	var sawPrompt, sawUnattendedNote bool
	for _, m := range messages {
		// Content lives in blocks, not on the message row.
		blocks, err := repo.ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil && *b.Content == "Sweep the dependency tree." {
				sawPrompt = true
			}
		}
		if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM &&
			m.DisplayStyle != nil && *m.DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			sawUnattendedNote = true
		}
	}
	assert.True(t, sawPrompt, "the trigger's prompt must seed the run")
	assert.True(t, sawUnattendedNote, "the hidden unattended note must seed the run")
}

// TestCronTriggerFiresEndToEnd is the case the feature exists for: a real
// 5-field cron expression, not an interval, firing through the real schedule
// into the real launcher. Interval and cron reach Temporal through different
// ScheduleSpec fields (Intervals vs CronExpressions → Calendars), so the
// interval test above does not prove a cron schedule ever fires.
//
// Cron resolution is one minute, so this waits for the next minute boundary —
// up to ~75s. Gated behind REQUIRE_CRON_E2E so the default suite stays fast;
// the interval test covers the shared wiring on every run.
func TestCronTriggerFiresEndToEnd(t *testing.T) {
	if testing.Short() || os.Getenv("REQUIRE_CRON_E2E") == "" {
		t.Skip("waits for a real cron minute boundary; set REQUIRE_CRON_E2E=1 to run")
	}

	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-cron-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(repo, temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)),
		taskQueue, nil)

	w := worker.New(temporalClient, taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(triggers.TriggerFireWorkflow,
		workflow.RegisterOptions{Name: triggers.FireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewFirer(repo, launcher).Fire,
		activity.RegisterOptions{Name: triggers.FireActivityName})
	w.RegisterWorkflowWithOptions(
		func(ctx workflow.Context, _ any) error { return nil },
		workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic},
	)
	require.NoError(t, w.Start())
	defer w.Stop()

	backend := triggers.NewBackend(temporalClient.ScheduleClient(), temporalClient, repo, taskQueue)
	svc := NewTriggerService(repo, backend, backend)

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Cron E2E", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	createMainWorktree(t, repo, projectID, now)
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, userID)

	created, err := svc.CreateTrigger(authCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: &reliantv1.TriggerDefinition{
			Name:      "every minute",
			ProjectId: projectID,
			DaemonId:  daemonID,
			Workflow:  "builtin://agent",
			Message:   "Check the build.",
			Params:    mockModelTriggerParams(t),
			Source: &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{
				Cron:     []string{"* * * * *"},
				Timezone: "America/New_York",
			}},
		},
	}))
	require.NoError(t, err)
	triggerID := created.Msg.GetTrigger().GetId()
	t.Cleanup(func() { _ = backend.Delete(context.Background(), triggerID) })

	// The schedule reports where it will fire next, which is what the UI shows.
	require.NotNil(t, created.Msg.GetTrigger().NextFireAt, "an enabled cron trigger must report its next fire time")
	next, err := time.Parse(time.RFC3339, created.Msg.GetTrigger().GetNextFireAt())
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), next, 61*time.Second, "an every-minute cron fires within the next minute")
	assert.Zero(t, next.Second(), "a cron fire lands on a minute boundary")

	launchedOutcome := core.TriggerEventLaunched
	var launched *core.TriggerEvent
	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := repo.GetLatestTriggerEvent(ctx, triggerID, &launchedOutcome)
		if err == nil && ev != nil {
			launched = ev
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NotNil(t, launched, "the cron schedule never launched a run")
	assert.Zero(t, launched.OccurredAt.Second(),
		"the event records the SCHEDULED time, which a cron puts on a minute boundary")
	require.NotNil(t, launched.ChatID)

	chat, err := repo.GetChat(ctx, *launched.ChatID)
	require.NoError(t, err)
	require.NotNil(t, chat.WorkflowID)
	_, err = temporalClient.DescribeWorkflowExecution(ctx, *chat.WorkflowID, "")
	require.NoError(t, err, "the chat's root workflow was never started in Temporal")
}

// A manual fire through the real service reaches the same path, which is what
// "run now" must do for a trigger that is paused.
func TestManualFireEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}

	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-manual-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(repo, temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)),
		taskQueue, nil)

	w := worker.New(temporalClient, taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(triggers.TriggerFireWorkflow,
		workflow.RegisterOptions{Name: triggers.FireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewFirer(repo, launcher).Fire,
		activity.RegisterOptions{Name: triggers.FireActivityName})
	w.RegisterWorkflowWithOptions(
		func(ctx workflow.Context, _ any) error { return nil },
		workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic},
	)
	require.NoError(t, w.Start())
	defer w.Stop()

	backend := triggers.NewBackend(temporalClient.ScheduleClient(), temporalClient, repo, taskQueue)
	svc := NewTriggerService(repo, backend, backend)

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Manual E2E", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	createMainWorktree(t, repo, projectID, now)
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, userID)

	// Created DISABLED, and fired anyway: the manual path must bypass the
	// enabled check, which is the case someone testing a trigger before
	// turning it on actually hits.
	disabled := false
	created, err := svc.CreateTrigger(authCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: &reliantv1.TriggerDefinition{
			Name:      "manual only",
			ProjectId: projectID,
			DaemonId:  daemonID,
			Enabled:   &disabled,
			Workflow:  "builtin://agent",
			Message:   "Run once, on request.",
			Params:    mockModelTriggerParams(t),
			Source: &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{
				Cron: []string{"0 9 * * *"},
			}},
		},
	}))
	require.NoError(t, err)
	triggerID := created.Msg.GetTrigger().GetId()
	require.False(t, created.Msg.GetTrigger().GetEnabled())
	t.Cleanup(func() { _ = backend.Delete(context.Background(), triggerID) })

	fired, err := svc.FireTrigger(authCtx,
		connect.NewRequest(&reliantv1.FireTriggerRequest{Id: triggerID}))
	require.NoError(t, err)
	fireID := fired.Msg.GetFireWorkflowId()
	assert.Contains(t, fireID, "manual")

	launchedOutcome := core.TriggerEventLaunched
	var launched *core.TriggerEvent
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := repo.GetLatestTriggerEvent(ctx, triggerID, &launchedOutcome)
		if err == nil && ev != nil {
			launched = ev
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.NotNil(t, launched, "a manual fire of a DISABLED trigger must still launch")
	assert.Equal(t, fireID, launched.DedupeKey)
	assert.Equal(t, true, launched.Payload["manual"])
}

// mockModelTriggerParams names the mock LLM driver, mirroring
// internal/launch's own fixture.
func mockModelTriggerParams(t *testing.T) map[string]*structpb.Value {
	t.Helper()
	value, err := structpb.NewValue(map[string]any{"id": "mock"})
	require.NoError(t, err)
	return map[string]*structpb.Value{"model": value}
}

// createMainWorktree gives the project the main worktree every chat needs.
//
// launch.Launcher refuses to create a chat in a project with no main worktree
// (internal/launch/session.go), so a project row alone is not a usable fixture
// — a trigger against one records a `failed` event rather than launching.
func createMainWorktree(t *testing.T, repo db.Repository, projectID string, now time.Time) {
	t.Helper()
	require.NoError(t, repo.CreateWorktree(context.Background(), &db.Worktree{
		ID:         uuid.NewString(),
		Name:       "main",
		Path:       t.TempDir(),
		Branch:     "main",
		BaseBranch: "main",
		ProjectID:  projectID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
}

// startTriggerDevServer boots one ephemeral in-memory Temporal on a free port,
// shared across this package's trigger integration tests.
func startTriggerDevServer(t *testing.T) client.Client {
	t.Helper()
	triggerDevServerOnce.Do(func() {
		opts := testsuite.DevServerOptions{
			LogLevel:      "never",
			ClientOptions: &client.Options{Namespace: "reliant-trigger-integration"},
		}
		if path, err := exec.LookPath("temporal"); err == nil {
			opts.ExistingPath = path
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		srv, err := testsuite.StartDevServer(ctx, opts)
		if err != nil {
			triggerDevServerErr = err
			return
		}
		triggerDevServer = srv
		triggerDevClient = srv.Client()
	})
	if triggerDevServerErr != nil {
		t.Fatalf("start Temporal dev server: %v", triggerDevServerErr)
	}
	return triggerDevClient
}
