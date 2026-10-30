package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// project_machine_catalogs holds each machine's own synced project config
// (forge:no-entity: these queries are its only reads and writes): its
// workflows and presets (the catalog, read on every run start, so read
// alone), and the rest of its snapshot (ProjectMachineConfig: skills,
// memories, MCP, config YAML, scenarios, which a run on that machine reads).
// project_configs carries their union; db.Repo.SyncProjectConfigSnapshot
// keeps it in step under LockProjectCatalog.

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

// SetProjectMachineRepoStates stores the git state one machine's snapshot
// was read from on its row, which UpsertProjectMachineCatalog wrote in the
// same transaction.
func SetProjectMachineRepoStates(ctx context.Context, db orm.Context, projectID, daemonID string, repoStatesJSON *string) error {
	_, err := db.Exec(ctx, `UPDATE project_machine_catalogs SET repo_states_json = $3 WHERE project_id = $1 AND daemon_id = $2`,
		projectID, daemonID, repoStatesJSON)
	if err != nil {
		return fmt.Errorf("failed to store machine %s's repo states of project %s: %w", daemonID, projectID, err)
	}
	return nil
}

// MachineRepoStates is one machine's reported git state of a project.
type MachineRepoStates struct {
	DaemonID       string    `bun:"daemon_id"`
	RepoStatesJSON *string   `bun:"repo_states_json"`
	PushedAt       time.Time `bun:"pushed_at"`
}

// ListProjectMachineRepoStates reads every machine's reported git state of a
// project, ordered by daemon id.
func ListProjectMachineRepoStates(ctx context.Context, db orm.Context, projectID string) ([]*MachineRepoStates, error) {
	var rows []*MachineRepoStates
	err := db.Bun().NewRaw(`SELECT daemon_id, repo_states_json, pushed_at FROM project_machine_catalogs WHERE project_id = ? ORDER BY daemon_id`,
		projectID).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to list machine repo states of project %s: %w", projectID, err)
	}
	return rows, nil
}

// ProjectMachineConfig is the rest of one machine's snapshot of a project.
// Synced is false for a row written before these columns existed: it holds
// a catalog but no config, so a read for that machine falls back to the
// project's record.
type ProjectMachineConfig struct {
	ProjectID         string    `bun:"project_id"`
	DaemonID          string    `bun:"daemon_id"`
	UserConfigYAML    *string   `bun:"user_config_yaml"`
	ProjectConfigYAML *string   `bun:"project_config_yaml"`
	LocalConfigYAML   *string   `bun:"local_config_yaml"`
	GlobalMemoryMD    *string   `bun:"global_memory_md"`
	ProjectMemoryMD   *string   `bun:"project_memory_md"`
	MCPConfigs        *string   `bun:"mcp_configs"`
	ScenariosJSON     *string   `bun:"scenarios_json"`
	SkillsJSON        *string   `bun:"skills_json"`
	RepoMemoriesJSON  *string   `bun:"repo_memories_json"`
	RuntimeType       *string   `bun:"runtime_type"`
	Synced            bool      `bun:"config_synced"`
	PushedAt          time.Time `bun:"pushed_at"`
}

const projectMachineConfigColumns = `project_id, daemon_id, user_config_yaml, project_config_yaml, local_config_yaml, global_memory_md, project_memory_md, mcp_configs, scenarios_json, skills_json, repo_memories_json, runtime_type, config_synced, pushed_at`

// GetProjectMachineConfig reads the rest of one machine's snapshot of a
// project, nil with no error when that machine has synced none.
func GetProjectMachineConfig(ctx context.Context, db orm.Context, projectID, daemonID string) (*ProjectMachineConfig, error) {
	var row ProjectMachineConfig
	err := db.Bun().NewRaw(`SELECT `+projectMachineConfigColumns+` FROM project_machine_catalogs WHERE project_id = ? AND daemon_id = ?`,
		projectID, daemonID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read machine %s's config of project %s: %w", daemonID, projectID, err)
	}
	return &row, nil
}

// ProjectMachineFiles is the per-file part of one machine's config: what the
// project's union is computed from.
type ProjectMachineFiles struct {
	DaemonID         string    `bun:"daemon_id"`
	ScenariosJSON    *string   `bun:"scenarios_json"`
	SkillsJSON       *string   `bun:"skills_json"`
	RepoMemoriesJSON *string   `bun:"repo_memories_json"`
	PushedAt         time.Time `bun:"pushed_at"`
}

// ListProjectMachineFiles reads every machine's per-file config of a project
// that carries one (config_synced), ordered by daemon id.
func ListProjectMachineFiles(ctx context.Context, db orm.Context, projectID string) ([]*ProjectMachineFiles, error) {
	var rows []*ProjectMachineFiles
	err := db.Bun().NewRaw(`SELECT daemon_id, scenarios_json, skills_json, repo_memories_json, pushed_at FROM project_machine_catalogs WHERE project_id = ? AND config_synced ORDER BY daemon_id`,
		projectID).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to list machine configs of project %s: %w", projectID, err)
	}
	return rows, nil
}

// SetProjectMachineConfig stores the rest of one machine's snapshot on its
// row, which UpsertProjectMachineCatalog wrote in the same transaction.
func SetProjectMachineConfig(ctx context.Context, db orm.Context, row *ProjectMachineConfig) error {
	_, err := db.Exec(ctx, `
UPDATE project_machine_catalogs SET
    user_config_yaml = $3, project_config_yaml = $4, local_config_yaml = $5,
    global_memory_md = $6, project_memory_md = $7, mcp_configs = $8,
    scenarios_json = $9, skills_json = $10, repo_memories_json = $11,
    runtime_type = $12, config_synced = true
WHERE project_id = $1 AND daemon_id = $2`,
		row.ProjectID, row.DaemonID, row.UserConfigYAML, row.ProjectConfigYAML, row.LocalConfigYAML,
		row.GlobalMemoryMD, row.ProjectMemoryMD, row.MCPConfigs,
		row.ScenariosJSON, row.SkillsJSON, row.RepoMemoriesJSON, row.RuntimeType)
	if err != nil {
		return fmt.Errorf("failed to store machine %s's config of project %s: %w", row.DaemonID, row.ProjectID, err)
	}
	return nil
}

// ProjectConfigUnion is the per-file content of a project's record, unioned
// over every machine's (see db.Repo.SyncProjectConfigSnapshot).
type ProjectConfigUnion struct {
	WorkflowsJSON, PresetsJSON, SkillsJSON, RepoMemoriesJSON, ScenariosJSON *string
}

// SetProjectConfigCatalogUnion rewrites the union columns of a project's
// config record alone, for a snapshot whose other content is older than the
// record's (see db.Repo.SyncProjectConfigSnapshot). updated_at moves, so
// GetProjectConfigVersion's token changes and cached readers re-read.
func SetProjectConfigCatalogUnion(ctx context.Context, db orm.Context, projectID string, union ProjectConfigUnion, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE project_configs SET project_workflows_json = $2, project_presets_json = $3,
    project_skills_json = $4, repo_memories_json = $5, project_scenarios_json = $6, updated_at = $7 WHERE project_id = $1`,
		projectID, union.WorkflowsJSON, union.PresetsJSON, union.SkillsJSON, union.RepoMemoriesJSON, union.ScenariosJSON, now)
	if err != nil {
		return fmt.Errorf("failed to store the workflow catalog of project %s: %w", projectID, err)
	}
	return nil
}
