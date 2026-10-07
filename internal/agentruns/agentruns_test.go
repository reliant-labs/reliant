// Copyright (c) 2025 Reliant Labs
package agentruns

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/threads"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// --- unit: what the adapter asks the launcher for ---

type fakeLauncher struct {
	event launch.Event
	spec  launch.Spec
	res   *launch.Result
	err   error
}

func (f *fakeLauncher) Launch(_ context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error) {
	f.event, f.spec = ev, spec
	return f.res, f.err
}

func TestStartRun_CarriesNoMachineIntoTheLaunch(t *testing.T) {
	launcher := &fakeLauncher{res: &launch.Result{Chat: &db.Chat{ID: "new-chat"}, RunID: "run-1"}}
	_, err := New(launcher, nil).StartRun(context.Background(), tools.StartRunRequest{
		OwnerUserID: "owner", ProjectID: "proj", Workflow: "builtin://agent", Message: "do it",
		DedupeKey: "parent:toolu_nm", NoMachine: true,
	})
	require.NoError(t, err)
	assert.True(t, launcher.spec.NoMachine, "a run started from a no-machine chat has no machine")
}

func TestStartRun_LaunchesNotUnattendedWithAgentStartRunEvent(t *testing.T) {
	launcher := &fakeLauncher{res: &launch.Result{Chat: &db.Chat{ID: "new-chat"}, RunID: "run-1"}}
	runs := New(launcher, nil)

	started, err := runs.StartRun(context.Background(), tools.StartRunRequest{
		OwnerUserID: "owner", ProjectID: "proj", Workflow: "builtin://agent", Message: "do it",
		Inputs: map[string]any{"n": 3, "flag": true}, Presets: map[string]string{"model": "fast"},
		Title: "named", ParentChatID: "parent", DedupeKey: "parent:toolu_1", DaemonID: "daemon-7",
	})
	require.NoError(t, err)
	assert.Equal(t, tools.StartedRun{ChatID: "new-chat", RunID: "run-1"}, started)

	assert.False(t, launcher.spec.Unattended,
		"the run belongs to a human who can watch and answer it; unattended is a trigger concept")
	assert.Equal(t, "owner", launcher.spec.OwnerUserID)
	assert.Equal(t, "proj", launcher.spec.ProjectID)
	assert.Equal(t, "daemon-7", launcher.spec.DaemonID, "the run is pinned to the daemon the caller named, as triggers do")
	assert.Equal(t, "builtin://agent", launcher.spec.Workflow)
	assert.Equal(t, map[string]string{"model": "fast"}, launcher.spec.Presets)
	require.NotNil(t, launcher.spec.Title)
	assert.Equal(t, "named", *launcher.spec.Title)
	assert.False(t, launcher.spec.GenerateTitle, "an explicit title is not regenerated")
	assert.Empty(t, launcher.spec.ChatID, "a NEW chat is created, never an existing one started")
	require.Len(t, launcher.spec.Messages, 1)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, launcher.spec.Messages[0].Role)
	assert.Equal(t, "do it", launcher.spec.Messages[0].Content)
	assert.EqualValues(t, 3, launcher.spec.Params["n"].GetNumberValue())
	assert.True(t, launcher.spec.Params["flag"].GetBoolValue())

	assert.Equal(t, core.TriggerEventKindAgentStartRun, launcher.event.Kind)
	assert.Equal(t, "parent:toolu_1", launcher.event.DedupeKey)
	assert.Equal(t, "parent", launcher.event.Payload["parent_chat_id"])
	assert.False(t, launcher.event.OccurredAt.IsZero())
}

func TestStartRun_GeneratesATitleWhenNoneIsGiven(t *testing.T) {
	launcher := &fakeLauncher{res: &launch.Result{Chat: &db.Chat{ID: "c"}}}
	_, err := New(launcher, nil).StartRun(context.Background(), tools.StartRunRequest{
		OwnerUserID: "o", ProjectID: "p", Workflow: "w", Message: "m", DedupeKey: "k",
	})
	require.NoError(t, err)
	assert.True(t, launcher.spec.GenerateTitle)
	assert.Nil(t, launcher.spec.Title)
}

func TestStartRun_AlreadyLaunchedAttachesInsteadOfFailing(t *testing.T) {
	launcher := &fakeLauncher{err: &launch.AlreadyLaunchedError{ChatID: "existing"}}
	started, err := New(launcher, nil).StartRun(context.Background(), tools.StartRunRequest{
		OwnerUserID: "o", ProjectID: "p", Workflow: "w", Message: "m", DedupeKey: "k",
	})
	require.NoError(t, err)
	assert.Equal(t, "existing", started.ChatID)
	assert.True(t, started.AlreadyStarted)
}

func TestStartRun_PropagatesLaunchFailures(t *testing.T) {
	boom := &launch.ValidationError{Reason: "workflow \"nope\" not found"}
	_, err := New(&fakeLauncher{err: boom}, nil).StartRun(context.Background(), tools.StartRunRequest{
		OwnerUserID: "o", ProjectID: "p", Workflow: "nope", Message: "m", DedupeKey: "k",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}

func TestStartRun_RejectsInputsThatCannotBeRepresented(t *testing.T) {
	_, err := New(&fakeLauncher{res: &launch.Result{Chat: &db.Chat{ID: "c"}}}, nil).StartRun(context.Background(), tools.StartRunRequest{
		OwnerUserID: "o", ProjectID: "p", Workflow: "w", Message: "m", DedupeKey: "k",
		Inputs: map[string]any{"bad": make(chan int)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestAdapterWithoutDependenciesFailsInsteadOfPanicking(t *testing.T) {
	runs := New(nil, nil)
	ctx := context.Background()
	_, err := runs.StartRun(ctx, tools.StartRunRequest{})
	assert.Error(t, err)
	assert.Error(t, runs.PauseRun(ctx, "u", "c"))
	_, err = runs.ResumeRun(ctx, "u", "c")
	assert.Error(t, err)
	assert.Error(t, runs.CancelRun(ctx, "u", "c"))
	_, err = runs.DeliverToRun(ctx, "u", "c", "m")
	assert.Error(t, err)
}

// --- unit: lifecycle and messaging run as the run's owner ---

type fakeRunAPI struct {
	mu       sync.Mutex
	calls    []string
	userSeen string
	resume   *reliantv1.ResumeRunResponse
	signal   *reliantv1.SignalRunResponse
	signaled *reliantv1.SignalRunRequest
	err      error
}

func (f *fakeRunAPI) record(ctx context.Context, call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.userSeen, _ = auth.GetUserIDFromContext(ctx)
}

func (f *fakeRunAPI) PauseRun(ctx context.Context, r *connect.Request[reliantv1.PauseRunRequest]) (*connect.Response[reliantv1.PauseRunResponse], error) {
	f.record(ctx, "pause:"+r.Msg.RunId)
	return connect.NewResponse(&reliantv1.PauseRunResponse{Success: true}), f.err
}

func (f *fakeRunAPI) ResumeRun(ctx context.Context, r *connect.Request[reliantv1.ResumeRunRequest]) (*connect.Response[reliantv1.ResumeRunResponse], error) {
	f.record(ctx, "resume:"+r.Msg.RunId)
	return connect.NewResponse(f.resume), f.err
}

func (f *fakeRunAPI) CancelRun(ctx context.Context, r *connect.Request[reliantv1.CancelRunRequest]) (*connect.Response[reliantv1.CancelRunResponse], error) {
	f.record(ctx, "cancel:"+r.Msg.RunId)
	return connect.NewResponse(&reliantv1.CancelRunResponse{Success: true}), f.err
}

func (f *fakeRunAPI) SignalRun(ctx context.Context, r *connect.Request[reliantv1.SignalRunRequest]) (*connect.Response[reliantv1.SignalRunResponse], error) {
	f.record(ctx, "signal:"+r.Msg.RunId)
	f.signaled = r.Msg
	return connect.NewResponse(f.signal), f.err
}

func TestLifecycleCallsRunServiceAsTheOwner(t *testing.T) {
	api := &fakeRunAPI{resume: &reliantv1.ResumeRunResponse{Success: true, Message: "ok"}}
	runs := New(nil, api)
	ctx := context.Background()

	require.NoError(t, runs.PauseRun(ctx, "owner-1", "chat-a"))
	assert.Equal(t, "owner-1", api.userSeen, "RunService authorizes from the context, so the call must carry the owner")

	result, err := runs.ResumeRun(ctx, "owner-2", "chat-b")
	require.NoError(t, err)
	assert.Equal(t, tools.RunResumeResult{Resumed: true, Detail: "ok"}, result)
	assert.Equal(t, "owner-2", api.userSeen)

	require.NoError(t, runs.CancelRun(ctx, "owner-3", "chat-c"))
	assert.Equal(t, "owner-3", api.userSeen)

	assert.Equal(t, []string{"pause:chat-a", "resume:chat-b", "cancel:chat-c"}, api.calls)
}

func TestResumeReportsAnUnservedResumeAsNotResumed(t *testing.T) {
	api := &fakeRunAPI{resume: &reliantv1.ResumeRunResponse{Success: false, Message: "session interrupted"}}
	result, err := New(nil, api).ResumeRun(context.Background(), "o", "c")
	require.NoError(t, err)
	assert.False(t, result.Resumed)
	assert.Equal(t, "session interrupted", result.Detail)
}

func TestLifecycleSurfacesServiceErrors(t *testing.T) {
	api := &fakeRunAPI{err: errors.New("temporal down"), resume: &reliantv1.ResumeRunResponse{}}
	runs := New(nil, api)
	assert.ErrorContains(t, runs.PauseRun(context.Background(), "o", "c"), "temporal down")
	_, err := runs.ResumeRun(context.Background(), "o", "c")
	assert.ErrorContains(t, err, "temporal down")
	assert.ErrorContains(t, runs.CancelRun(context.Background(), "o", "c"), "temporal down")
}

func TestDeliverToRunUsesSignalRunSemantics(t *testing.T) {
	api := &fakeRunAPI{signal: &reliantv1.SignalRunResponse{Delivered: true, MessageIds: []string{"m-1"}}}
	delivery, err := New(nil, api).DeliverToRun(context.Background(), "owner", "chat-x", "hello")
	require.NoError(t, err)
	assert.Equal(t, tools.RunDelivery{Delivered: true, MessageID: "m-1"}, delivery)
	assert.Equal(t, "owner", api.userSeen)
	require.Len(t, api.signaled.Messages, 1)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, api.signaled.Messages[0].Role)
	assert.Equal(t, "hello", api.signaled.Messages[0].Content)

	api.signal = &reliantv1.SignalRunResponse{Delivered: false}
	delivery, err = New(nil, api).DeliverToRun(context.Background(), "owner", "chat-x", "hello")
	require.NoError(t, err)
	assert.False(t, delivery.Delivered, "SignalRun reports a non-live run as delivered=false, never an error")
}

// --- integration: through the real launcher and the real database ---

type recordingStarter struct {
	mu    sync.Mutex
	calls []client.StartWorkflowOptions
	input []v2.WorkflowInput
}

type fakeRun struct {
	client.WorkflowRun
	id, runID string
}

func (r *fakeRun) GetID() string    { return r.id }
func (r *fakeRun) GetRunID() string { return r.runID }

func (s *recordingStarter) ExecuteWorkflow(_ context.Context, opts client.StartWorkflowOptions, _ interface{}, args ...interface{}) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, opts)
	if in, ok := args[0].(v2.WorkflowInput); ok {
		s.input = append(s.input, in)
	}
	return &fakeRun{id: opts.ID, runID: "run-" + opts.ID}, nil
}

type noopRecorder struct{}

func (noopRecorder) RecordRun(context.Context, string, string, string) {}

func TestStartRunThroughTheRealLauncherRecordsLineageAndIsIdempotent(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	now := time.Now().UTC()

	const owner = "agentruns-owner"
	projectID := "agentruns-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: owner, Name: "P", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main", ProjectID: projectID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	// The calling chat: the parent the new run's lineage should name.
	parentID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: parentID, Title: "parent", ProjectID: projectID, UserID: owner,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	starter := &recordingStarter{}
	runs := New(launch.NewLauncher(repo, threads.NewService(repo), starter, noopRecorder{}, "test-queue"), nil)

	req := tools.StartRunRequest{
		OwnerUserID: owner, ProjectID: projectID, Workflow: "builtin://agent",
		Message: "audit the repo", Title: "audit", ParentChatID: parentID,
		DedupeKey: parentID + ":toolu_abc",
		Inputs:    map[string]any{"model": map[string]any{"id": "mock"}},
	}
	first, err := runs.StartRun(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, first.ChatID)
	assert.False(t, first.AlreadyStarted)

	// The run is the OWNER's, not unattended, and Temporal was started once.
	chat, err := repo.GetChat(ctx, first.ChatID)
	require.NoError(t, err)
	assert.Equal(t, owner, chat.UserID)
	require.Len(t, starter.input, 1)
	_, unattended := starter.input[0].Inputs[v2.InputKeyUnattended]
	assert.False(t, unattended, "an agent-started run must not be marked unattended")

	// The launch left an agent.start_run event naming the parent.
	event, err := repo.GetTriggerEventByChat(ctx, core.TriggerEventKindAgentStartRun, first.ChatID)
	require.NoError(t, err)
	assert.Equal(t, parentID, event.Payload["parent_chat_id"])
	assert.Equal(t, parentID+":toolu_abc", event.DedupeKey)
	assert.Equal(t, core.TriggerEventLaunched, event.Outcome)

	// Re-launching the same tool call attaches to the same chat; no second
	// chat, no second Temporal start.
	again, err := runs.StartRun(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, first.ChatID, again.ChatID)
	assert.True(t, again.AlreadyStarted)
	assert.Len(t, starter.calls, 1, "a retried start must not start Temporal twice")

	// The new run counts toward the live cap until it finishes.
	live, err := repo.CountLiveLaunchedRuns(ctx, owner, core.TriggerEventKindAgentStartRun)
	require.NoError(t, err)
	assert.Equal(t, 1, live)
}
