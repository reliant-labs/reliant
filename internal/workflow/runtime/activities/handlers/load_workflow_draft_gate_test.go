// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A draft is never runnable (specs/workflow-draft-lifecycle.md). Every
// execution path — run start, `ref:` children, spawns, routers, loops —
// resolves user workflows through ActivityLoadWorkflow, so this activity is the
// gate. A draft referenced by slug fails with an actionable "is a draft"
// message, not a bare "not found" for a workflow the user can see in their list.
func TestLoadWorkflowActivity_RefusesDraftBySlug(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	now := time.Now().UTC()

	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Draft Gate", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, ProjectID: projectID, Title: "test",
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	createWorkflow := func(slug, definition string, status db.WorkflowDraftStatus) {
		require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: userID, Name: slug, Slug: slug,
			Definition: definition, Status: status, CreatedAt: now, UpdatedAt: now,
		}))
	}
	leaf := func(name string) string {
		return "name: " + name + `
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`
	}

	createWorkflow("gate-draft", leaf("gate-draft"), db.WorkflowDraftStatusDraft)
	createWorkflow("gate-complete", leaf("gate-complete"), db.WorkflowDraftStatusComplete)
	// A complete parent whose `ref:` child is still a draft (the child was
	// moved back to draft after the parent was marked complete).
	createWorkflow("gate-parent", `name: gate-parent
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: gate-draft
`, db.WorkflowDraftStatusComplete)

	activity := NewLoadWorkflowActivity(repo)

	t.Run("a complete workflow loads", func(t *testing.T) {
		out, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "gate-complete"})
		require.NoError(t, err)
		assert.NotEmpty(t, out.YAML)
	})

	t.Run("a draft by slug is refused as a draft", func(t *testing.T) {
		_, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "gate-draft"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a draft")
		assert.Contains(t, err.Error(), "mark it complete")
	})

	t.Run("a complete parent that refs a draft child is refused", func(t *testing.T) {
		_, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "gate-parent"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a draft")
	})
}
