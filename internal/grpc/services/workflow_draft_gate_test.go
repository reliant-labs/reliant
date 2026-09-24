package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A draft is never runnable (specs/workflow-draft-lifecycle.md). The request-
// side resolvers that start runs refuse a draft by slug with an actionable
// "is a draft" message: CreateChat, the scenario runner's workflow loader, and
// preset resolution. (ref/spawn/router resolution at run time is
// ActivityLoadWorkflow; see load_workflow_draft_gate_test.go.)
func TestDraftWorkflowIsNeverRunnable(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "test-project-draft-gate-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Draft Gate",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	// Valid, so the only thing standing between it and running is its status.
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user", Name: "gate-draft", Slug: "gate-draft",
		Definition: `name: gate-draft
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`,
		Status: db.WorkflowDraftStatusDraft, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))

	t.Run("CreateChat", func(t *testing.T) {
		chatService := &ChatService{database: repo}
		_, err := chatService.CreateChat(ctx, connect.NewRequest(&reliantv1.CreateChatRequest{
			ProjectId: projectID,
			Workflow:  "gate-draft",
			Messages:  []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "hello"}},
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a draft")
		chats, listErr := repo.ListChats(ctx, db.ChatFilters{ProjectID: &projectID})
		require.NoError(t, listErr)
		assert.Empty(t, chats, "no chat is created for a draft workflow")
	})

	t.Run("scenario workflow loader", func(t *testing.T) {
		_, err := createScenarioWorkflowLoader(repo, ctx, "test-user", projectID)("gate-draft")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a draft")
	})

	t.Run("preset workflow resolution", func(t *testing.T) {
		presetService := &PresetService{database: repo}
		_, err := presetService.loadWorkflow(ctx, "gate-draft", projectID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a draft")
	})
}
