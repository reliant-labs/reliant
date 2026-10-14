package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// callerDraft is a workflow the harness caller (test-user) owns under slug,
// whose definition is the builtin agent workflow.
func callerDraft(t *testing.T, slug string) *db.WorkflowDraft {
	t.Helper()
	def, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	if err != nil {
		t.Fatalf("reading the builtin agent workflow: %v", err)
	}
	return &db.WorkflowDraft{ID: "wf-" + slug, UserID: "test-user", Name: slug, Slug: slug, Definition: string(def), Status: db.WorkflowDraftStatusComplete}
}

// draftsRepo is a store holding these drafts of the harness caller, in a
// project p-1 the caller owns. A slug it does not hold is absent (nil, nil),
// as the repository answers; any other project is not found.
func draftsRepo(drafts ...*db.WorkflowDraft) *workflow.StubRepo {
	bySlug := map[string]*db.WorkflowDraft{}
	for _, d := range drafts {
		bySlug[d.Slug] = d
	}
	return &workflow.StubRepo{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == "p-1" && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj"}, nil
			}
			return nil, errors.New("project not found")
		},
		GetWorkflowDraftBySlugFunc: func(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
			if d, ok := bySlug[slug]; ok && userID == d.UserID {
				return d, nil
			}
			return nil, nil
		},
	}
}
