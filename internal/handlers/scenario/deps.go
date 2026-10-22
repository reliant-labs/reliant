// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package scenario

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the scenario handlers use. The
// last three feed workflowsource.Loader and launch, which resolve the
// workflows a scenario runs.
type Repository interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	GetWorkflowDraft(ctx context.Context, id string) (*db.WorkflowDraft, error)
	GetWorkflowScenario(ctx context.Context, id string) (*db.WorkflowScenario, error)
	ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*db.WorkflowScenario, error)
	CreateWorkflowScenario(ctx context.Context, scenario *db.WorkflowScenario) error
	UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error
	DeleteWorkflowScenario(ctx context.Context, id string) error

	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error)
}

// Sender runs a command on the caller's daemon. toolexec.DaemonRouter
// implements it.
type Sender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}
