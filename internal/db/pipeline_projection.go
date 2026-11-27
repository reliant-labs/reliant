package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// What the PR-record projector reads and writes for enrolled repositories.
// Each method is the internal/store query of the same name; its doc there
// says what it locks and which errors it returns.

// GetPRRecordByNumber returns a repository's pull request record, or nil.
func (r *Repo) GetPRRecordByNumber(ctx context.Context, provider, repositoryID string, number int) (*core.PRRecord, error) {
	return store.GetPRRecordByNumber(ctx, r.ORM(), provider, repositoryID, number)
}

// GetPRRecordByMergeCommit returns the merged record whose merge commit is
// sha, or nil.
func (r *Repo) GetPRRecordByMergeCommit(ctx context.Context, provider, repositoryID, sha string) (*core.PRRecord, error) {
	return store.GetPRRecordByMergeCommit(ctx, r.ORM(), provider, repositoryID, sha)
}

// RecordPipelineRepoConfig records the validated config read at sha and the
// gate-stage changes it carries, if the repository is still at version.
func (r *Repo) RecordPipelineRepoConfig(ctx context.Context, id string, version int64, sha string, config []byte, changes []core.PipelineConfigChange) (*core.PipelineRepo, error) {
	return store.RecordPipelineRepoConfig(ctx, r.ORM(), id, version, sha, config, changes)
}

// SetPipelineRepoConfigError records why the config read at sha was not
// recorded, if the repository is still at version.
func (r *Repo) SetPipelineRepoConfigError(ctx context.Context, id string, version int64, sha, reason string) (*core.PipelineRepo, error) {
	return store.SetPipelineRepoConfigError(ctx, r.ORM(), id, version, sha, reason)
}
