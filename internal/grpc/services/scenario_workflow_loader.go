package services

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// createScenarioWorkflowLoader resolves the refs a scenario reaches the way a
// run resolves them (workflowsource): the user's own workflows, then the
// project's synced workflows by name: — the rule `reliant workflow scenario
// run` applies to the same files on disk.
func createScenarioWorkflowLoader(repo db.Repository, ctx context.Context, userID, projectID string) func(string) (*reliantv1.Workflow, error) {
	return workflowsource.Loader(ctx, repo, workflowsource.Options{UserID: userID, ProjectID: projectID})
}
