package db

import (
	"context"
	"time"

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

// SetPipelineRepoConfigPending records that the base branch's pipeline.yaml
// at a pushed commit is still to be read, if the repository is still at
// version.
func (r *Repo) SetPipelineRepoConfigPending(ctx context.Context, id string, version int64, p core.PipelineConfigPending) (*core.PipelineRepo, error) {
	return store.SetPipelineRepoConfigPending(ctx, r.ORM(), id, version, p)
}

// ListDuePipelineConfigReads lists the enrolled repositories with a pending
// config read due at now.
func (r *Repo) ListDuePipelineConfigReads(ctx context.Context, now time.Time, limit int) ([]*core.PipelineRepo, error) {
	return store.ListDuePipelineConfigReads(ctx, r.ORM(), now, limit)
}

// ClaimPipelineConfigRead takes a pending config read of sha until lease.
func (r *Repo) ClaimPipelineConfigRead(ctx context.Context, id string, version int64, sha string, lease time.Time) (*core.PipelineRepo, error) {
	return store.ClaimPipelineConfigRead(ctx, r.ORM(), id, version, sha, lease)
}

// DeferPipelineConfigRead records why a pending config read of sha reached
// no answer, and when it is due again.
func (r *Repo) DeferPipelineConfigRead(ctx context.Context, id string, version int64, sha, reason string, retry time.Time) (*core.PipelineRepo, error) {
	return store.DeferPipelineConfigRead(ctx, r.ORM(), id, version, sha, reason, retry)
}
