package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// What the PR-record projector reads and writes for enrolled repositories
// (pipeline build plan task M3): a record by its pull request, the record a
// base-branch commit merged, and the repository's config as read from that
// branch.

// GetPRRecordByNumber returns the record of a repository's pull request,
// keyed by the provider's stable repository id, or nil when it has none.
func GetPRRecordByNumber(ctx context.Context, db orm.Context, provider, repositoryID string, number int) (*core.PRRecord, error) {
	var rows []prRecordRow
	if err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ? AND repository_id = ? AND number = ?`, provider, repositoryID, number).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("get pr record by number: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core()
}

// GetPRRecordByMergeCommit returns the merged record whose merge commit is
// sha in a repository, or nil when none is recorded (a direct push, or a
// merge whose closed event has not been applied yet).
func GetPRRecordByMergeCommit(ctx context.Context, db orm.Context, provider, repositoryID, sha string) (*core.PRRecord, error) {
	if sha == "" {
		return nil, nil
	}
	var rows []prRecordRow
	if err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ? AND repository_id = ? AND state = ? AND merge_commit_sha = ?
		ORDER BY observed_at DESC LIMIT 1`, provider, repositoryID, core.PRRecordMerged, sha).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("get pr record by merge commit: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core()
}

// RecordPipelineRepoConfig records the validated pipeline.yaml read from the
// base branch at sha, and the gate-stage changes it carries, in one
// transaction, if the repository is still at version: a lost race changes
// nothing and is core.ErrPipelineVersionConflict. Any recorded config error
// is cleared. READ COMMITTED is correct: the compare-and-set on version is
// the only read the write depends on, and it locks the row it updates.
func RecordPipelineRepoConfig(ctx context.Context, db orm.Context, id string, version int64, sha string, config []byte, changes []core.PipelineConfigChange) (*core.PipelineRepo, error) {
	if sha == "" || len(config) == 0 {
		return nil, errors.New("record pipeline repo config: a sha and a config are required")
	}
	for _, c := range changes {
		if !slices.Contains(core.PipelineGateStageSettings, c.Setting) {
			return nil, fmt.Errorf("record pipeline repo config: %q is not a gate stage setting", c.Setting)
		}
		if strings.TrimSpace(c.ChangedByProvider) == "" || strings.TrimSpace(c.ChangedByProviderUserID) == "" || strings.TrimSpace(c.ChangedByLogin) == "" {
			return nil, fmt.Errorf("record pipeline repo config: the change to %s names no actor", c.Setting)
		}
	}
	var out *core.PipelineRepo
	err := db.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		r, err := casPipelineRepo(tx, db, id, version,
			`config_sha = ?, config_json = ?::jsonb, config_error = NULL, config_error_sha = NULL`, sha, string(config))
		if err != nil {
			return err
		}
		for _, c := range changes {
			if _, err := db.Bun().NewRaw(`INSERT INTO pipeline_setting_changes
				(id, org_id, project_id, setting, old_value, new_value, changed_via, repo_id, config_sha,
				 config_record_id, changed_by_provider, changed_by_provider_user_id, changed_by_login)
				VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`,
				uuid.NewString(), r.OrgID, r.ProjectID, c.Setting, c.OldValue, c.NewValue, core.PipelineChangedViaConfig,
				r.ID, sha, c.ConfigRecordID, c.ChangedByProvider, c.ChangedByProviderUserID, c.ChangedByLogin).Exec(tx); err != nil {
				return fmt.Errorf("record %s change: %w", c.Setting, err)
			}
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetPipelineRepoConfigError records why the pipeline.yaml read from the
// base branch at sha was not recorded, keeping the config in force, if the
// repository is still at version.
func SetPipelineRepoConfigError(ctx context.Context, db orm.Context, id string, version int64, sha, reason string) (*core.PipelineRepo, error) {
	if reason == "" {
		return nil, errors.New("set pipeline repo config error: a reason is required")
	}
	return casPipelineRepo(ctx, db, id, version, `config_error = ?, config_error_sha = NULLIF(?, '')`, reason, sha)
}
