// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// runListFixture is the part of the run handlers' list fixture
// (internal/handlers/run/run_list_test.go) that inbox_test.go builds on: a
// user, their project, and seeded runs in a real database. The inbox tests
// never call the run service, so this copy has none. It moves with the Inbox
// lane (research/forge-migration/06-p3b-recipe.md).
type runListFixture struct {
	repo   *db.Repo
	ctx    context.Context
	userID string
}

func newRunListFixture(t *testing.T) *runListFixture {
	t.Helper()
	repo := db.NewTestRepo(t)
	userID := uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: "p-" + userID, UserID: userID, Name: "proj", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return &runListFixture{repo: repo, ctx: ctx, userID: userID}
}

func (f *runListFixture) project() string { return "p-" + f.userID }

func (f *runListFixture) seed(t *testing.T, id, workflow string, status db.WorkflowStatus, at time.Time) {
	t.Helper()
	workflowID := id
	require.NoError(t, f.repo.CreateChat(f.ctx, &db.Chat{
		ID: id, Title: "run " + id, ProjectID: f.project(), UserID: f.userID,
		WorkflowName: &workflow, WorkflowID: &workflowID, State: db.ChatStateIdle,
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}))
	_, err := f.repo.CreateThread(f.ctx, &db.Thread{ID: id, ChatID: id, CreatedAt: at})
	require.NoError(t, err)
	require.NoError(t, f.repo.CreateWorkflow(f.ctx, &db.Workflow{
		ID: id, ChatID: id, WorkflowName: workflow, Thread: id, Status: status, CreatedAt: at,
	}))
}
