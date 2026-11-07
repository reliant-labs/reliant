package scenario_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/ptr"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
)

// The fixture behind the scaffold tests, which need no database: one project
// and one workflow the caller ("test-user", AuthedContext's default
// principal) owns, a saved scenario of theirs on that workflow, and one
// another user owns. Any other ID is missing, answered as the store answers
// it: a project is NotFound, a draft or a scenario is (nil, nil).
const (
	ownProjectID     = "project-own"
	ownScenarioID    = "scenario-own"
	otherScenarioID  = "scenario-other"
	ownWorkflowSlug  = "tail-flow"
	ownWorkflowDraft = "draft-own"
	missingID        = "missing"
)

// fixtureWorkflow parses as a workflow whose one node a scenario can mock.
const fixtureWorkflow = `name: tail-flow
apiVersion: "1.0"
entry: [draft]
nodes:
  - id: draft
    type: call_llm
    args:
      model: mock
edges: []
`

// fixtureScenario is one scenario file for fixtureWorkflow.
const fixtureScenario = `name: drafts
events:
  - node: draft
    output:
      response_text: done
expect:
  outcome: completed
  reached: [draft]
`

// fixture is the rows newScenarioRepo serves. Rows read created and deleted
// back from it.
type fixture struct {
	created []*db.WorkflowScenario
	deleted []string
}

// newScenarioRepo returns a StubRepo answering the fixture's project,
// workflow and scenario lookups. The project-synced scenarios column
// (GetProjectScenariosJSON) is not on the StubRepo, so a row that reaches a
// project scenario is answered Internal, as any repository that cannot read
// it is.
// scenarioDaemon holds ownProjectID's checkout (newScenarioRepo).
const scenarioDaemon = "daemon-1"

func newScenarioRepo(f *fixture) *scenario.MockRepository {
	scenarios := map[string]*db.WorkflowScenario{
		ownScenarioID: {
			ID: ownScenarioID, UserID: "test-user", Name: "Own scenario", Events: fixtureScenario,
			WorkflowDraftID: ptr.Of(ownWorkflowDraft),
		},
		otherScenarioID: {
			ID: otherScenarioID, UserID: "someone-else", Name: "Other scenario", Events: fixtureScenario,
			WorkflowDraftID: ptr.Of("draft-other"),
		},
	}
	draft := &db.WorkflowDraft{ID: ownWorkflowDraft, UserID: "test-user", Slug: ownWorkflowSlug, Definition: fixtureWorkflow}
	return &scenario.MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == ownProjectID && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Path: "/home/test/project"}, nil
			}
			return nil, core.ErrProjectNotFound
		},
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if id == ownProjectID {
				// projects.path is the creating machine's; the checkout this
				// store records is /home/test/project on scenarioDaemon.
				return &db.Project{ID: id, UserID: "test-user", Path: "/Users/creator/project"}, nil
			}
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("project"), id)
		},
		ListProjectDaemonsForProjectFunc: func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
			return []*core.ProjectDaemon{{ProjectID: projectID, DaemonID: scenarioDaemon, Path: "/home/test/project", InstallState: core.ProjectInstallInstalled}}, nil
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			return &db.Daemon{ID: id, UserID: "test-user"}, nil
		},
		GetWorkflowDraftBySlugFunc: func(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
			if userID == "test-user" && slug == ownWorkflowSlug {
				return draft, nil
			}
			return nil, nil
		},
		ListWorkflowScenariosByDraftFunc: func(_ context.Context, draftID string) ([]*db.WorkflowScenario, error) {
			var out []*db.WorkflowScenario
			for _, s := range scenarios {
				if ptr.From(s.WorkflowDraftID) == draftID {
					out = append(out, s)
				}
			}
			return out, nil
		},
		GetWorkflowScenarioFunc: func(_ context.Context, id string) (*db.WorkflowScenario, error) {
			if s, ok := scenarios[id]; ok {
				return s, nil
			}
			return nil, nil
		},
		CreateWorkflowScenarioFunc: func(_ context.Context, s *db.WorkflowScenario) error {
			f.created = append(f.created, s)
			return nil
		},
		DeleteWorkflowScenarioFunc: func(_ context.Context, id string) error {
			f.deleted = append(f.deleted, id)
			return nil
		},
	}
}

// placeAsNamed places every named machine as itself (daemonplacement.Machines).
func placeAsNamed(_ context.Context, _, daemonID string) (string, error) {
	if daemonID == "" {
		return scenarioDaemon, nil
	}
	return daemonID, nil
}
