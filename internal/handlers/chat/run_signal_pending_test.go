// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// A pending run has never started, and SendMessage refuses to start it, so a
// signal to it cannot be delivered. It must say so (delivered=false) rather than
// surface SendMessage's FailedPrecondition.
func TestSignalRunOnPendingRunIsNotDelivered(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "signal-pending-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Signal Pending", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	workflowName := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "pending", ProjectID: projectID,
		WorkflowName: &workflowName, State: db.ChatStateIdle, WorkflowID: &chatID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	threadsSvc := threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())})
	_, _, _, err := threadsSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: chatID, ChatID: chatID, WorkflowName: workflowName, Thread: chatID,
			Status: db.Pending(), CreatedAt: now,
		},
		ThreadID: chatID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	chats := &Service{deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}, threads: threadsSvc}
	runs, err := run.New(run.Deps{
		Logger:    slog.Default(),
		Config:    &typedconfig.Config{},
		Repo:      repo,
		RunLists:  store.NewRunListExtStore(repo.ORM()),
		Workflows: store.NewWorkflowExtStore(repo.ORM()),
		ChatRows:  store.NewChatExtStore(repo.ORM()),
		ChatViews: store.NewChatsWithActivityExtStore(repo.ORM()),
		Chats:     chats,
	})
	require.NoError(t, err)

	resp, err := runs.SignalRun(ctx, connect.NewRequest(&reliantv1.SignalRunRequest{
		RunId: chatID,
		Messages: []*reliantv1.InputMessage{
			{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "hello?"},
		},
	}))
	require.NoError(t, err, "a pending run reports delivered=false, never an error")
	require.False(t, resp.Msg.Delivered)
}
