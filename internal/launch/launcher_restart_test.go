// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func threadTexts(t *testing.T, ctx context.Context, repo db.Repository, chatID string) []string {
	t.Helper()
	rootThread := chatID
	messages, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{Thread: &rootThread, Limit: 50})
	require.NoError(t, err)
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		texts = append(texts, messageText(t, ctx, repo, message.ID))
	}
	return texts
}

func chatStartSpec(t *testing.T, chatID, workflow, text string) (Event, Spec) {
	t.Helper()
	return Event{Kind: core.TriggerEventKindChatStart, DedupeKey: chatID}, Spec{
		OwnerUserID: launchTestUserID, ChatID: chatID, Workflow: workflow,
		Params: mockModelParams(t), Messages: userSeed(text),
	}
}

// M-A: the first start commits and then Temporal fails, leaving a recorded
// pending chat. The user's next send carries DIFFERENT content and a workflow
// switch. It is a new turn, not a retry: the message must land, the switch must
// apply, and the run must start as the rows say.
func TestLaunchPendingNewTurnAfterFailedStartAppliesMessageAndSwitch(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{failRootStarts: 1}
	launcher, _ := newTestLauncher(t, repo, starter)
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)

	ev, spec := chatStartSpec(t, chatID, "builtin://agent", "first")
	_, err := launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrInternal, "the first start's Temporal call fails")

	ev, spec = chatStartSpec(t, chatID, "builtin://structured-agent", "second")
	result, err := launcher.Launch(ctx, ev, spec)
	require.NoError(t, err)
	assert.Equal(t, chatID, result.Chat.ID)

	assert.Equal(t, []string{"first", "second"}, threadTexts(t, ctx, repo, chatID),
		"the second send's message must not be dropped")

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "builtin://structured-agent", *chat.WorkflowName)
	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "builtin://structured-agent", root.WorkflowName)

	started := starter.lastRootInput(t)
	assert.Equal(t, "builtin://structured-agent", started.WorkflowName,
		"the run must start as the workflow the chat row holds")
}

// A true retry (same content) after a failed start is idempotent: one event,
// one copy of the message, the run started from what was persisted.
func TestLaunchPendingTrueRetryIsIdempotent(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{failRootStarts: 1}
	launcher, _ := newTestLauncher(t, repo, starter)
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)

	ev, spec := chatStartSpec(t, chatID, "builtin://structured-agent", "only once")
	_, err := launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrInternal)

	result, err := launcher.Launch(ctx, ev, spec)
	require.NoError(t, err)
	assert.Equal(t, []string{"only once"}, threadTexts(t, ctx, repo, chatID))
	assert.Equal(t, result.EventID, eventsFor(t, ctx, repo, chatID).ID)
	assert.Equal(t, "builtin://structured-agent", starter.lastRootInput(t).WorkflowName)

	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status)
}

// A scheduled fire retried after its trigger was edited must start the run it
// recorded, not one built from the edited definition: the rows say workflow A,
// so initialData must never be built for workflow B.
func TestLaunchScheduleRetryStartsFromPersistedWorkflow(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{failRootStarts: 1}
	launcher, _ := newTestLauncher(t, repo, starter)

	chatID := uuid.NewString()
	ev := Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "fire-" + chatID}
	spec := Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, NewChatID: chatID,
		Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("scheduled"),
	}
	_, err := launcher.Launch(ctx, ev, spec)
	require.ErrorIs(t, err, ErrInternal)

	edited := spec
	edited.Workflow = "builtin://structured-agent"
	_, err = launcher.Launch(ctx, ev, edited)
	require.NoError(t, err)

	assert.Equal(t, "builtin://agent", starter.lastRootInput(t).WorkflowName)
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "builtin://agent", *chat.WorkflowName)
	assert.Equal(t, []string{"scheduled"}, threadTexts(t, ctx, repo, chatID))
}

// Two tabs sending the same pending branch at once. Whatever the interleaving,
// every call that reports success has its message on the thread, a call that
// lost reports ErrNotPending, and the chat ends active with one root event.
func TestLaunchPendingConcurrentStartsDoNotLoseMessages(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)

	texts := []string{"from tab one", "from tab two"}
	errs := make([]error, len(texts))
	var wg sync.WaitGroup
	for i, text := range texts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev, spec := chatStartSpec(t, chatID, "builtin://agent", text)
			_, errs[i] = launcher.Launch(ctx, ev, spec)
		}()
	}
	wg.Wait()

	saved := threadTexts(t, ctx, repo, chatID)
	succeeded := 0
	for i, err := range errs {
		if err == nil {
			succeeded++
			assert.Contains(t, saved, texts[i], "a successful start must have its message saved")
			continue
		}
		assert.True(t, errors.Is(err, ErrNotPending) || errors.Is(err, ErrAlreadyLaunched),
			"the losing start must report it came too late, got %v", err)
	}
	require.GreaterOrEqual(t, succeeded, 1, "one of the two starts must win")

	root, err := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status)
	assert.NotNil(t, eventsFor(t, ctx, repo, chatID))
}

// failingLookups makes the named reads fail the way a database outage does.
type failingLookups struct {
	db.Repository
	failProject  bool
	failChat     bool
	failWorkflow bool
}

func (r *failingLookups) GetProjectWithUserCheck(ctx context.Context, id, userID string) (*db.Project, error) {
	if r.failProject {
		return nil, errors.New("connection reset by peer")
	}
	return r.Repository.GetProjectWithUserCheck(ctx, id, userID)
}

func (r *failingLookups) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	if r.failChat {
		return nil, errors.New("connection reset by peer")
	}
	return r.Repository.GetChat(ctx, id)
}

func (r *failingLookups) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	if r.failWorkflow {
		return nil, errors.New("connection reset by peer")
	}
	return r.Repository.GetUsableWorkflowBySlug(ctx, userID, slug)
}

// M1: a database error on a lookup is not "not found" and not "invalid": the
// fire path treats those as final and records the fire as lost.
func TestLaunchStoreFailuresStayRetryable(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)

	cases := []struct {
		name  string
		repo  *failingLookups
		spec  func(t *testing.T) (Event, Spec)
		isNot []error
	}{
		{
			name: "project lookup on a new chat",
			repo: &failingLookups{Repository: repo, failProject: true},
			spec: func(t *testing.T) (Event, Spec) {
				return Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "k-" + uuid.NewString()}, Spec{
					OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
					Params: mockModelParams(t), Messages: userSeed("hi"),
				}
			},
		},
		{
			name: "chat lookup on a pending start",
			repo: &failingLookups{Repository: repo, failChat: true},
			spec: func(t *testing.T) (Event, Spec) {
				chatID := pendingBranch(t, repo, ctx, projectID, mainWorktreeID)
				return chatStartSpec(t, chatID, "builtin://agent", "hi")
			},
		},
		{
			name: "workflow draft lookup",
			repo: &failingLookups{Repository: repo, failWorkflow: true},
			spec: func(t *testing.T) (Event, Spec) {
				return Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "k-" + uuid.NewString()}, Spec{
					OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "my-custom-workflow",
					Params: mockModelParams(t), Messages: userSeed("hi"),
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			starter := &fakeStarter{}
			launcher, _ := newTestLauncher(t, tc.repo, starter)
			ev, spec := tc.spec(t)
			_, err := launcher.Launch(ctx, ev, spec)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInternal, "a store failure is retryable")
			assert.NotErrorIs(t, err, ErrNotFound)
			var validation *ValidationError
			assert.False(t, errors.As(err, &validation), "a store failure is not a validation verdict")
			assert.Empty(t, starter.startedIDs())
		})
	}
}

// The other side of M1: a project that really is absent is final.
func TestLaunchMissingProjectIsNotFound(t *testing.T) {
	repo, ctx, _, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindSchedule, DedupeKey: "k-" + uuid.NewString()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: "no-such-project", Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.ErrorIs(t, err, ErrNotFound)
}
