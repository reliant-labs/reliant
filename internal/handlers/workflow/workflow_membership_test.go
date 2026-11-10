package workflow

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// liveIn is a membership of the orgs it names only.
type liveIn map[string]bool

func (m liveIn) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

// A saved workflow of an org its owner has left cannot be read or saved over
// by any key the request carries: its draft id, or its name (which is its
// slug).
func TestWorkflowDraft_RemovedMemberByIDAndByName(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, "test-user"), liveIn{"home": true})
	now := time.Now().UTC()
	projectID := "test-project-member-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "P", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	globex := "globex"
	const definition = "name: left-flow\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n"
	left := &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user", OrgID: &globex, Name: "left-flow", Slug: "left-flow",
		Definition: definition, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, repo.CreateWorkflowDraft(ctx, left))
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo}))

	_, err := svc.GetWorkflow(ctx, connect.NewRequest(&reliantv1.GetWorkflowRequest{DraftId: &left.ID}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "get by draft id: %v", err)
	_, err = svc.GetWorkflow(ctx, connect.NewRequest(&reliantv1.GetWorkflowRequest{Name: left.Name}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "get by name: %v", err)

	edited := mustParse(t, "name: left-flow\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hijacked\"\n")
	_, err = svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: projectID, DraftId: &left.ID, Workflow: edited,
	}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "save by draft id: %v", err)
	_, err = svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: projectID, Workflow: edited,
	}))
	require.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err), "save by name: %v", err)

	kept, err := repo.GetWorkflowDraft(ctx, left.ID)
	require.NoError(t, err)
	require.Equal(t, definition, kept.Definition, "the denied draft is unchanged")
}
