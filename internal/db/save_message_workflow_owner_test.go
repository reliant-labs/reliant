package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// A message saved to a spawned thread must carry THAT thread's workflow, not
// the caller's.
//
// SendMessage passes the workflow it is operating on, which for a message
// addressed to a spawn is the parent's root workflow — a spawn runs inline
// inside the parent and has no Temporal execution of its own. Storing the
// parent's id made the thread look like it had switched workflows mid-stream,
// and the timeline draws its "handoff" divider on exactly that signal. A
// "continue" typed into a spawn produced a spurious "Agent handoff" marker.
func TestSaveMessageToThread_UsesThreadsOwnWorkflow(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	parentWorkflowID := chatID
	spawnThreadID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "handoff", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: parentWorkflowID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &Thread{
		ID: spawnThreadID, ChatID: chatID, ParentThreadID: &parentWorkflowID,
		Origin: ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID: parentWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: parentWorkflowID, Status: WorkflowStatusRunning, CreatedAt: now,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID: spawnThreadID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: spawnThreadID, Status: WorkflowStatusRunning, CreatedAt: now,
	}))

	// The caller hands over the PARENT's workflow, as SendMessage does.
	saved, err := repo.SaveMessageToThread(ctx, chatID, spawnThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "continue", &parentWorkflowID, nil, nil)
	require.NoError(t, err)

	stored, err := repo.GetMessage(ctx, saved.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.WorkflowID)
	require.Equal(t, spawnThreadID, *stored.WorkflowID,
		"a message on a spawned thread must name that thread's workflow, or the timeline reads it as a handoff")
}

// A message on the main thread keeps the caller's workflow — the common path
// must be unchanged.
func TestSaveMessageToThread_MainThreadKeepsCallerWorkflow(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "main", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{
		ID: mainThreadID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: mainThreadID, Status: WorkflowStatusRunning, CreatedAt: now,
	}))

	saved, err := repo.SaveMessageToThread(ctx, chatID, mainThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "hello", &mainThreadID, nil, nil)
	require.NoError(t, err)

	stored, err := repo.GetMessage(ctx, saved.ID)
	require.NoError(t, err)
	require.Equal(t, mainThreadID, *stored.WorkflowID)
}

// A thread with no workflow row yet keeps the caller's value rather than
// losing the attribution entirely.
func TestSaveMessageToThread_NoWorkflowRowKeepsCallerValue(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	parentWorkflowID := chatID
	freshThreadID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "fresh", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: parentWorkflowID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &Thread{
		ID: freshThreadID, ChatID: chatID, ParentThreadID: &parentWorkflowID,
		Origin: ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	saved, err := repo.SaveMessageToThread(ctx, chatID, freshThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "first", &parentWorkflowID, nil, nil)
	require.NoError(t, err)

	stored, err := repo.GetMessage(ctx, saved.ID)
	require.NoError(t, err)
	require.Equal(t, parentWorkflowID, *stored.WorkflowID)
}
