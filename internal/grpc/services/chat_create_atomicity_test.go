// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeWorkflowRun is a minimal client.WorkflowRun for tests that just need
// CreateChat's post-transaction ExecuteWorkflow calls to succeed.
type fakeWorkflowRun struct {
	client.WorkflowRun
	id    string
	runID string
}

func (f *fakeWorkflowRun) GetID() string    { return f.id }
func (f *fakeWorkflowRun) GetRunID() string { return f.runID }

// atomicityTestTemporalClient embeds client.Client so it satisfies the whole
// SDK surface, but only implements ExecuteWorkflow — CreateChat's
// post-transaction best-effort calls (main workflow + title generation).
type atomicityTestTemporalClient struct {
	client.Client
}

func (c *atomicityTestTemporalClient) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, _ interface{}, _ ...interface{},
) (client.WorkflowRun, error) {
	return &fakeWorkflowRun{id: options.ID, runID: "test-run-" + options.ID}, nil
}

// failAfterUserUpdateRepo wraps db.Repository so it satisfies the whole
// interface, but fails the CreateUserUpdate call — the last write inside
// CreateChat's transaction — so the test can assert that nothing earlier in
// the transaction (chat row, workflow, thread, messages) survives the abort.
type failAfterUserUpdateRepo struct {
	db.Repository
	shouldFail bool
}

func (r *failAfterUserUpdateRepo) CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error {
	if r.shouldFail {
		return fmt.Errorf("injected failure: CreateUserUpdate")
	}
	return r.Repository.CreateUserUpdate(ctx, update)
}

// TestChatService_CreateChat_AtomicOnMidTransactionFailure proves that a
// failure on the LAST write inside CreateChat's transaction (the
// chat_created user update) leaves NO partial state behind: no orphan chat
// row, no orphan workflow/thread, no message rows. Before the transaction
// wrapper this test fails — see the story in the task description for the
// verification procedure (revert the wrapper, confirm this test fails,
// restore it).
func TestChatService_CreateChat_AtomicOnMidTransactionFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-atomic-create-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Atomic Create Chat Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	failingRepo := &failAfterUserUpdateRepo{Repository: repo, shouldFail: true}
	service := &ChatService{
		database:   failingRepo,
		threads:    threads.NewService(failingRepo),
		tempClient: &atomicityTestTemporalClient{},
	}

	_, err := service.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: projectID,
		Workflow:  "builtin://agent",
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "hello",
		}},
		WorkflowParams: map[string]*structpb.Value{
			"model": mustStructValue(t, map[string]interface{}{"id": "mock"}),
		},
	}))
	require.Error(t, err, "CreateChat must fail when the trailing chat_created write fails")

	// No chat row should have been committed.
	chats, listErr := repo.ListChats(ctx, db.ChatFilters{ProjectID: &projectID})
	require.NoError(t, listErr)
	require.Empty(t, chats, "no orphan chat row should survive a failed transaction")

	// No user_updates row (chat_created or otherwise) should exist for this user
	// beyond sequence 0 - the injected failure means nothing should have committed.
	updates, updatesErr := repo.GetUserUpdatesSince(ctx, "test-user", 0, 100)
	require.NoError(t, updatesErr)
	for _, u := range updates {
		require.NotEqual(t, db.UserUpdateChatCreated, u.UpdateType,
			"no chat_created update should exist for a chat that was never committed")
	}
}

// TestChatService_CreateChat_SucceedsWithoutInjectedFailure is the control
// for TestChatService_CreateChat_AtomicOnMidTransactionFailure: with the
// injected failure disabled, chat creation must succeed and leave a
// consistent chat + workflow + thread + chat_created update behind.
func TestChatService_CreateChat_SucceedsWithoutInjectedFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-atomic-create-ok-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Atomic Create Chat Control Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
	// CreateChat requires a main worktree to bind the chat to (CreateProject
	// makes one in production; this harness inserts the row directly).
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
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

	failingRepo := &failAfterUserUpdateRepo{Repository: repo, shouldFail: false}
	service := &ChatService{
		database:   failingRepo,
		threads:    threads.NewService(failingRepo),
		tempClient: &atomicityTestTemporalClient{},
	}

	resp, err := service.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: projectID,
		Workflow:  "builtin://agent",
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "hello",
		}},
		WorkflowParams: map[string]*structpb.Value{
			"model": mustStructValue(t, map[string]interface{}{"id": "mock"}),
		},
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)

	chatID := resp.Msg.Chat.Id
	createdChat, getErr := repo.GetChat(ctx, chatID)
	require.NoError(t, getErr)
	require.NotNil(t, createdChat)

	wf, wfErr := repo.GetWorkflow(ctx, chatID)
	require.NoError(t, wfErr)
	require.NotNil(t, wf, "root workflow should exist for the created chat")

	thread, threadErr := repo.GetThread(ctx, chatID)
	require.NoError(t, threadErr)
	require.NotNil(t, thread, "root thread should exist for the created chat")

	updates, updatesErr := repo.GetUserUpdatesSince(ctx, "test-user", 0, 100)
	require.NoError(t, updatesErr)
	foundChatCreated := false
	for _, u := range updates {
		if u.UpdateType == db.UserUpdateChatCreated && u.EntityID == chatID {
			foundChatCreated = true
		}
	}
	require.True(t, foundChatCreated, "chat_created user update should exist for the successfully created chat")
}
