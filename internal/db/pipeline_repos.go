package db

import (
	"context"
	"encoding/json"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The pipeline's enrolled repositories (pipeline_repos). Each method is the
// internal/store query of the same name; its doc there says what it locks
// and which errors it returns. Every Set* is a compare-and-set on the
// version the caller read: a lost race is core.ErrPipelineVersionConflict.

// GetPipelineRepo returns the enrolled repository with id, or nil.
func (r *Repo) GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error) {
	return store.GetPipelineRepo(ctx, r.ORM(), id)
}

// GetPipelineRepoByRepository returns the provider repository's enrolment, or nil.
func (r *Repo) GetPipelineRepoByRepository(ctx context.Context, provider, repositoryID string) (*core.PipelineRepo, error) {
	return store.GetPipelineRepoByRepository(ctx, r.ORM(), provider, repositoryID)
}

// ListEnrolledPipelineRepos lists every repository whose mode is not off.
func (r *Repo) ListEnrolledPipelineRepos(ctx context.Context) ([]*core.PipelineRepo, error) {
	return store.ListEnrolledPipelineRepos(ctx, r.ORM())
}

// UpsertPipelineRepo enrols a repository, or refreshes its enrolment by the
// same project; another project's is core.ErrPipelineRepoEnrolledElsewhere.
func (r *Repo) UpsertPipelineRepo(ctx context.Context, e core.PipelineRepoEnrolment) (*core.PipelineRepo, error) {
	return store.UpsertPipelineRepo(ctx, r.ORM(), e)
}

// SetPipelineRepoMode sets the mode, if the row is still at version.
func (r *Repo) SetPipelineRepoMode(ctx context.Context, id string, version int64, mode core.PipelineRepoMode) (*core.PipelineRepo, error) {
	return store.SetPipelineRepoMode(ctx, r.ORM(), id, version, mode)
}

// SetPipelineRepoQueueState sets the queue state (and, when paused, the
// decision that paused it), if the row is still at version.
func (r *Repo) SetPipelineRepoQueueState(ctx context.Context, id string, version int64, state core.PipelineQueueState, pausedBy string) (*core.PipelineRepo, error) {
	return store.SetPipelineRepoQueueState(ctx, r.ORM(), id, version, state, pausedBy)
}

// SetPipelineRepoConfig records the validated config read at sha, if the row
// is still at version.
func (r *Repo) SetPipelineRepoConfig(ctx context.Context, id string, version int64, sha string, config json.RawMessage) (*core.PipelineRepo, error) {
	return store.SetPipelineRepoConfig(ctx, r.ORM(), id, version, sha, config)
}
