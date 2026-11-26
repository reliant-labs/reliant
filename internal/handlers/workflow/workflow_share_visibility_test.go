package workflow

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/viewertest"
	"github.com/reliant-labs/reliant/internal/workflowdraftstest"
)

// A saved workflow's sharing state is on its owner's list and get answers, so
// the web can offer "Share workflow…" and say whether it is shared: PRIVATE
// until SetWorkflowDraftSharing shares it, ORG after, with the org it belongs
// to. A viewer of the org reads none of it here: a saved workflow reaches
// another member only through ListSharedWorkflowDrafts.
func TestSavedWorkflowVisibility_OwnerReadsIt(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	drafts := workflowdraftstest.New(t, repo)
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo, Triggers: &MockTriggers{}, Drafts: drafts, Projects: projectaccesstest.For(repo), Orgs: f.Membership}))

	org := viewertest.OrgID
	created, err := svc.CreateWorkflowDraft(f.Owner, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{
		ProjectId: f.ProjectID, Title: "Team Flow", OrgId: &org,
	}))
	require.NoError(t, err)
	draftID, slug := created.Msg.DraftId, created.Msg.Slug

	includeDrafts := true // a new saved workflow is a draft
	listed := func() *reliantv1.WorkflowListItem {
		t.Helper()
		resp, err := svc.ListWorkflows(f.Owner, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: f.ProjectID, IncludeHidden: &includeDrafts}))
		require.NoError(t, err)
		for _, w := range resp.Msg.Workflows {
			if w.GetDraftId() == draftID {
				return w
			}
		}
		t.Fatalf("saved workflow %s not listed", draftID)
		return nil
	}
	gotByID := func(ctx context.Context) (*reliantv1.GetWorkflowResponse, error) {
		resp, err := svc.GetWorkflow(ctx, connect.NewRequest(&reliantv1.GetWorkflowRequest{ProjectId: f.ProjectID, DraftId: &draftID}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	gotByName := func() *reliantv1.GetWorkflowResponse {
		t.Helper()
		resp, err := svc.GetWorkflow(f.Owner, connect.NewRequest(&reliantv1.GetWorkflowRequest{ProjectId: f.ProjectID, Name: slug}))
		require.NoError(t, err)
		return resp.Msg
	}

	private := reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_PRIVATE
	shared := reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_ORG
	item := listed()
	assert.Equal(t, private, item.GetVisibility(), "listed before sharing")
	assert.Equal(t, org, item.GetOrgId())
	byID, err := gotByID(f.Owner)
	require.NoError(t, err)
	assert.Equal(t, private, byID.GetVisibility(), "got by id before sharing")
	assert.Equal(t, org, byID.GetOrgId())
	assert.Equal(t, private, gotByName().GetVisibility(), "got by name before sharing")

	// Shared the way SetWorkflowDraftSharing shares it.
	draft, err := drafts.GetWorkflowDraft(f.Owner, draftID)
	require.NoError(t, err)
	change, err := authz.WorkflowShareChange(f.Owner, f.OwnerID, draft, core.VisibilityOrg)
	require.NoError(t, err)
	require.NoError(t, repo.RunTx(f.Owner, orm.ReadCommitted, func(ctx context.Context) error {
		_, err := store.ApplyShareChange(ctx, repo.ORM(), change)
		return err
	}))

	assert.Equal(t, shared, listed().GetVisibility(), "listed after sharing")
	byID, err = gotByID(f.Owner)
	require.NoError(t, err)
	assert.Equal(t, shared, byID.GetVisibility(), "got by id after sharing")
	byName := gotByName()
	assert.Equal(t, shared, byName.GetVisibility(), "got by name after sharing")
	assert.Equal(t, org, byName.GetOrgId())

	// Another member of the org reads the shared workflow only through
	// ListSharedWorkflowDrafts: here it is not found, by id or by name.
	_, err = gotByID(f.Viewer)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a viewer's get by id: %v", err)
	_, err = svc.GetWorkflow(f.Viewer, connect.NewRequest(&reliantv1.GetWorkflowRequest{Name: slug}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a viewer's get by name: %v", err)
}

// Builtin and project workflows are nobody's to share: no visibility, no org.
func TestBuiltinWorkflowVisibility_Unspecified(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo, Triggers: &MockTriggers{}, Drafts: workflowdraftstest.New(t, repo), Projects: projectaccesstest.For(repo), Orgs: f.Membership}))

	resp, err := svc.GetWorkflow(f.Owner, connect.NewRequest(&reliantv1.GetWorkflowRequest{ProjectId: f.ProjectID, Name: "builtin://agent"}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_UNSPECIFIED, resp.Msg.GetVisibility())
	assert.Nil(t, resp.Msg.OrgId)

	list, err := svc.ListWorkflows(f.Owner, connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: f.ProjectID}))
	require.NoError(t, err)
	for _, w := range list.Msg.Workflows {
		if w.Source == "builtin" {
			assert.Equal(t, reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_UNSPECIFIED, w.GetVisibility(), w.Name)
			assert.Nil(t, w.OrgId, w.Name)
		}
	}
}
