package run_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// emptyRunRepo is a repository holding no run, session or project, answering
// each lookup as the store does for a missing row: a run or a session is
// NotFound, and the caller has no projects.
func emptyRunRepo() *run.MockRepository {
	return &run.MockRepository{
		GetWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("workflow"), id)
		},
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
		},
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
		},
		ListProjectsFunc: func(context.Context, db.ProjectFilters) ([]*db.Project, error) {
			return nil, nil
		},
	}
}
