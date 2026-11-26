package workflow_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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

// draftsStore is the repository and the saved workflows a handler test runs
// against: both are mocks, and deps() wires them as one Deps.
type draftsStore struct {
	*workflow.MockRepository
	Triggers *workflow.MockTriggers
	Projects projectaccess.Service
	Drafts   *workflow.MockDrafts
}

func (s *draftsStore) deps() workflow.Deps {
	return workflow.Deps{Repo: s.MockRepository, Triggers: s.Triggers, Projects: s.Projects, Drafts: s.Drafts}
}

// draftsRepo is a store holding these drafts of the harness caller, in a
// project p-1 the caller owns. A slug it does not hold is absent (nil, nil),
// as the repository answers; any other project is not found.
func draftsRepo(drafts ...*db.WorkflowDraft) *draftsStore {
	bySlug := map[string]*db.WorkflowDraft{}
	for _, d := range drafts {
		bySlug[d.Slug] = d
	}
	owned := func(_ context.Context, id, userID string) (*db.Project, error) {
		if id == "p-1" && userID == "test-user" {
			return &db.Project{ID: id, UserID: userID, Name: "proj"}, nil
		}
		return nil, core.ErrProjectNotFound
	}
	return &draftsStore{
		MockRepository: &workflow.MockRepository{},
		Triggers:       &workflow.MockTriggers{},
		Projects:       projectaccesstest.Func(owned),
		Drafts: &workflow.MockDrafts{
			GetWorkflowDraftBySlugFunc: func(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
				if d, ok := bySlug[slug]; ok && userID == d.UserID {
					return d, nil
				}
				return nil, nil
			},
		},
	}
}

// withAutomations is deps with the automations retirer a delete stops
// schedules through.
func withAutomations(deps workflow.Deps, r workflow.AutomationRetirer) workflow.Deps {
	deps.Automations = r
	return deps
}
