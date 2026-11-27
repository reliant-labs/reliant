package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
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
	// RepoStatesJSON is read by GetProjectMachineCatalog only, in the same
	// row read as the catalog (SetProjectMachineRepoStates writes it).
	RepoStatesJSON *string `bun:"repo_states_json"`
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

// GetProjectMachineCatalog reads one machine's catalog of a project, with the
// git state it was synced from, nil with no error when that machine has
// synced none.
func GetProjectMachineCatalog(ctx context.Context, db orm.Context, projectID, daemonID string) (*ProjectMachineCatalog, error) {
	var row ProjectMachineCatalog
	err := db.Bun().NewRaw(`SELECT `+projectMachineCatalogColumns+`, repo_states_json FROM project_machine_catalogs WHERE project_id = ? AND daemon_id = ?`,
		projectID, daemonID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read machine %s's catalog of project %s: %w", daemonID, projectID, err)
	}
	return &row, nil
}

// A row runs to megabytes: workflows_json alone averaged 325 kB compressed in
// prod (2026-11-18, up to 616 kB), and every read of a TOASTed column
// decompresses and ships all of it. The reads below each select only the
// column their caller uses, so reading a machine's presets, or when it last
// pushed, no longer moves its workflows too. Before them, ListInbox read the
// whole catalog row twice per call (once for the workflows, once for the
// presets), 600–720 ms each in trace ed2301a0….

// GetProjectMachineWorkflowsJSON reads one machine's synced workflows of a
// project and nothing else of its row. found is false when that machine has
// synced none.
func GetProjectMachineWorkflowsJSON(ctx context.Context, db orm.Context, projectID, daemonID string) (workflows *string, found bool, err error) {
	return machineCatalogColumn(ctx, db, `SELECT workflows_json FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`,
		"workflows", projectID, daemonID)
}

// GetProjectMachinePresetsJSON is GetProjectMachineWorkflowsJSON for presets.
func GetProjectMachinePresetsJSON(ctx context.Context, db orm.Context, projectID, daemonID string) (presets *string, found bool, err error) {
	return machineCatalogColumn(ctx, db, `SELECT presets_json FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`,
		"presets", projectID, daemonID)
}

func machineCatalogColumn(ctx context.Context, db orm.Context, query, what, projectID, daemonID string) (*string, bool, error) {
	var v *string
	err := db.QueryRow(ctx, query, projectID, daemonID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read machine %s's %s of project %s: %w", daemonID, what, projectID, err)
	}
	return v, true, nil
}

// rowVersionSQL is the version of the row a catalog read reads: its tuple's
// xmin and ctid, which together name one version of a row. Every UPDATE
// writes a new tuple, at a new ctid, so any change to the row changes it
// (and so does a VACUUM FULL, or a freeze, which only costs a cache miss);
// two versions written by one transaction differ by ctid, since the first
// cannot be pruned while that transaction runs. Reading it reads no column
// of the row, so none of its TOASTed JSON is fetched or decompressed.
//
// It lets a reader that kept a column's value (db.Repo's catalog column
// cache) ask "is this still the row I read?" for a few bytes, where reading
// the column again moved hundreds of KB: ListInbox read each machine's
// workflows in 84ms p50 (219ms p95), three times a call, and the project's
// in 51ms (207ms p95), twice (prod, 2026-11-26).
const rowVersionSQL = `xmin::text || '/' || ctid::text`

// ProjectMachineCatalogVersion is the version of one machine's catalog row of
// a project (rowVersionSQL); found is false when that machine has synced none.
func ProjectMachineCatalogVersion(ctx context.Context, db orm.Context, projectID, daemonID string) (version string, found bool, err error) {
	return rowVersion(ctx, db, `SELECT `+rowVersionSQL+` FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`,
		fmt.Sprintf("machine %s's catalog of project %s", daemonID, projectID), projectID, daemonID)
}

// GetProjectMachineWorkflowsJSONVersioned is GetProjectMachineWorkflowsJSON
// with the version of the row it was read from, in the same statement.
func GetProjectMachineWorkflowsJSONVersioned(ctx context.Context, db orm.Context, projectID, daemonID string) (workflows *string, version string, found bool, err error) {
	return versionedColumn(ctx, db, `SELECT `+rowVersionSQL+`, workflows_json FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`,
		fmt.Sprintf("machine %s's workflows of project %s", daemonID, projectID), projectID, daemonID)
}

// GetProjectMachinePresetsJSONVersioned is GetProjectMachinePresetsJSON with
// the version of the row it was read from, in the same statement.
func GetProjectMachinePresetsJSONVersioned(ctx context.Context, db orm.Context, projectID, daemonID string) (presets *string, version string, found bool, err error) {
	return versionedColumn(ctx, db, `SELECT `+rowVersionSQL+`, presets_json FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`,
		fmt.Sprintf("machine %s's presets of project %s", daemonID, projectID), projectID, daemonID)
}

func rowVersion(ctx context.Context, db orm.Context, query, what string, args ...any) (string, bool, error) {
	var version string
	err := db.QueryRow(ctx, query, args...).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to read the version of %s: %w", what, err)
	}
	return version, true, nil
}

func versionedColumn(ctx context.Context, db orm.Context, query, what string, args ...any) (*string, string, bool, error) {
	var version string
	var v *string
	err := db.QueryRow(ctx, query, args...).Scan(&version, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to read %s: %w", what, err)
	}
	return v, version, true, nil
}

// GetProjectMachineCatalogPushedAt is when one machine last pushed its
// catalog of a project; found is false when it never has.
func GetProjectMachineCatalogPushedAt(ctx context.Context, db orm.Context, projectID, daemonID string) (pushedAt time.Time, found bool, err error) {
	err = db.QueryRow(ctx, `SELECT pushed_at FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`,
		projectID, daemonID).Scan(&pushedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to read when machine %s last pushed project %s: %w", daemonID, projectID, err)
	}
	return pushedAt, true, nil
}

// MachineWorkflowFiles is what is known of one machine's workflow files of a
// project: the workflows it synced, those it has removed, and the git state
// it synced them from. ListProjectMachineWorkflowFiles reads them.
type MachineWorkflowFiles struct {
	DaemonID       string
	WorkflowsJSON  *string
	RemovedJSON    *string
	RepoStatesJSON *string
}

// ListProjectMachineWorkflowFiles reads every machine's workflow files of a
// project, ordered by daemon id: in one read what saying where a workflow
// lives needs (db.Repo.WorkflowSourceDiagnostic, ProjectWorkflowProvenance),
// which read every machine's whole catalog twice before.
func ListProjectMachineWorkflowFiles(ctx context.Context, db orm.Context, projectID string) ([]*MachineWorkflowFiles, error) {
	rows, err := db.Query(ctx, `SELECT daemon_id, workflows_json, removed_json, repo_states_json
FROM project_machine_catalogs WHERE project_id = $1 ORDER BY daemon_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list machine workflow files of project %s: %w", projectID, err)
	}
	defer rows.Close()
	var out []*MachineWorkflowFiles
	for rows.Next() {
		var row MachineWorkflowFiles
		if err := rows.Scan(&row.DaemonID, &row.WorkflowsJSON, &row.RemovedJSON, &row.RepoStatesJSON); err != nil {
			return nil, fmt.Errorf("failed to scan machine workflow files of project %s: %w", projectID, err)
		}
		out = append(out, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list machine workflow files of project %s: %w", projectID, err)
	}
	return out, nil
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

// DeleteProjectMachineCatalog deletes one machine's catalog row of a project:
// the project was detached from the machine, so what it synced no longer
// belongs in the project's union. None is not an error. ctx must carry the
// transaction holding LockProjectCatalog, which rewrites the union after.
func DeleteProjectMachineCatalog(ctx context.Context, db orm.Context, projectID, daemonID string) error {
	if _, err := db.Exec(ctx, `DELETE FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`, projectID, daemonID); err != nil {
		return fmt.Errorf("failed to delete machine %s's catalog of project %s: %w", daemonID, projectID, err)
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

// ProjectFilesUnion is the union of every machine's skills, repo memories and
// scenarios of a project (config.UnionSkills).
func ProjectFilesUnion(ctx context.Context, db orm.Context, projectID string) (ProjectConfigUnion, error) {
	rows, err := ListProjectMachineFiles(ctx, db, projectID)
	if err != nil {
		return ProjectConfigUnion{}, err
	}
	machines := make([]config.MachineFiles, 0, len(rows))
	for _, row := range rows {
		m := config.MachineFiles{DaemonID: row.DaemonID, PushedAt: row.PushedAt}
		if m.Skills, err = config.ParseStoredSkills(row.SkillsJSON); err != nil {
			return ProjectConfigUnion{}, err
		}
		if m.RepoMemories, err = config.ParseRepoMemories(row.RepoMemoriesJSON); err != nil {
			return ProjectConfigUnion{}, err
		}
		if m.Scenarios, err = config.ParseStoredScenarios(row.ScenariosJSON); err != nil {
			return ProjectConfigUnion{}, err
		}
		machines = append(machines, m)
	}
	var union ProjectConfigUnion
	if union.SkillsJSON, err = config.EncodeCatalogJSON(config.UnionSkills(machines)); err != nil {
		return union, err
	}
	if union.ScenariosJSON, err = config.EncodeCatalogJSON(config.UnionScenarios(machines)); err != nil {
		return union, err
	}
	if memories := config.UnionRepoMemories(machines); len(memories) > 0 {
		encoded, err := json.Marshal(memories)
		if err != nil {
			return union, err
		}
		s := string(encoded)
		union.RepoMemoriesJSON = &s
	}
	return union, nil
}

// ProjectMachineCatalogs is every machine's catalog of a project.
func ProjectMachineCatalogs(ctx context.Context, db orm.Context, projectID string) ([]config.MachineCatalog, error) {
	rows, err := ListProjectMachineCatalogs(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]config.MachineCatalog, 0, len(rows))
	for _, row := range rows {
		c, _, err := MachineCatalogFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// MachineCatalogFromRow decodes one machine's catalog row and its removal history.
func MachineCatalogFromRow(row *ProjectMachineCatalog) (config.MachineCatalog, []config.RemovedCatalogFile, error) {
	if row == nil {
		return config.MachineCatalog{}, nil, nil
	}
	c := config.MachineCatalog{DaemonID: row.DaemonID, PushedAt: row.PushedAt}
	var err error
	if c.Workflows, err = config.ParseStoredWorkflows(row.WorkflowsJSON); err != nil {
		return c, nil, err
	}
	if c.Presets, err = config.ParseStoredPresets(row.PresetsJSON); err != nil {
		return c, nil, err
	}
	removed, err := config.ParseRemovedCatalogFiles(row.RemovedJSON)
	return c, removed, err
}

// GetProjectMachineConfigRecord reads one machine's own snapshot of a project
// as a config record: what a run on that machine reads its skills, memories,
// MCP servers and config from. synced is false when the machine has never
// synced a whole snapshot of the project (no row, or one written before the
// row carried the config), which is not the same as having synced nothing.
func GetProjectMachineConfigRecord(ctx context.Context, db orm.Context, projectID, daemonID string) (*core.ProjectConfigRecord, bool, error) {
	if projectID == "" || daemonID == "" {
		return nil, false, fmt.Errorf("project ID and daemon ID are required")
	}
	row, err := GetProjectMachineConfig(ctx, db, projectID, daemonID)
	if err != nil || row == nil || !row.Synced {
		return nil, false, err
	}
	return &core.ProjectConfigRecord{
		ProjectID:            row.ProjectID,
		DaemonID:             row.DaemonID,
		UserConfigYAML:       row.UserConfigYAML,
		ProjectConfigYAML:    row.ProjectConfigYAML,
		LocalConfigYAML:      row.LocalConfigYAML,
		GlobalMemoryMD:       row.GlobalMemoryMD,
		ProjectMemoryMD:      row.ProjectMemoryMD,
		MCPConfigs:           row.MCPConfigs,
		ProjectScenariosJSON: row.ScenariosJSON,
		ProjectSkillsJSON:    row.SkillsJSON,
		RepoMemoriesJSON:     row.RepoMemoriesJSON,
		RuntimeType:          row.RuntimeType,
		PushedAt:             row.PushedAt,
	}, true, nil
}
