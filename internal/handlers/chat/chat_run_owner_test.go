// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
)

// The root run a chat is created with records its owner at creation. These go
// through the RPC rather than the store, because the RPC is the thing that can
// forget to set it.

func newRunOwnerProject(t *testing.T, ctx context.Context, repo *db.Repo) string {
	t.Helper()
	now := time.Now().UTC()
	projectID := "test-project-run-owner-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Run Owner Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return projectID
}

func requireRunOwner(t *testing.T, repo *db.Repo, runID, want string) {
	t.Helper()
	run, err := store.LoadWorkflow(context.Background(), repo.ORM(), runID)
	require.NoError(t, err)
	require.NotNil(t, run, "run %s was not created", runID)
	require.NotNil(t, run.OwnerUserID, "run %s was created without an owner", runID)
	require.Equal(t, want, *run.OwnerUserID)
}

func TestChatService_CreateChat_RootRunCarriesOwner(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	// StartChat refuses a project without a main worktree.
	projectID, _ := newInvariantTestProject(t, ctx, repo, true)

	temporal := &atomicityTestTemporalClient{}
	service := &Service{
		deps: Deps{
			Repo:        repo,
			Workflows:   store.NewWorkflowExtStore(repo.ORM()),
			Worktrees:   store.NewWorktreeExtStore(repo.ORM()),
			ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()),
			Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()),
			Mailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}),
			AgentMessages: store.NewAgentMessageExtStore(repo.ORM()),
			Temporal:      temporal,
		},
		threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}),
		runs:    runs.New(runs.Deps{Repo: repo, Resolver: questions.NewFromRepo(repo), Chats: store.NewChatsWithActivityExtStore(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Temporal: temporal, Pause: nil}),
	}

	resp, err := service.StartChat(ctx, connect.NewRequest(&reliantv1.StartChatRequest{
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

	// The root run's id is the chat's id.
	requireRunOwner(t, repo, resp.Msg.GetChat().GetId(), "test-user")
}

func TestChatService_BranchChat_RootRunCarriesOwner(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := newRunOwnerProject(t, ctx, repo)
	sourceChatID, msgID := setupBranchAtomicitySource(t, repo, ctx, projectID)

	service := &Service{
		deps: Deps{
			Repo:        repo,
			Workflows:   store.NewWorkflowExtStore(repo.ORM()),
			Worktrees:   store.NewWorktreeExtStore(repo.ORM()),
			ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()),
			Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()),
			Mailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}),
			AgentMessages: store.NewAgentMessageExtStore(repo.ORM()),
		},
		threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}),
	}
	resp, err := service.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    sourceChatID,
		MessageId: msgID,
	}))
	require.NoError(t, err)

	requireRunOwner(t, repo, resp.Msg.GetChat().GetId(), "test-user")
}
