package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// project_machine_catalogs holds each machine's own synced workflows and
// presets for a project (forge:no-entity: these queries are its only reads
// and writes). project_configs.project_workflows_json/project_presets_json
// are the union of these rows; db.Repo.SyncProjectConfigSnapshot keeps them
// in step under LockProjectCatalog.

// ProjectMachineCatalog is one machine's row.
type ProjectMachineCatalog struct {
	ProjectID     string    `bun:"project_id"`
	DaemonID      string    `bun:"daemon_id"`
	WorkflowsJSON *string   `bun:"workflows_json"`
	PresetsJSON   *string   `bun:"presets_json"`
	RemovedJSON   *string   `bun:"removed_json"`
	PushedAt      time.Time `bun:"pushed_at"`
	UpdatedAt     time.Time `bun:"updated_at"`
}

// LockProjectCatalog takes the transaction-scoped advisory lock that makes
// one writer of a project's catalogs at a time, so two machines pushing at
// once cannot each write a union that misses the other's files. It covers
// the first write too, when there is no row to lock. ctx must carry a
// transaction.
func LockProjectCatalog(ctx context.Context, db orm.Context, projectID string) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "project-catalog:"+projectID); err != nil {
		return fmt.Errorf("failed to lock project catalog: %w", err)
	}
	return nil
}

const projectMachineCatalogColumns = `project_id, daemon_id, workflows_json, presets_json, removed_json, pushed_at, updated_at`

// GetProjectMachineCatalog reads one machine's catalog of a project, nil with
// no error when that machine has synced none.
func GetProjectMachineCatalog(ctx context.Context, db orm.Context, projectID, daemonID string) (*ProjectMachineCatalog, error) {
	var row ProjectMachineCatalog
	err := db.Bun().NewRaw(`SELECT `+projectMachineCatalogColumns+` FROM project_machine_catalogs WHERE project_id = ? AND daemon_id = ?`,
		projectID, daemonID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read machine %s's catalog of project %s: %w", daemonID, projectID, err)
	}
	return &row, nil
}

// ListProjectMachineCatalogs reads every machine's catalog of a project,
// ordered by daemon id.
func ListProjectMachineCatalogs(ctx context.Context, db orm.Context, projectID string) ([]*ProjectMachineCatalog, error) {
	var rows []*ProjectMachineCatalog
	err := db.Bun().NewRaw(`SELECT `+projectMachineCatalogColumns+` FROM project_machine_catalogs WHERE project_id = ? ORDER BY daemon_id`,
		projectID).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to list machine catalogs of project %s: %w", projectID, err)
	}
	return rows, nil
}

// UpsertProjectMachineCatalog replaces one machine's catalog of a project.
func UpsertProjectMachineCatalog(ctx context.Context, db orm.Context, row *ProjectMachineCatalog) error {
	_, err := db.Exec(ctx, `
INSERT INTO project_machine_catalogs (`+projectMachineCatalogColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (project_id, daemon_id) DO UPDATE SET
    workflows_json = EXCLUDED.workflows_json,
    presets_json = EXCLUDED.presets_json,
    removed_json = EXCLUDED.removed_json,
    pushed_at = EXCLUDED.pushed_at,
    updated_at = EXCLUDED.updated_at`,
		row.ProjectID, row.DaemonID, row.WorkflowsJSON, row.PresetsJSON, row.RemovedJSON, row.PushedAt, row.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to store machine %s's catalog of project %s: %w", row.DaemonID, row.ProjectID, err)
	}
	return nil
}

// SetProjectConfigCatalogUnion rewrites the union columns of a project's
// config record alone, for a snapshot whose other content is older than the
// record's (see db.Repo.SyncProjectConfigSnapshot). updated_at moves, so
// GetProjectConfigVersion's token changes and cached readers re-read.
func SetProjectConfigCatalogUnion(ctx context.Context, db orm.Context, projectID string, workflowsJSON, presetsJSON *string, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE project_configs SET project_workflows_json = $2, project_presets_json = $3, updated_at = $4 WHERE project_id = $1`,
		projectID, workflowsJSON, presetsJSON, now)
	if err != nil {
		return fmt.Errorf("failed to store the workflow catalog of project %s: %w", projectID, err)
	}
	return nil
}
