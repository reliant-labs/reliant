// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
)

// userStreamRaceRepo commits another write to the user's update stream from
// outside BranchChat's transaction just before the branch announces itself on
// that stream, every time. That is what a running chat of the same user does
// all the time.
type userStreamRaceRepo struct {
	Repository
	real     *db.Repo
	attempts atomic.Int32
}

func (r *userStreamRaceRepo) CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error {
	if update.UpdateType == db.UserUpdateChatCreated {
		r.attempts.Add(1)
		// Committed on its own connection, after the branch's transaction
		// has read and written plenty: its snapshot predates this row.
		other := &db.UserUpdate{
			UserID:     update.UserID,
			UpdateType: db.UserUpdateChatActivityChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   uuid.NewString(),
			Data:       json.RawMessage(`{}`),
		}
		if err := r.real.CreateUserUpdate(context.Background(), other); err != nil {
			return err
		}
	}
	return r.Repository.CreateUserUpdate(ctx, update)
}

// branchTestTime is a fixed instant for the rows these tests seed.
var branchTestTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newBranchTestService(repo *db.Repo, role Repository) *Service {
	return &Service{
		deps: Deps{
			Repo: role, Chats: chataccesstest.For(role),
			Workflows:   store.NewWorkflowExtStore(repo.ORM()),
			Worktrees:   store.NewWorktreeExtStore(repo.ORM()),
			ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()),
			Questions: &MockQuestions{}, Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()),
			Mailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}),
			AgentMessages: store.NewAgentMessageExtStore(repo.ORM()),
		},
		threads: threads.New(threads.Deps{Repo: role}),
	}
}

func setupBranchProject(t *testing.T, repo *db.Repo, ctx context.Context) string {
	t.Helper()
	projectID := "test-project-branch-" + uuid.NewString()
	now := branchTestTime
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Branch Project", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return projectID
}

// TestBranchChat_ConcurrentUserStreamWriteDoesNotAbortTheBranch: a write
// another transaction commits to the user's update stream while a branch is
// being made does not abort the branch. Under SERIALIZABLE the branch's
// upsert of the user's stream counter failed with 40001 "could not serialize
// access due to concurrent update" whenever that happened, and every retry
// met the next write: prod trace 82a3f948 ran six attempts in 10.75s. At
// READ COMMITTED the upsert waits for the row and goes on, once.
func TestBranchChat_ConcurrentUserStreamWriteDoesNotAbortTheBranch(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := setupBranchProject(t, repo, ctx)
	sourceChatID, msgID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	racing := &userStreamRaceRepo{Repository: repo, real: repo}
	service := newBranchTestService(repo, racing)

	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)
	require.Equal(t, int32(1), racing.attempts.Load(), "the branch's transaction ran more than once")
}

// TestBranchChat_CopiesPlansAndTasksPending: the branch gets a copy of every
// plan of the source chat and of their tasks, reset to pending, with each
// task's parent mapped to its parent's copy, and the source's plans untouched.
func TestBranchChat_CopiesPlansAndTasksPending(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := setupBranchProject(t, repo, ctx)
	sourceChatID, msgID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	// Fixed instants: only their order matters.
	base := branchTestTime
	desc := "the description"
	complexity := int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE)
	older := &db.Plan{ID: uuid.NewString(), ThreadID: sourceChatID, Title: "older", Status: int32(db.PlanStatusCompleted), CreatedAt: base}
	newer := &db.Plan{ID: uuid.NewString(), ThreadID: sourceChatID, Title: "newer", Description: &desc, Complexity: &complexity, Status: int32(db.PlanStatusActive), CreatedAt: base.Add(time.Minute)}
	require.NoError(t, repo.CreatePlan(ctx, older))
	require.NoError(t, repo.CreatePlan(ctx, newer))
	parent := &db.Task{ID: uuid.NewString(), PlanID: newer.ID, Title: "parent", Status: int32(db.TaskStatusCompleted), Position: 0}
	require.NoError(t, repo.CreateTask(ctx, parent))
	require.NoError(t, repo.CreateTask(ctx, &db.Task{ID: uuid.NewString(), PlanID: newer.ID, ParentTaskID: &parent.ID, Title: "child", Description: &desc, Status: int32(db.TaskStatusInProgress), Position: 1}))
	require.NoError(t, repo.CreateTask(ctx, &db.Task{ID: uuid.NewString(), PlanID: older.ID, Title: "lone", Status: int32(db.TaskStatusCompleted), Position: 0}))

	service := newBranchTestService(repo, repo)
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)
	branchChatID := resp.Msg.Chat.Id

	plans, err := repo.ListPlansByChatID(ctx, branchChatID)
	require.NoError(t, err)
	require.Len(t, plans, 2)
	// Newest first, as in the source.
	require.Equal(t, "newer", plans[0].Title)
	require.Equal(t, "older", plans[1].Title)
	for _, p := range plans {
		require.Equal(t, branchChatID, p.ThreadID)
		require.NotEqual(t, newer.ID, p.ID)
		require.NotEqual(t, older.ID, p.ID)
		require.Equal(t, int32(db.PlanStatusPending), p.Status)
		require.Nil(t, p.CompletedAt)
	}
	require.Equal(t, desc, *plans[0].Description)
	require.Equal(t, complexity, *plans[0].Complexity)

	tasks, err := repo.ListTasksByPlan(ctx, plans[0].ID)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, "parent", tasks[0].Title)
	require.Nil(t, tasks[0].ParentTaskID)
	require.Equal(t, "child", tasks[1].Title)
	require.NotNil(t, tasks[1].ParentTaskID)
	require.Equal(t, tasks[0].ID, *tasks[1].ParentTaskID, "the child's parent is the parent's copy")
	require.Equal(t, desc, *tasks[1].Description)
	for _, task := range tasks {
		require.Equal(t, int32(db.TaskStatusPending), task.Status)
	}
	lone, err := repo.ListTasksByPlan(ctx, plans[1].ID)
	require.NoError(t, err)
	require.Len(t, lone, 1)
	require.Equal(t, "lone", lone[0].Title)

	// The source keeps its own plans as they were.
	sourcePlans, err := repo.ListPlansByChatID(ctx, sourceChatID)
	require.NoError(t, err)
	require.Len(t, sourcePlans, 2)
	require.Equal(t, newer.ID, sourcePlans[0].ID)
	require.Equal(t, int32(db.PlanStatusActive), sourcePlans[0].Status)
}
