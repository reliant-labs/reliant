package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The pipeline's enrolment and status reads (PipelineService). Each method
// is the internal/store query of the same name.

// ListPipelineReposByRepository lists the enrolments last observed as
// owner/name.
func (r *Repo) ListPipelineReposByRepository(ctx context.Context, provider, repository string) ([]*core.PipelineRepo, error) {
	return store.ListPipelineReposByRepository(ctx, r.ORM(), provider, repository)
}

// FreshIntegrationAccessByLabel lists the user's fresh grants of a resource
// last observed as label.
func (r *Repo) FreshIntegrationAccessByLabel(ctx context.Context, userID, integrationID, label string, freshAfter time.Time) ([]core.IntegrationAccessGrant, error) {
	return store.FreshIntegrationAccessByLabel(ctx, r.ORM(), userID, integrationID, label, freshAfter)
}

// CountPRPipelinesByState counts the repository's PRs in each state.
func (r *Repo) CountPRPipelinesByState(ctx context.Context, repoID string) ([]core.PRPipelineStateCount, error) {
	return store.CountPRPipelinesByState(ctx, r.ORM(), repoID)
}

// ListActiveQueueEntries lists the repository's waiting and batched entries.
func (r *Repo) ListActiveQueueEntries(ctx context.Context, repoID string) ([]*core.QueueEntryListing, error) {
	return store.ListActiveQueueEntries(ctx, r.ORM(), repoID)
}

// CurrentQueueBatch returns the repository's newest batch not yet final.
func (r *Repo) CurrentQueueBatch(ctx context.Context, repoID string) (*core.QueueBatch, error) {
	return store.CurrentQueueBatch(ctx, r.ORM(), repoID)
}

// ProjectPipelineEnrolled reports whether a repository of the project is
// enrolled with a mode other than off (G1).
func (r *Repo) ProjectPipelineEnrolled(ctx context.Context, projectID string) (bool, error) {
	return store.ProjectPipelineEnrolled(ctx, r.ORM(), projectID)
}
