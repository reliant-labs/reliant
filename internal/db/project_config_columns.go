package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetProjectConfigPushedAt returns when the daemon pushed the project's
// current config record (pushed_at), without reading any of its payload. It
// returns sql.ErrNoRows, unwrapped, when the project has no record, like
// GetProjectConfigRecord.
//
// The daemon config sync compares this against every incoming snapshot and
// delta to drop stale ones. It used to read the whole record for that one
// timestamp — 18 MB per comparison in prod, on every push.
func (r *Repo) GetProjectConfigPushedAt(ctx context.Context, projectID string) (time.Time, error) {
	if projectID == "" {
		return time.Time{}, fmt.Errorf("project ID cannot be empty")
	}
	query := r.bindQuery(`SELECT pushed_at FROM project_configs WHERE project_id = ? LIMIT 1`)

	var pushedAt time.Time
	if err := r.DB.QueryRowContext(ctx, query, projectID).Scan(&pushedAt); err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, err
		}
		return time.Time{}, fmt.Errorf("failed to load config pushed_at for project %s: %w", projectID, err)
	}
	return pushedAt, nil
}

// GetProjectScenariosJSON reads one column of the project config record: the
// synced .reliant scenarios. It returns sql.ErrNoRows, unwrapped, when the
// project has no record. See getProjectConfigColumn for why the whole record
// is the wrong read.
func (r *Repo) GetProjectScenariosJSON(ctx context.Context, projectID string) (*string, error) {
	return r.getProjectConfigColumn(ctx, projectID, "project_scenarios_json")
}

// GetProjectConfigVersion returns an opaque token that changes whenever the
// project's config record is rewritten, without reading any of its payload.
// It returns sql.ErrNoRows, unwrapped, when the project has no record, like
// GetProjectConfigRecord.
//
// It exists so a reader can cache what it parsed out of the record and
// re-read the record only when it changed. The record is 15–20 MB for a
// workspace with many nested checkouts (nearly all of it
// project_skills_json), and the temporal worker re-read and re-parsed it on
// every LLM call: 26 GB of allocation per 22 minutes in prod.
//
// The token pairs updated_at with the row version's xmin. updated_at is
// rewritten by UpsertProjectConfigRecord, the only writer, on every write;
// xmin is the id of the transaction that wrote the current row version, so it
// changes on every UPDATE regardless of which replica's clock stamped
// updated_at. Two writes from different replicas within one microsecond would
// share updated_at; they cannot share xmin. Callers compare tokens for
// equality only — they are not ordered.
func (r *Repo) GetProjectConfigVersion(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project ID cannot be empty")
	}
	query := r.bindQuery(`SELECT updated_at, xmin::text FROM project_configs WHERE project_id = ? LIMIT 1`)

	var (
		updatedAt sql.NullTime
		xmin      string
	)
	if err := r.DB.QueryRowContext(ctx, query, projectID).Scan(&updatedAt, &xmin); err != nil {
		if err == sql.ErrNoRows {
			return "", err
		}
		return "", fmt.Errorf("failed to load config version for project %s: %w", projectID, err)
	}
	return fmt.Sprintf("%d/%s", updatedAt.Time.UnixMicro(), xmin), nil
}
