package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Pipeline enrolment (pipeline_repos; pipeline redesign §4.1). The table is
// forge:no-entity, so its columns are named here once and every query reads
// through them. Every state change is one UPDATE … WHERE version = ?
// (compare-and-set) that bumps the version, so it needs no transaction: a
// writer that read an older version changes nothing and gets
// core.ErrPipelineVersionConflict.

const pipelineRepoColumns = `id, org_id, project_id, provider, account_key, repository_id, repository,
	base_branch, mode, config_sha, config_json, config_error, config_error_sha,
	config_pending_sha, config_pending_at, config_pending_sender_id, config_pending_sender_login,
	config_pending_attempts, config_pending_retry_at, queue_state, queue_paused_by_decision_id,
	created_by_user_id, created_at, updated_at, version`

// GetPipelineRepo returns the enrolled repository with id, or nil when there
// is none.
func GetPipelineRepo(ctx context.Context, db orm.Context, id string) (*core.PipelineRepo, error) {
	return scanPipelineRepo(ctx, db.Bun().NewRaw(`SELECT `+pipelineRepoColumns+` FROM pipeline_repos WHERE id = ?`, id))
}

// GetPipelineRepoByRepository returns the enrolment of the provider's
// repository (its stable id, not owner/name), or nil when it is not enrolled.
func GetPipelineRepoByRepository(ctx context.Context, db orm.Context, provider, repositoryID string) (*core.PipelineRepo, error) {
	return scanPipelineRepo(ctx, db.Bun().NewRaw(`SELECT `+pipelineRepoColumns+` FROM pipeline_repos
		WHERE provider = ? AND repository_id = ?`, provider, repositoryID))
}

// ListEnrolledPipelineRepos lists every repository whose mode is not off
// (shadow or on), by id.
func ListEnrolledPipelineRepos(ctx context.Context, db orm.Context) ([]*core.PipelineRepo, error) {
	var out []*core.PipelineRepo
	if err := db.Bun().NewRaw(`SELECT `+pipelineRepoColumns+` FROM pipeline_repos
		WHERE mode <> ? ORDER BY id`, core.PipelineRepoModeOff).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list enrolled pipeline repos: %w", err)
	}
	return out, nil
}

// UpsertPipelineRepo enrols a repository, or re-enrols it: a re-enrolment
// refreshes the installation, the owner/name and the base branch and bumps
// the version, keeping mode, queue state and config. A repository another
// project enrolled is refused with core.ErrPipelineRepoEnrolledElsewhere and
// left as it is: moving an enrolment is not an upsert (G1). One statement:
// the unique (provider, repository_id) key serializes two enrolments.
func UpsertPipelineRepo(ctx context.Context, db orm.Context, e core.PipelineRepoEnrolment) (*core.PipelineRepo, error) {
	id := e.ID
	if id == "" {
		id = uuid.NewString()
	}
	base := e.BaseBranch
	if base == "" {
		base = "main"
	}
	r, err := scanPipelineRepo(ctx, db.Bun().NewRaw(`INSERT INTO pipeline_repos AS r
		(id, org_id, project_id, provider, account_key, repository_id, repository, base_branch, created_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, repository_id) DO UPDATE SET
			account_key = EXCLUDED.account_key, repository = EXCLUDED.repository,
			base_branch = EXCLUDED.base_branch, updated_at = now(), version = r.version + 1
		WHERE r.project_id = EXCLUDED.project_id AND r.org_id = EXCLUDED.org_id
		RETURNING `+pipelineRepoColumns,
		id, e.OrgID, e.ProjectID, e.Provider, e.AccountKey, e.RepositoryID, e.Repository, base, e.CreatedByUserID))
	if err != nil {
		return nil, fmt.Errorf("enrol pipeline repo: %w", err)
	}
	if r == nil {
		return nil, fmt.Errorf("enrol %s %s: %w", e.Provider, e.RepositoryID, core.ErrPipelineRepoEnrolledElsewhere)
	}
	return r, nil
}

// SetPipelineRepoMode sets the repository's mode, if it is still at
// version.
func SetPipelineRepoMode(ctx context.Context, db orm.Context, id string, version int64, mode core.PipelineRepoMode) (*core.PipelineRepo, error) {
	if !slices.Contains(core.AllPipelineRepoModes, mode) {
		return nil, fmt.Errorf("set pipeline repo mode: unknown mode %q", mode)
	}
	return casPipelineRepo(ctx, db, id, version, `mode = ?`, mode)
}

// SetPipelineRepoQueueState sets the repository's queue state, if it is
// still at version. pausedBy names the decision that paused it; it is kept
// only while the state is paused, and cleared otherwise.
func SetPipelineRepoQueueState(ctx context.Context, db orm.Context, id string, version int64, state core.PipelineQueueState, pausedBy string) (*core.PipelineRepo, error) {
	if !slices.Contains(core.AllPipelineQueueStates, state) {
		return nil, fmt.Errorf("set pipeline queue state: unknown state %q", state)
	}
	if state != core.PipelineQueuePaused {
		pausedBy = ""
	}
	return casPipelineRepo(ctx, db, id, version, `queue_state = ?, queue_paused_by_decision_id = NULLIF(?, '')`, state, pausedBy)
}

// SetPipelineRepoConfig records the validated pipeline.yaml read from the
// base branch at sha, if the repository is still at version.
func SetPipelineRepoConfig(ctx context.Context, db orm.Context, id string, version int64, sha string, config json.RawMessage) (*core.PipelineRepo, error) {
	if sha == "" || len(config) == 0 {
		return nil, errors.New("set pipeline repo config: a sha and a config are required")
	}
	return casPipelineRepo(ctx, db, id, version, `config_sha = ?, config_json = ?::jsonb`, sha, string(config))
}

// casPipelineRepo applies set to the row at version, bumping it.
func casPipelineRepo(ctx context.Context, db orm.Context, id string, version int64, set string, args ...any) (*core.PipelineRepo, error) {
	args = append(args, id, version)
	r, err := scanPipelineRepo(ctx, db.Bun().NewRaw(`UPDATE pipeline_repos SET `+set+`,
		version = version + 1, updated_at = now()
		WHERE id = ? AND version = ? RETURNING `+pipelineRepoColumns, args...))
	if err != nil {
		return nil, fmt.Errorf("update pipeline repo %s: %w", id, err)
	}
	if r == nil {
		return nil, fmt.Errorf("pipeline repo %s at version %d: %w", id, version, core.ErrPipelineVersionConflict)
	}
	return r, nil
}

// rawScanner is a statement that scans one row: bun's raw query.
type rawScanner interface {
	Scan(ctx context.Context, dest ...any) error
}

func scanPipelineRepo(ctx context.Context, q rawScanner) (*core.PipelineRepo, error) {
	var r core.PipelineRepo
	if err := q.Scan(ctx, &r); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}
