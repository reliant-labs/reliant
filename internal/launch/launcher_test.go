// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// fakeStarter records every ExecuteWorkflow call instead of reaching Temporal.
// Launch is the only thing that starts a root run, so what it asks Temporal for
// — the workflow id, the task queue, the inputs — is the contract worth pinning.
type fakeStarter struct {
	mu    sync.Mutex
	calls []startCall
	// failRootStarts makes the next N DynamicWorkflow starts fail, simulating
	// Temporal being unreachable after the session committed.
	failRootStarts int
}

type startCall struct {
	options  client.StartWorkflowOptions
	workflow interface{}
	args     []interface{}
}

func (f *fakeStarter) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, startCall{options: options, workflow: wf, args: args})
	if _, isRoot := args[0].(v2.WorkflowInput); isRoot && f.failRootStarts > 0 {
		f.failRootStarts--
		return nil, errors.New("temporal unavailable")
	}
	return &fakeWorkflowRun{id: options.ID, runID: "run-" + options.ID}, nil
}

// rootRun returns the DynamicWorkflow start, which is the one that matters.
// GenerateTitleWorkflow also goes through this fake.
func (f *fakeStarter) rootRun(t *testing.T) (startCall, v2.WorkflowInput) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		input, ok := call.args[0].(v2.WorkflowInput)
		if ok {
			return call, input
		}
	}
	t.Fatal("no DynamicWorkflow start was issued")
	return startCall{}, v2.WorkflowInput{}
}

// lastRootInput is the WorkflowInput of the most recent DynamicWorkflow start.
func (f *fakeStarter) lastRootInput(t *testing.T) v2.WorkflowInput {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if input, ok := f.calls[i].args[0].(v2.WorkflowInput); ok {
			return input
		}
	}
	t.Fatal("no DynamicWorkflow start was issued")
	return v2.WorkflowInput{}
}

func (f *fakeStarter) startedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		ids = append(ids, call.options.ID)
	}
	return ids
}

type fakeWorkflowRun struct {
	client.WorkflowRun
	id    string
	runID string
}

func (r *fakeWorkflowRun) GetID() string    { return r.id }
func (r *fakeWorkflowRun) GetRunID() string { return r.runID }

// recordingRunRecorder stands in for runs.Service, which Launch calls to write
// the run ids back onto the chat.
type recordingRunRecorder struct {
	repo  db.Repository
	calls []string
}

func (r *recordingRunRecorder) RecordRun(ctx context.Context, chatID, workflowID, runID string) {
	r.calls = append(r.calls, chatID+"/"+workflowID+"/"+runID)
	chat, err := r.repo.GetChat(ctx, chatID)
	if err != nil {
		return
	}
	chat.WorkflowID = &workflowID
	chat.RunID = &runID
	_ = r.repo.UpdateChat(ctx, chat)
}

const launchTestUserID = "launch-user"

// launchFixture builds a project with its main worktree — the minimum a launch
// needs, since every chat must bind to a resolvable worktree.
func launchFixture(t *testing.T) (*db.Repo, context.Context, string, string) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	now := time.Now().UTC()
	projectID := "launch-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     launchTestUserID,
		Name:       "Launch Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	mainWorktreeID := uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID:         mainWorktreeID,
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

	return repo, ctx, projectID, mainWorktreeID
}

func newTestLauncher(t *testing.T, repo db.Repository, starter *fakeStarter) (*Launcher, *recordingRunRecorder) {
	t.Helper()
	recorder := &recordingRunRecorder{repo: repo}
	return NewLauncher(repo, threads.NewService(repo), starter, recorder, "test-task-queue", nil), recorder
}

// chatStartEvent is what an interactive first send produces.
func chatStartEvent() Event {
	return Event{Kind: core.TriggerEventKindChatStart, OccurredAt: time.Now().UTC()}
}

func userSeed(content string) []SeedMessage {
	return []SeedMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: content}}
}

// messageText reads a message's rendered text, which lives in its content
// blocks rather than on the row.
func messageText(t *testing.T, ctx context.Context, repo db.Repository, messageID string) string {
	t.Helper()
	blocks, err := repo.ListContentBlocks(ctx, messageID)
	require.NoError(t, err)
	var text string
	for _, block := range blocks {
		if block.Content != nil {
			text += *block.Content
		}
	}
	return text
}

func mockModelParams(t *testing.T) map[string]*structpb.Value {
	t.Helper()
	value, err := structpb.NewValue(map[string]interface{}{"id": "mock"})
	require.NoError(t, err)
	return map[string]*structpb.Value{"model": value}
}

// The whole contract in one test: a launch commits the chat, its pending root
// run and thread, and the seed messages together, then starts the run.
func TestLaunchCreatesSessionAndStartsRun(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{}
	launcher, recorder := newTestLauncher(t, repo, starter)

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID,
		ProjectID:   projectID,
		Workflow:    "builtin://agent",
		Params:      mockModelParams(t),
		Messages: []SeedMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, Content: "be brief"},
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "hello"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Chat)

	chatID := result.Chat.ID
	assert.Equal(t, chatID, result.WorkflowID,
		"the root workflow id IS the chat id — everything that signals a run relies on it")
	assert.Equal(t, mainWorktreeID, *result.Chat.WorktreeID,
		"a chat with no worktree named must bind to the project's main one, or the sidebar hides it")

	// The chat row committed.
	persisted, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, launchTestUserID, persisted.UserID)
	assert.Equal(t, "builtin://agent", *persisted.WorkflowName)

	// Its root run is ACTIVE: Launch closes the pending window itself once
	// Temporal has accepted the start.
	rootRun, err := repo.GetWorkflow(ctx, result.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), rootRun.Status)
	assert.Equal(t, chatID, rootRun.Thread, "root run: thread == workflow id")
	require.NotNil(t, rootRun.OwnerUserID)
	assert.Equal(t, launchTestUserID, *rootRun.OwnerUserID)

	// And its thread, which is what messages hang off.
	thread, err := repo.GetThread(ctx, result.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, chatID, thread.ChatID)

	// Both seed messages landed, system first — the order matters, because the
	// system framing has to be in view before the model reads the user's ask.
	rootThread := chatID
	messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{Thread: &rootThread, Limit: 10})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, messages[0].Role)
	assert.Equal(t, "be brief", messageText(t, ctx, repo, messages[0].ID))
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[1].Role)
	assert.Equal(t, "hello", messageText(t, ctx, repo, messages[1].ID))

	// Temporal was asked to run the chat's workflow, under the chat's id, on
	// the configured queue.
	call, input := starter.rootRun(t)
	assert.Equal(t, chatID, call.options.ID)
	assert.Equal(t, "test-task-queue", call.options.TaskQueue)
	// Temporal's 10s default times out replaying a large history, which is
	// what stretched a background spawn's report window to minutes — see
	// docs/incidents/2026-10-04-spawn-report-collision.md.
	assert.Equal(t, workflow.DynamicWorkflowTaskTimeout, call.options.WorkflowTaskTimeout)
	assert.Equal(t, chatID, input.ChatID)
	assert.Equal(t, "builtin://agent", input.WorkflowName)
	require.NotNil(t, input.ExecContext)
	assert.Equal(t, chatID, input.ExecContext.Thread)

	// RecordRun is what puts the run ids on the chat.
	require.Len(t, recorder.calls, 1)
	assert.Equal(t, chatID+"/"+chatID+"/run-"+chatID, recorder.calls[0])
	assert.Equal(t, "run-"+chatID, result.RunID)

	// The launch is recorded: one chat.start event keyed by the chat id.
	assert.NotEmpty(t, result.EventID)
}

// An idempotent source derives the chat id from its own dedupe key, so a
// retried fire cannot create a second chat. Honoring NewChatID is what makes
// that possible.
func TestLaunchHonorsNewChatID(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	wantID := uuid.NewString()
	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID,
		ProjectID:   projectID,
		NewChatID:   wantID,
		Workflow:    "builtin://agent",
		Params:      mockModelParams(t),
		Messages:    userSeed("hello"),
	})
	require.NoError(t, err)

	assert.Equal(t, wantID, result.Chat.ID)
	assert.Equal(t, wantID, result.WorkflowID)
	assert.Contains(t, starter.startedIDs(), wantID,
		"the Temporal workflow id must be the supplied chat id, or a retry cannot attach to it")

	_, err = repo.GetChat(ctx, wantID)
	require.NoError(t, err, "the chat must be persisted under the supplied id")
}

// A scheduled run has nobody to answer an ask_user, and the runtime decides
// that by reading this one input off the root run's inputs.
func TestLaunchUnattendedSetsWorkflowInput(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID,
		ProjectID:   projectID,
		Workflow:    "builtin://agent",
		Params:      mockModelParams(t),
		Messages:    userSeed("run the nightly build"),
		Unattended:  true,
	})
	require.NoError(t, err)

	_, input := starter.rootRun(t)
	assert.True(t, v2.IsUnattended(input.Inputs),
		"the runtime reads unattended off the root run's inputs and propagates it to every spawn")
}

// The interactive path asks for a title; a scheduled one supplies its own and
// must not pay for an LLM call to re-derive it.
func TestLaunchGenerateTitleIsOptional(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)

	withTitle := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, withTitle)
	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID:   launchTestUserID,
		ProjectID:     projectID,
		Workflow:      "builtin://agent",
		Params:        mockModelParams(t),
		Messages:      userSeed("hello"),
		GenerateTitle: true,
	})
	require.NoError(t, err)
	assert.Contains(t, withTitle.startedIDs(), "generate-title-"+result.Chat.ID)

	without := &fakeStarter{}
	launcher2, _ := newTestLauncher(t, repo, without)
	result2, err := launcher2.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID,
		ProjectID:   projectID,
		Workflow:    "builtin://agent",
		Params:      mockModelParams(t),
		Messages:    userSeed("hello"),
	})
	require.NoError(t, err)
	assert.NotContains(t, without.startedIDs(), "generate-title-"+result2.Chat.ID)
}

// Validation runs BEFORE the transaction, which is the whole reason it is where
// it is: a workflow whose tree cannot run must not leave a chat behind that
// nothing will ever execute.
func TestLaunchValidationFailureLeavesNoChat(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID,
		ProjectID:   projectID,
		Workflow:    "builtin://definitely-not-a-real-workflow",
		Params:      mockModelParams(t),
		Messages:    userSeed("hello"),
	})
	require.Error(t, err)

	var validationErr *ValidationError
	require.ErrorAs(t, err, &validationErr,
		"an unrunnable workflow is a validation failure, not an internal error — retrying cannot help")

	chats, listErr := repo.ListChats(ctx, db.ChatFilters{
		UserID:    launchTestUserID,
		ProjectID: &projectID,
		Limit:     10,
	})
	require.NoError(t, listErr)
	assert.Empty(t, chats, "a rejected launch must leave no orphan chat row")
	assert.Empty(t, starter.startedIDs(), "and must never reach Temporal")
}

// A malformed Spec and an unhostable one are different wire codes, and the
// handler must not have to read the message text to tell them apart.
func TestLaunchValidationKinds(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

	t.Run("a dotted param key is an invalid argument", func(t *testing.T) {
		_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID,
			ProjectID:   projectID,
			Workflow:    "builtin://agent",
			Params:      map[string]*structpb.Value{"agent.model": structpb.NewStringValue("mock")},
			Messages:    userSeed("hello"),
		})
		var validationErr *ValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Equal(t, ValidationInvalidArgument, validationErr.Kind)
		assert.Contains(t, validationErr.Reason, "nested objects")
	})

	t.Run("a project with no main worktree is a failed precondition", func(t *testing.T) {
		now := time.Now().UTC()
		barrenProjectID := "launch-barren-" + uuid.NewString()
		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID:         barrenProjectID,
			UserID:     launchTestUserID,
			Name:       "No Worktree",
			Path:       t.TempDir(),
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		}))

		_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID,
			ProjectID:   barrenProjectID,
			Workflow:    "builtin://agent",
			Params:      mockModelParams(t),
			Messages:    userSeed("hello"),
		})
		var validationErr *ValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Equal(t, ValidationFailedPrecondition, validationErr.Kind)
		assert.Contains(t, validationErr.Reason, "no main worktree")
	})

	t.Run("a project the caller does not own is not found", func(t *testing.T) {
		_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: "somebody-else",
			ProjectID:   projectID,
			Workflow:    "builtin://agent",
			Params:      mockModelParams(t),
			Messages:    userSeed("hello"),
		})
		var notFoundErr *NotFoundError
		require.ErrorAs(t, err, &notFoundErr)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("a launch with nothing to say is refused", func(t *testing.T) {
		_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID,
			ProjectID:   projectID,
			Workflow:    "builtin://agent",
			Params:      mockModelParams(t),
		})
		var validationErr *ValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Contains(t, validationErr.Reason, "at least one user message")
	})
}

// pendingBranch seeds what BranchChat leaves behind: a chat whose root run is
// pending, with a thread and one inherited message, and nothing started.
func pendingBranch(t *testing.T, repo *db.Repo, ctx context.Context, projectID, worktreeID string) string {
	t.Helper()
	chatID := uuid.NewString()
	now := time.Now().UTC()
	workflowName := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: launchTestUserID, Title: "", ProjectID: projectID,
		WorktreeID: &worktreeID, WorkflowName: &workflowName, WorkflowID: &chatID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, _, _, err := threads.NewService(repo).CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: chatID, ChatID: chatID, WorkflowName: workflowName, Thread: chatID,
			Status: db.Pending(), CreatedAt: now, OwnerUserID: ptr(launchTestUserID),
		},
		ThreadID: chatID, ChatID: chatID,
	})
	require.NoError(t, err)
	return chatID
}

func ptr[T any](v T) *T { return &v }

func eventsFor(t *testing.T, ctx context.Context, repo db.Repository, chatID string) *core.TriggerEvent {
	t.Helper()
	ev, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindChatStart, chatID)
	require.NoError(t, err)
	return ev
}

// A chat start is recorded as a chat.start event whose dedupe key is the chat
// id, the Temporal start attaches to an existing run rather than killing it,
// and the root run is active by the time Launch returns — "pending" must mean
// "never started", and SendMessage relies on it.
func TestLaunchRecordsEventStartsWithUseExistingAndActivatesRoot(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	chatID := uuid.NewString()
	result, err := launcher.Launch(ctx, Event{
		Kind: core.TriggerEventKindChatStart, DedupeKey: chatID,
		Payload: map[string]any{"message_count": 1},
	}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, NewChatID: chatID,
		Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("hello"),
	})
	require.NoError(t, err)

	ev := eventsFor(t, ctx, repo, chatID)
	assert.Equal(t, result.EventID, ev.ID)
	assert.Equal(t, core.TriggerEventLaunched, ev.Outcome)
	require.NotNil(t, ev.ChatID)
	assert.Equal(t, chatID, *ev.ChatID)
	assert.Nil(t, ev.TriggerID)
	assert.Equal(t, "builtin://agent", ev.Payload["workflow"])
	assert.NotContains(t, ev.Payload, "content", "message text belongs in messages, not the event payload")

	call, _ := starter.rootRun(t)
	assert.Equal(t, enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, call.options.WorkflowIDConflictPolicy)

	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status)
}

// A branch's first send starts the existing chat: the seed lands on its forked
// thread, exactly one event is recorded, a workflow switch applies, and a
// second start is refused without a second Temporal call.
func TestLaunchStartsPendingBranchAndRefusesASecondStart(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)

	spec := Spec{
		OwnerUserID: launchTestUserID, ChatID: chatID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("continue on the branch"),
		GreenfieldProbe: false,
	}
	ev := Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}

	result, err := launcher.Launch(ctx, ev, spec)
	require.NoError(t, err)
	assert.Equal(t, chatID, result.Chat.ID)

	rootThread := chatID
	messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{Thread: &rootThread, Limit: 10})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "continue on the branch", messageText(t, ctx, repo, messages[0].ID))
	assert.Equal(t, result.EventID, eventsFor(t, ctx, repo, chatID).ID)

	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status)

	before := len(starter.startedIDs())
	_, err = launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrAlreadyLaunched, "the same send again is a duplicate of the recorded start")
	assert.NotErrorIs(t, err, ErrNotPending)

	different := spec
	different.Messages = userSeed("a different message")
	_, err = launcher.Launch(ctx, ev, different)
	require.ErrorIs(t, err, ErrNotPending, "new content for a started chat belongs to SendMessage")
	assert.Len(t, starter.startedIDs(), before, "a refused start must not call Temporal again")
}

func TestLaunchPendingSwitchesWorkflowAndRejectsForeignProject(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)

	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}, Spec{
		OwnerUserID: launchTestUserID, ChatID: chatID, ProjectID: "some-other-project",
		Workflow: "builtin://agent", Messages: userSeed("hi"),
	})
	var validationErr *ValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Empty(t, starter.startedIDs())

	_, err = launcher.Launch(ctx, Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}, Spec{
		OwnerUserID: launchTestUserID, ChatID: chatID, Workflow: "builtin://structured-agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.NoError(t, err)
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "builtin://structured-agent", *chat.WorkflowName)
	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "builtin://structured-agent", root.WorkflowName)
}

// The event and chat commit before Temporal is asked. If that start fails, a
// retry with the same chat id must finish the start — one event row, one set
// of messages — instead of reporting a duplicate or creating a second chat.
func TestLaunchResumesAfterTemporalStartFailure(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{failRootStarts: 1}
	launcher, _ := newTestLauncher(t, repo, starter)

	chatID := uuid.NewString()
	ev := Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}
	spec := Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, NewChatID: chatID,
		Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("hello"),
	}

	_, err := launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrInternal)

	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Pending(), root.Status, "a failed start leaves the chat pending, never half-active")

	result, err := launcher.Launch(ctx, ev, spec)
	require.NoError(t, err)
	assert.Equal(t, chatID, result.Chat.ID)

	root, err = repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status)

	rootThread := chatID
	messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{Thread: &rootThread, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, messages, 1, "the retry must not save the seed messages twice")
	assert.Equal(t, result.EventID, eventsFor(t, ctx, repo, chatID).ID)

	// Now that it has started, a third call is a true duplicate.
	_, err = launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrAlreadyLaunched)
}

// The caller's JWT travels on the execution context, so a cloud-daemon chat's
// first run resolves the control-plane daemon like its later runs do; Mode
// becomes workflow_params.mode.
func TestLaunchCarriesUserJWTAndMode(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	mode := "plan"
	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Mode: &mode, UserJWT: "jwt-123", Messages: userSeed("hi"),
	})
	require.NoError(t, err)

	_, input := starter.rootRun(t)
	assert.Equal(t, "jwt-123", input.ExecContext.UserJWT)
	assert.Equal(t, "plan", input.Inputs["mode"])
}

// A scheduled run reads the event that started it. The launcher builds the
// trigger from the event row it just wrote, so id and payload round-trip.
func TestLaunchCarriesTheTriggerOnWorkflowInput(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	scheduledFor := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	triggerID := uuid.NewString()
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: launchTestUserID, ProjectID: projectID, Name: "nightly",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: scheduledFor, UpdatedAt: scheduledFor,
	}))

	result, err := launcher.Launch(ctx, Event{
		Kind:       core.TriggerEventKindSchedule,
		TriggerID:  triggerID,
		DedupeKey:  "fire-" + uuid.NewString(),
		OccurredAt: scheduledFor,
		Payload:    map[string]any{"scheduled_for": "2026-01-02T09:00:00Z", "trigger_name": "nightly"},
	}, Spec{
		OwnerUserID: launchTestUserID,
		ProjectID:   projectID,
		Workflow:    "builtin://agent",
		Params:      mockModelParams(t),
		Messages:    userSeed("run the nightly build"),
		Unattended:  true,
	})
	require.NoError(t, err)

	_, input := starter.rootRun(t)
	require.NotNil(t, input.Trigger, "the root run must carry the event that launched it")
	assert.Equal(t, "schedule", input.Trigger.Kind)
	assert.Equal(t, triggerID, input.Trigger.TriggerID)
	assert.Equal(t, result.EventID, input.Trigger.EventID)
	assert.Equal(t, "2026-01-02T09:00:00Z", input.Trigger.OccurredAt)
	assert.Equal(t, "nightly", input.Trigger.Payload["trigger_name"])

	// An interactive start is an event too.
	starter2 := &fakeStarter{}
	launcher2, _ := newTestLauncher(t, repo, starter2)
	_, err = launcher2.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.NoError(t, err)
	_, input = starter2.rootRun(t)
	require.NotNil(t, input.Trigger)
	assert.Equal(t, "chat.start", input.Trigger.Kind)
}

// A chat's launch event is found by chat id, which is how restarts rebuild it.
func TestLoadChatTriggerReturnsTheLaunchEvent(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.NoError(t, err)

	got := LoadChatTrigger(ctx, repo, result.Chat.ID)
	assert.Equal(t, "chat.start", got.Kind)
	assert.Equal(t, result.EventID, got.EventID)

	legacy := LoadChatTrigger(ctx, repo, "no-such-chat")
	assert.Equal(t, "chat.start", legacy.Kind, "a chat with no event reads as an interactive start")
}

// A pinned daemon has to reach BOTH the chat row (so later sends keep using
// it) and the run's inputs (which the runtime turns into its daemon selector).
func TestLaunchPinsSpecDaemonOnNewChat(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"), DaemonID: "daemon-pinned",
	})
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, "daemon-pinned", *chat.ActiveDaemonID)

	_, input := starter.rootRun(t)
	assert.Equal(t, "daemon-pinned", input.Inputs["session_daemon_id"])
}

func TestLaunchWithoutSpecDaemonLeavesChatUnpinned(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
	_, input := starter.rootRun(t)
	assert.NotContains(t, input.Inputs, "session_daemon_id")
}

func TestLaunchPinsSpecDaemonOnPendingChat(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)

	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}, Spec{
		OwnerUserID: launchTestUserID, ChatID: chatID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("go"), DaemonID: "daemon-pinned",
	})
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, "daemon-pinned", *chat.ActiveDaemonID)

	_, input := starter.rootRun(t)
	assert.Equal(t, "daemon-pinned", input.Inputs["session_daemon_id"])
}

// M4: the event row is inserted before the chat, so for two concurrent
// launches of the same (kind, dedupe_key) the unique constraint — not the
// chats primary key — decides the winner. Exactly one creates the chat; the
// loser finishes the winner's launch (or reports it already launched) and
// never surfaces an Internal error.
func TestConcurrentLaunchesOfTheSameEventConvergeOnOneChat(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	chatID := uuid.NewString()
	ev := Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "fire-" + chatID}
	spec := Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, NewChatID: chatID,
		Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("hello"),
	}

	const launches = 6
	errs := make([]error, launches)
	var wg sync.WaitGroup
	begin := make(chan struct{})
	for i := 0; i < launches; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			_, errs[i] = launcher.Launch(ctx, ev, spec)
		}(i)
	}
	close(begin)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			assert.ErrorIs(t, err, ErrAlreadyLaunched, "launch %d must succeed or report already-launched, got %v", i, err)
			assert.NotErrorIs(t, err, ErrInternal, "launch %d lost the race with an Internal error", i)
		}
	}

	stored, err := repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, ev.DedupeKey)
	require.NoError(t, err)
	require.NotNil(t, stored.ChatID)
	assert.Equal(t, chatID, *stored.ChatID, "the event must point at the one chat")

	rootThread := chatID
	messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{Thread: &rootThread, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, messages, 1, "the seed message must be saved exactly once")

	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status)
}

// A guard that declines aborts the launch with nothing written: no chat and
// no event row.
func TestLaunchGuardDeclinesWithoutWritingAnything(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	chatID := uuid.NewString()
	ev := Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "fire-" + chatID}
	_, err := launcher.Launch(ctx, ev, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, NewChatID: chatID,
		Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("hello"),
		Guard: func(context.Context) (string, error) { return "previous run is running", nil },
	})
	var declined *DeclinedError
	require.ErrorAs(t, err, &declined)
	assert.Equal(t, "previous run is running", declined.Reason)

	_, err = repo.GetChat(ctx, chatID)
	assert.Error(t, err, "a declined launch must not create the chat")
	_, err = repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindSchedule, ev.DedupeKey)
	assert.ErrorIs(t, err, core.ErrTriggerEventNotFound, "a declined launch must not write an event row")
	assert.Empty(t, starter.startedIDs())
}

// A re-run must start from exactly what the run started from, so the seed user
// text is recorded on the start, for every kind of launch.
func TestLaunchRecordsThePromptOnTheStart(t *testing.T) {
	for _, kind := range []core.TriggerEventKind{core.TriggerEventKindChatStart, core.TriggerEventKindAgentStartRun} {
		t.Run(string(kind), func(t *testing.T) {
			repo, ctx, projectID, _ := launchFixture(t)
			launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

			chatID := uuid.NewString()
			_, err := launcher.Launch(ctx, Event{Kind: kind, DedupeKey: chatID}, Spec{
				OwnerUserID: launchTestUserID, ProjectID: projectID, NewChatID: chatID,
				Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("review the diff"),
			})
			require.NoError(t, err)

			ev, err := repo.GetTriggerEventByDedupe(ctx, kind, chatID)
			require.NoError(t, err)
			start, ok := ev.Payload["start"].(map[string]any)
			require.True(t, ok, "payload = %v", ev.Payload)
			assert.Equal(t, "review the diff", start["prompt"])
		})
	}
}

func TestStartRecordLeavesAnOversizedPromptOut(t *testing.T) {
	payload := map[string]any{}
	startRecord{Workflow: "w", Prompt: strings.Repeat("x", maxRecordedPrompt+1)}.addTo(payload)
	assert.NotContains(t, payload["start"], "prompt")
	payload = map[string]any{}
	startRecord{Workflow: "w", Prompt: strings.Repeat("x", maxRecordedPrompt)}.addTo(payload)
	assert.Contains(t, payload["start"], "prompt")
}
