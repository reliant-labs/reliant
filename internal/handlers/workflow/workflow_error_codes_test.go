package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
)

// readFailures is what a repository call can fail with, and the code the
// client reads for each: a classified error keeps its own code, and an
// opaque one is Internal with the handler's summary and none of its text.
var readFailures = []struct {
	name string
	err  error
	want connect.Code
}{
	{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
	{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
}

// The workflow reads (the project check, a draft by slug, a draft by id)
// pass a classified repository error through and redact an opaque one; the
// driver's text never reaches the client.
func TestWorkflowRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range readFailures {
		t.Run("ListWorkflows, checking the project: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := draftsRepo()
			repo.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) { return nil, tc.err }
			svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: repo}))
			_, err := svc.ListWorkflows(workflow.AuthedContext(t), connect.NewRequest(&pb.ListWorkflowsRequest{ProjectId: "p-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ExportWorkflow, reading the draft: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := draftsRepo()
			repo.GetWorkflowDraftBySlugFunc = func(context.Context, string, string) (*db.WorkflowDraft, error) { return nil, tc.err }
			svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: repo}))
			_, err := svc.ExportWorkflow(workflow.AuthedContext(t), connect.NewRequest(&pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "flow"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("SetWorkflowStatus, reading the draft: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := draftsRepo()
			repo.GetWorkflowDraftFunc = func(context.Context, string) (*db.WorkflowDraft, error) { return nil, tc.err }
			svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: repo}))
			_, err := svc.SetWorkflowStatus(workflow.AuthedContext(t), connect.NewRequest(&pb.SetWorkflowStatusRequest{
				ProjectId: "p-1", DraftId: "wf-flow", Status: pb.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT,
			}))
			requireClientReads(t, err, tc.want)
		})
	}
}

// A draft id the store has no row for is NotFound, as the store reports it.
// It used to be Internal, wrapping the store's not-found text.
func TestSetWorkflowStatus_MissingDraftIsNotFound(t *testing.T) {
	t.Parallel()

	repo := draftsRepo()
	repo.GetWorkflowDraftFunc = func(_ context.Context, id string) (*db.WorkflowDraft, error) {
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("workflow draft"), id)
	}
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: repo}))
	_, err := svc.SetWorkflowStatus(workflow.AuthedContext(t), connect.NewRequest(&pb.SetWorkflowStatusRequest{
		ProjectId: "p-1", DraftId: "wf-missing", Status: pb.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT,
	}))
	requireClientReads(t, err, connect.CodeNotFound)
}

// A slug another draft took first is AlreadyExists, from the store's own
// db.ErrWorkflowSlugTaken rather than a handler branch.
func TestCreateWorkflowDraft_SlugTakenIsAlreadyExists(t *testing.T) {
	t.Parallel()

	repo := draftsRepo()
	repo.GetWorkflowDraftByNameFunc = func(context.Context, string, string) (*db.WorkflowDraft, error) { return nil, nil }
	repo.GetProjectWorkflowsJSONFunc = func(context.Context, string) (*string, error) { return nil, nil }
	repo.CreateWorkflowDraftFunc = func(_ context.Context, d *db.WorkflowDraft) error {
		return fmt.Errorf("%w: %q", db.ErrWorkflowSlugTaken, d.Slug)
	}
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: repo}))
	_, err := svc.CreateWorkflowDraft(workflow.AuthedContext(t), connect.NewRequest(&pb.CreateWorkflowDraftRequest{ProjectId: "p-1"}))
	requireClientReads(t, err, connect.CodeAlreadyExists)
	require.Equal(t, "workflow slug already taken", svcerr.ToConnect(err).Message())
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}
