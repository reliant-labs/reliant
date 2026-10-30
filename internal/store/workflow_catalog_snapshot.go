package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// workflow_catalog_snapshots holds the project workflows and presets a run
// started with (forge:no-entity: these queries are its only reads and
// writes). See db.Repo.PinProjectCatalog.

// WorkflowCatalogSnapshot is one pinned catalog.
type WorkflowCatalogSnapshot struct {
	ID            string  `bun:"id"`
	ProjectID     string  `bun:"project_id"`
	DaemonID      *string `bun:"daemon_id"`
	WorkflowsJSON *string `bun:"workflows_json"`
	PresetsJSON   *string `bun:"presets_json"`
	// UserWorkflowsJSON is the owner's account workflows as a ref resolved
	// them at the pin (db.PinnedUserWorkflow by slug); NULL for none.
	UserWorkflowsJSON *string `bun:"user_workflows_json"`
	// RepoStatesJSON is the git state DaemonID's files were synced from
	// (config.SyncedRepoState array); NULL for the union or none reported.
	RepoStatesJSON *string `bun:"repo_states_json"`
}

// CatalogSnapshotRetention is how long a pinned catalog no run has used is
// kept.
const CatalogSnapshotRetention = 90 * 24 * time.Hour

// PutWorkflowCatalogSnapshot stores a pinned catalog, or marks the stored one
// of that id used now, and prunes those unused for CatalogSnapshotRetention.
func PutWorkflowCatalogSnapshot(ctx context.Context, db orm.Context, row *WorkflowCatalogSnapshot, now time.Time) error {
	if _, err := db.Exec(ctx, `
INSERT INTO workflow_catalog_snapshots (id, project_id, daemon_id, workflows_json, presets_json, user_workflows_json, repo_states_json, created_at, last_used_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (id) DO UPDATE SET last_used_at = EXCLUDED.last_used_at`,
		row.ID, row.ProjectID, row.DaemonID, row.WorkflowsJSON, row.PresetsJSON, row.UserWorkflowsJSON, row.RepoStatesJSON, now); err != nil {
		return fmt.Errorf("failed to store the workflow catalog snapshot of project %s: %w", row.ProjectID, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM workflow_catalog_snapshots WHERE last_used_at < $1`, now.Add(-CatalogSnapshotRetention)); err != nil {
		return fmt.Errorf("failed to prune workflow catalog snapshots: %w", err)
	}
	return nil
}

// UseWorkflowCatalogSnapshot reads a pinned catalog and marks it used now;
// nil with no error when there is none of that id.
func UseWorkflowCatalogSnapshot(ctx context.Context, db orm.Context, id string, now time.Time) (*WorkflowCatalogSnapshot, error) {
	var row WorkflowCatalogSnapshot
	err := db.Bun().NewRaw(`
UPDATE workflow_catalog_snapshots SET last_used_at = ? WHERE id = ?
RETURNING id, project_id, daemon_id, workflows_json, presets_json, user_workflows_json, repo_states_json`, now, id).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow catalog snapshot %s: %w", id, err)
	}
	return &row, nil
}
