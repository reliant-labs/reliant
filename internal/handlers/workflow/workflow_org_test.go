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
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// leftGlobex: test-user is a live member of acme, and has left globex.
type leftGlobex struct{}

func (leftGlobex) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return userID == "test-user" && orgID == "acme", true, nil
}

// A saved workflow in an org its owner has left can be neither read nor
// updated, whichever key reaches it: GetWorkflow by name, SaveWorkflow by
// draft id, and SaveWorkflow by the same name (the slug fallback). Its
// definition is unchanged afterwards. A workflow of an org they are still in
// saves as before.
func TestWorkflow_RemovedMemberLosesTheOrgsWorkflow(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	ctx = authz.WithMembership(ctx, leftGlobex{})

	create := func(name, org string) *db.WorkflowDraft {
		now := time.Now().UTC()
		draft := &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: "test-user", OrgID: &org, Name: name, Slug: generateSlug(name),
			Definition: validLifecycleWorkflow, Status: db.WorkflowDraftStatusDraft,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
		return draft
	}
	suffix := uuid.NewString()[:8]
	left := create("left-flow-"+suffix, "globex")
	kept := create("kept-flow-"+suffix, "acme")

	_, err := svc.GetWorkflow(ctx, connect.NewRequest(&reliantv1.GetWorkflowRequest{Name: left.Name, ProjectId: projectID}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "GetWorkflow by name: %v", err)

	save := func(name string, draftID *string) error {
		wf := mustParse(t, validLifecycleWorkflow)
		wf.Name = name
		wf.Description = "overwritten"
		_, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
			ProjectId: projectID, Workflow: wf, DraftId: draftID,
		}))
		return err
	}
	require.Equal(t, connect.CodeNotFound, svcerr.Code(save(left.Name, &left.ID)), "SaveWorkflow by draft id")
	require.Equal(t, connect.CodeNotFound, svcerr.Code(save(left.Name, nil)), "SaveWorkflow by the same name")

	stored, err := repo.GetWorkflowDraft(ctx, left.ID)
	require.NoError(t, err)
	require.Equal(t, left.Definition, stored.Definition, "the removed member's saves must not reach the row")
	require.Equal(t, left.Version, stored.Version)

	require.NoError(t, save(kept.Name, &kept.ID), "a live member's workflow saves")
	got, err := svc.GetWorkflow(ctx, connect.NewRequest(&reliantv1.GetWorkflowRequest{Name: kept.Name, ProjectId: projectID}))
	require.NoError(t, err)
	require.Equal(t, kept.ID, got.Msg.GetDraftId())
}
