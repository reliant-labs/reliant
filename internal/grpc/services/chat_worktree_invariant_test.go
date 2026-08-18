package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// Every chat must carry a resolvable worktree_id. The UI groups the chat list
// by worktree and drops chats whose worktree does not resolve, so a chat
// persisted with a null or dangling worktree_id runs to completion while
// staying invisible — what `reliant workflow run` produced before
// resolveChatWorktreeID gated the write.

// newInvariantTestProject creates a project, optionally with its main worktree,
// and returns (projectID, mainWorktreeID). mainWorktreeID is empty when
// withMainWorktree is false.
func newInvariantTestProject(t *testing.T, ctx context.Context, repo *db.Repo, withMainWorktree bool) (string, string) {
	t.Helper()

	now := time.Now().UTC()
	projectID := "wt-invariant-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Worktree Invariant Project",
		Path:       t.TempDir(),
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	if !withMainWorktree {
		return projectID, ""
	}

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
	return projectID, mainWorktreeID
}

func TestResolveChatWorktreeID_DefaultsToMainWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, mainWorktreeID := newInvariantTestProject(t, ctx, repo, true)
	service := &ChatService{database: repo}

	// A caller that names no worktree (the CLI) binds to the main worktree
	// rather than persisting null.
	resolved, err := service.resolveChatWorktreeID(ctx, projectID, nil)
	require.NoError(t, err)
	require.NotNil(t, resolved, "worktree_id must never resolve to null")
	require.Equal(t, mainWorktreeID, *resolved)

	// An explicitly empty string is the same "unset" intent as nil.
	empty := ""
	resolved, err = service.resolveChatWorktreeID(ctx, projectID, &empty)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, mainWorktreeID, *resolved)
}

func TestResolveChatWorktreeID_KeepsValidRequestedWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, mainWorktreeID := newInvariantTestProject(t, ctx, repo, true)

	// A second, non-main worktree in the same project — a "branched chat".
	now := time.Now().UTC()
	branchWorktreeID := uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID:         branchWorktreeID,
		Name:       "feature",
		Path:       t.TempDir(),
		Branch:     "feature/x",
		BaseBranch: "main",
		ProjectID:  projectID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	service := &ChatService{database: repo}

	resolved, err := service.resolveChatWorktreeID(ctx, projectID, &branchWorktreeID)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, branchWorktreeID, *resolved,
		"an explicit worktree must be preserved, not replaced by main")
	require.NotEqual(t, mainWorktreeID, *resolved)
}

func TestResolveChatWorktreeID_RejectsUnknownWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, _ := newInvariantTestProject(t, ctx, repo, true)
	service := &ChatService{database: repo}

	missing := uuid.NewString()
	_, err := service.resolveChatWorktreeID(ctx, projectID, &missing)
	require.Error(t, err, "a dangling worktree_id must be refused, not silently accepted")

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestResolveChatWorktreeID_RejectsForeignProjectWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, _ := newInvariantTestProject(t, ctx, repo, true)
	otherProjectID, otherMainWorktreeID := newInvariantTestProject(t, ctx, repo, true)
	require.NotEqual(t, projectID, otherProjectID)

	service := &ChatService{database: repo}

	// Binding a chat to another project's worktree would run it against the
	// wrong tree and look like it worked.
	_, err := service.resolveChatWorktreeID(ctx, projectID, &otherMainWorktreeID)
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

func TestResolveChatWorktreeID_RejectsProjectWithoutMainWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, _ := newInvariantTestProject(t, ctx, repo, false)
	service := &ChatService{database: repo}

	// The buggy state is refused rather than written as null.
	_, err := service.resolveChatWorktreeID(ctx, projectID, nil)
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	require.Contains(t, connectErr.Message(), "no main worktree")
}

// TestChatService_CreateChat_PersistsMainWorktreeWhenOmitted is the regression
// test for the CLI path: `reliant workflow run` sends no worktree_id, and the
// chat it creates must still land with one.
func TestChatService_CreateChat_PersistsMainWorktreeWhenOmitted(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, mainWorktreeID := newInvariantTestProject(t, ctx, repo, true)

	now := time.Now().UTC()
	trivialWorkflow := strings.TrimSpace(`
name: worktree-invariant-workflow
apiVersion: v2
inputs:
  prompt:
    type: string
    default: hi
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo {{inputs.prompt}}"
`)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     "test-user",
		Name:       "Worktree Invariant Workflow",
		Slug:       "worktree-invariant-workflow",
		Definition: trivialWorkflow,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
		Version:    1,
	}))

	temporal := &atomicityTestTemporalClient{}
	service := &ChatService{
		database:   repo,
		threads:    threads.NewService(repo),
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	// No WorktreeId, exactly as the CLI sends it.
	resp, err := service.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
		ProjectId: projectID,
		Workflow:  "worktree-invariant-workflow",
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "hello",
		}},
	}))
	require.NoError(t, err, "omitting worktree_id must not be rejected")
	require.Equal(t, mainWorktreeID, resp.Msg.GetChat().GetWorktreeId(),
		"the response must report the resolved worktree")

	chats, listErr := repo.ListChats(ctx, db.ChatFilters{
		UserID:    "test-user",
		ProjectID: &projectID,
		Limit:     10,
	})
	require.NoError(t, listErr)
	require.Len(t, chats, 1, "the chat should have been persisted")
	require.NotNil(t, chats[0].WorktreeID,
		"a CLI-created chat must not persist a null worktree_id — the sidebar would hide it")
	require.Equal(t, mainWorktreeID, *chats[0].WorktreeID)
}
