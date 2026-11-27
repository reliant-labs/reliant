package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
// is cleared, and so is a pending read of sha. READ COMMITTED is correct: the compare-and-set on version is
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
		clear, clearArgs := clearPending(sha)
		r, err := casPipelineRepo(tx, db, id, version,
			`config_sha = ?, config_json = ?::jsonb, config_error = NULL, config_error_sha = NULL, `+clear,
			append([]any{sha, string(config)}, clearArgs...)...)
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
// repository is still at version. A pending read of sha is done: the file
// is missing or invalid, and reading it again reads the same file.
func SetPipelineRepoConfigError(ctx context.Context, db orm.Context, id string, version int64, sha, reason string) (*core.PipelineRepo, error) {
	if reason == "" {
		return nil, errors.New("set pipeline repo config error: a reason is required")
	}
	clear, clearArgs := clearPending(sha)
	return casPipelineRepo(ctx, db, id, version, `config_error = ?, config_error_sha = NULLIF(?, ''), `+clear,
		append([]any{reason, sha}, clearArgs...)...)
}

// pendingColumns are the pending config read's columns but its sha.
var pendingColumns = []string{"config_pending_at", "config_pending_sender_id", "config_pending_sender_login",
	"config_pending_attempts", "config_pending_retry_at"}

// clearPending is the SET fragment, and its arguments, that clears the
// pending config read when it is of sha; a read of another commit stays
// pending. Every right-hand side of an UPDATE reads the row as it was, so
// each column compares the old config_pending_sha.
func clearPending(sha string) (string, []any) {
	var b strings.Builder
	var args []any
	for _, c := range pendingColumns {
		fmt.Fprintf(&b, "%s = CASE WHEN config_pending_sha = ? THEN NULL ELSE %s END, ", c, c)
		args = append(args, sha)
	}
	b.WriteString("config_pending_sha = NULLIF(config_pending_sha, ?)")
	return b.String(), append(args, sha)
}

// SetPipelineRepoConfigPending records that the pipeline.yaml at sha, on the
// base branch, is still to be read (a push that may have changed it), due
// now, if the repository is still at version. It replaces a pending read of
// another commit: the newest push is the one whose file counts.
func SetPipelineRepoConfigPending(ctx context.Context, db orm.Context, id string, version int64, p core.PipelineConfigPending) (*core.PipelineRepo, error) {
	if p.SHA == "" {
		return nil, errors.New("set pipeline repo config pending: a sha is required")
	}
	return casPipelineRepo(ctx, db, id, version, `config_pending_sha = ?, config_pending_at = now(),
		config_pending_sender_id = NULLIF(?, ''), config_pending_sender_login = NULLIF(?, ''),
		config_pending_attempts = 0, config_pending_retry_at = now()`, p.SHA, p.SenderID, p.SenderLogin)
}

// ListDuePipelineConfigReads lists the enrolled repositories (mode not off)
// with a pending config read due at now, oldest due first, at most limit
// (0 is 100).
func ListDuePipelineConfigReads(ctx context.Context, db orm.Context, now time.Time, limit int) ([]*core.PipelineRepo, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []*core.PipelineRepo
	if err := db.Bun().NewRaw(`SELECT `+pipelineRepoColumns+` FROM pipeline_repos
		WHERE mode <> ? AND config_pending_sha IS NOT NULL AND coalesce(config_pending_retry_at, created_at) <= ?
		ORDER BY coalesce(config_pending_retry_at, created_at), id LIMIT ?`,
		core.PipelineRepoModeOff, now, limit).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list due pipeline config reads: %w", err)
	}
	return out, nil
}

// ClaimPipelineConfigRead takes the repository's pending read of sha for
// one attempt, until lease: it counts the attempt and holds the read off
// every other worker until then, if the repository is still at version and
// still pending at sha. Another worker's claim, or a newer push, is
// core.ErrPipelineVersionConflict.
func ClaimPipelineConfigRead(ctx context.Context, db orm.Context, id string, version int64, sha string, lease time.Time) (*core.PipelineRepo, error) {
	return casPendingRead(ctx, db, id, version, sha,
		`config_pending_attempts = coalesce(config_pending_attempts, 0) + 1, config_pending_retry_at = ?`, lease)
}

// DeferPipelineConfigRead records why the pending read of sha reached no
// answer (no gate pool daemon read it) as the repository's config error,
// keeping the config in force and the read pending, due again at retry, if
// the repository is still at version and still pending at sha.
func DeferPipelineConfigRead(ctx context.Context, db orm.Context, id string, version int64, sha, reason string, retry time.Time) (*core.PipelineRepo, error) {
	if reason == "" {
		return nil, errors.New("defer pipeline config read: a reason is required")
	}
	return casPendingRead(ctx, db, id, version, sha,
		`config_error = ?, config_error_sha = ?, config_pending_retry_at = ?`, reason, sha, retry)
}

// casPendingRead is casPipelineRepo on a row whose pending read is of sha.
func casPendingRead(ctx context.Context, db orm.Context, id string, version int64, sha, set string, args ...any) (*core.PipelineRepo, error) {
	if sha == "" {
		return nil, errors.New("pipeline config read: a sha is required")
	}
	args = append(args, id, version, sha)
	r, err := scanPipelineRepo(ctx, db.Bun().NewRaw(`UPDATE pipeline_repos SET `+set+`,
		version = version + 1, updated_at = now()
		WHERE id = ? AND version = ? AND config_pending_sha = ? RETURNING `+pipelineRepoColumns, args...))
	if err != nil {
		return nil, fmt.Errorf("update pipeline repo %s: %w", id, err)
	}
	if r == nil {
		return nil, fmt.Errorf("pipeline repo %s at version %d pending %s: %w", id, version, sha, core.ErrPipelineVersionConflict)
	}
	return r, nil
}
