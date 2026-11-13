package workflow

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
)

// forkMembers is acme: ada administers it, bob is a member, liv is a member
// who saved a workflow, and the user in left has left it.
type forkMembers struct{ left string }

func (m forkMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return orgID == "acme" && userID != m.left && userID != "olga", true, nil
}

func (m forkMembers) RoleFresh(ctx context.Context, userID, orgID string) (string, bool, error) {
	if member, _, _ := m.IsMember(ctx, userID, orgID); !member {
		return "", true, nil
	}
	if userID == "ada" {
		return "admin", true, nil
	}
	return "member", true, nil
}

// A saved workflow whose creator left its org is forked, never re-owned: an
// org admin gets a copy of their own in the same org, with forked_from naming
// the original, and the original stays its creator's. Only an admin forks,
// only an orphan is forked, and an outsider cannot tell it exists.
func TestForkOrphanedWorkflow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	acme := "acme"
	cora := "cora-" + uuid.NewString()[:8]
	members := forkMembers{left: cora}
	as := func(userID string) context.Context {
		ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
		return authz.WithMembership(auth.WithCredential(ctx, auth.CredentialSession), members)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	project := func(userID string) string {
		id := "p-" + uuid.NewString()
		require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
			ID: id, UserID: userID, OrgID: &acme, Name: "P", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		return id
	}
	const definition = "name: release\ntitle: Release\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n"
	saved := func(userID string) *db.WorkflowDraft {
		d := &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: userID, OrgID: &acme, Name: "release", Slug: "release",
			Definition: definition, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		require.NoError(t, repo.CreateWorkflowDraft(context.Background(), d))
		return d
	}
	orphan, livs := saved(cora), saved("liv")
	adasProject := project("ada")
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo}))
	fork := func(ctx context.Context, id, projectID string) (*reliantv1.ForkOrphanedWorkflowResponse, error) {
		resp, err := svc.ForkOrphanedWorkflow(ctx, connect.NewRequest(&reliantv1.ForkOrphanedWorkflowRequest{WorkflowId: id, ProjectId: projectID}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	reason := func(err error) string { return svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader) }

	_, err := fork(as("bob"), orphan.ID, project("bob"))
	assert.Equal(t, orgtakeover.ReasonOrgAdminRequired, reason(err), "a member who is not an admin: %v", err)
	_, err = fork(as("olga"), orphan.ID, adasProject)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "an outsider cannot tell it exists")
	_, err = fork(as("ada"), livs.ID, adasProject)
	assert.Equal(t, ReasonWorkflowNotOrphaned, reason(err), "a live member's workflow is theirs")

	got, err := fork(as("ada"), orphan.ID, adasProject)
	require.NoError(t, err)
	copied, err := repo.GetWorkflowDraft(context.Background(), got.GetId())
	require.NoError(t, err)
	assert.Equal(t, "ada", copied.UserID, "the fork is the admin's own")
	require.NotNil(t, copied.OrgID)
	assert.Equal(t, acme, *copied.OrgID, "in the original's org")
	require.NotNil(t, copied.ForkedFrom)
	assert.Equal(t, "orphan://"+orphan.ID, *copied.ForkedFrom)
	assert.Equal(t, got.GetSlug(), copied.Slug)
	assert.Contains(t, copied.Definition, "name: "+got.GetSlug(), "the fork's identity is its own slug")
	assert.Equal(t, db.WorkflowDraftStatusComplete, copied.Status, "it validates for the admin, so it keeps its status")

	original, err := repo.GetWorkflowDraft(context.Background(), orphan.ID)
	require.NoError(t, err)
	assert.Equal(t, cora, original.UserID, "the original is never re-owned")

	again, err := fork(as("ada"), orphan.ID, adasProject)
	require.NoError(t, err)
	assert.NotEqual(t, got.GetSlug(), again.GetSlug(), "a second fork gets a slug of its own")
}
