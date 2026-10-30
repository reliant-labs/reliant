// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProjectConfig. The generated project_config_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: projectConfigRepo (a *crud.Repo[ProjectConfig]) and the
//     package-level GetProjectConfigByID / ListProjectConfig / CreateProjectConfig / UpdateProjectConfig / DeleteProjectConfig
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProjectConfig struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProjectConfigBy(ctx context.Context, db orm.Context, /* args */) ([]*ProjectConfig, error) {
//		var rows []*ProjectConfig
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("project_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProjectConfigs(ctx context.Context, db orm.Context) ([]*ProjectConfig, error) {
//		return ListProjectConfig(ctx, db, orm.WhereEq("project_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A project has at most one config record (project_configs.project_id is
// UNIQUE). Every read below returns missing("project config") when the
// project has none: svcerr.NotFound, and errors.Is(err, sql.ErrNoRows) for
// the callers that treat no record as an empty one.
//
// The whole row is the wrong read for anything that needs one part of it. It
// carries every skill body and repo memory the daemon indexed, recursively
// across nested repos: a workspace of ~30 checkouts of the same repos synced
// an 18 MB row, nearly all of it project_skills_json. Workflow resolution and
// preset loading need a column that is empty or a few KB, yet read that row
// three or more times per send — measured in prod as ~95% of SendMessage,
// StartChat and UpdateWorkflowParams wall time (p50 1.7s / 2.3s / 1.0s). So
// each consumer reads only its column.

// UpsertProjectConfigRecord writes the project's record, replacing every
// column but id and created_at of an existing one. A zero PushedAt or
// CreatedAt is the time of the write; updated_at always is.
func UpsertProjectConfigRecord(ctx context.Context, db orm.Context, rec *core.ProjectConfigRecord) error {
	now := time.Now().UTC()
	pushedAt := rec.PushedAt
	if pushedAt.IsZero() {
		pushedAt = now
	}
	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	_, err := db.Bun().NewInsert().Model(&ProjectConfig{
		ID:                   rec.ID,
		ProjectID:            rec.ProjectID,
		DaemonID:             rec.DaemonID,
		UserConfigYaml:       rec.UserConfigYAML,
		ProjectConfigYaml:    rec.ProjectConfigYAML,
		LocalConfigYaml:      rec.LocalConfigYAML,
		GlobalMemoryMd:       rec.GlobalMemoryMD,
		ProjectMemoryMd:      rec.ProjectMemoryMD,
		MCPConfigs:           rec.MCPConfigs,
		ProjectWorkflowsJSON: rec.ProjectWorkflowsJSON,
		ProjectPresetsJSON:   rec.ProjectPresetsJSON,
		ProjectScenariosJSON: rec.ProjectScenariosJSON,
		ProjectSkillsJSON:    rec.ProjectSkillsJSON,
		RepoMemoriesJSON:     rec.RepoMemoriesJSON,
		RuntimeType:          rec.RuntimeType,
		PushedAt:             pushedAt,
		CreatedAt:            createdAt,
		UpdatedAt:            now,
	}).
		On("CONFLICT (project_id) DO UPDATE").
		Set("daemon_id = EXCLUDED.daemon_id").
		Set("user_config_yaml = EXCLUDED.user_config_yaml").
		Set("project_config_yaml = EXCLUDED.project_config_yaml").
		Set("local_config_yaml = EXCLUDED.local_config_yaml").
		Set("global_memory_md = EXCLUDED.global_memory_md").
		Set("project_memory_md = EXCLUDED.project_memory_md").
		Set("mcp_configs = EXCLUDED.mcp_configs").
		Set("project_workflows_json = EXCLUDED.project_workflows_json").
		Set("project_presets_json = EXCLUDED.project_presets_json").
		Set("project_scenarios_json = EXCLUDED.project_scenarios_json").
		Set("project_skills_json = EXCLUDED.project_skills_json").
		Set("repo_memories_json = EXCLUDED.repo_memories_json").
		Set("runtime_type = EXCLUDED.runtime_type").
		Set("pushed_at = EXCLUDED.pushed_at").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to upsert project config record for project %s: %w", rec.ProjectID, err)
	}
	return nil
}

// GetProjectConfigRecord reads the project's whole record. See above for why
// a caller that needs one column should not.
func GetProjectConfigRecord(ctx context.Context, db orm.Context, projectID string) (*core.ProjectConfigRecord, error) {
	rows, err := ListProjectConfig(ctx, db, orm.WhereEq(ProjectConfigFieldProjectID, projectID), orm.WithLimit(1))
	if err != nil {
		return nil, fmt.Errorf("failed to load project config record for project %s: %w", projectID, err)
	}
	if len(rows) == 0 {
		return nil, missing("project config")
	}
	row := rows[0]
	return &core.ProjectConfigRecord{
		ID:                   row.ID,
		ProjectID:            row.ProjectID,
		DaemonID:             row.DaemonID,
		UserConfigYAML:       row.UserConfigYaml,
		ProjectConfigYAML:    row.ProjectConfigYaml,
		LocalConfigYAML:      row.LocalConfigYaml,
		GlobalMemoryMD:       row.GlobalMemoryMd,
		ProjectMemoryMD:      row.ProjectMemoryMd,
		MCPConfigs:           row.MCPConfigs,
		ProjectWorkflowsJSON: row.ProjectWorkflowsJSON,
		ProjectPresetsJSON:   row.ProjectPresetsJSON,
		ProjectScenariosJSON: row.ProjectScenariosJSON,
		ProjectSkillsJSON:    row.ProjectSkillsJSON,
		RepoMemoriesJSON:     row.RepoMemoriesJSON,
		RuntimeType:          row.RuntimeType,
		PushedAt:             row.PushedAt,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}, nil
}

// GetProjectConfigPushedAt returns when the daemon pushed the project's
// current record, reading none of its payload. The daemon config sync
// compares it against every incoming snapshot and delta to drop stale ones;
// it used to read the whole record for that one timestamp — 18 MB per
// comparison in prod, on every push.
func GetProjectConfigPushedAt(ctx context.Context, db orm.Context, projectID string) (time.Time, error) {
	var pushedAt time.Time
	err := db.Bun().NewSelect().Model((*ProjectConfig)(nil)).
		Column(ProjectConfigFieldPushedAt).
		Where("project_id = ?", projectID).
		Limit(1).
		Scan(ctx, &pushedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, missing("project config")
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to load config pushed_at for project %s: %w", projectID, err)
	}
	return pushedAt, nil
}

// GetProjectConfigVersion returns an opaque token that changes whenever the
// project's record is rewritten, reading none of its payload, so a reader can
// cache what it parsed out of the record and re-read it only when it
// changed. The record is 15–20 MB for a workspace with many nested checkouts,
// and the temporal worker re-read and re-parsed it on every LLM call: 26 GB
// of allocation per 22 minutes in prod.
//
// The token pairs updated_at with the row version's xmin. updated_at is
// rewritten by UpsertProjectConfigRecord, the only writer, on every write;
// xmin is the id of the transaction that wrote the current row version, so
// it changes on every UPDATE regardless of which replica's clock stamped
// updated_at. Two writes from different replicas within one microsecond
// would share updated_at; they cannot share xmin. Callers compare tokens for
// equality only — they are not ordered.
func GetProjectConfigVersion(ctx context.Context, db orm.Context, projectID string) (string, error) {
	var (
		updatedAt sql.NullTime
		xmin      string
	)
	err := db.Bun().NewSelect().Model((*ProjectConfig)(nil)).
		Column(ProjectConfigFieldUpdatedAt).
		ColumnExpr("xmin::text").
		Where("project_id = ?", projectID).
		Limit(1).
		Scan(ctx, &updatedAt, &xmin)
	if errors.Is(err, sql.ErrNoRows) {
		return "", missing("project config")
	}
	if err != nil {
		return "", fmt.Errorf("failed to load config version for project %s: %w", projectID, err)
	}
	return fmt.Sprintf("%d/%s", updatedAt.Time.UnixMicro(), xmin), nil
}

// GetProjectWorkflowsJSON reads the synced .reliant/workflows.
func GetProjectWorkflowsJSON(ctx context.Context, db orm.Context, projectID string) (*string, error) {
	return projectConfigColumn(ctx, db, projectID, ProjectConfigFieldProjectWorkflowsJSON)
}

// GetProjectPresetsJSON reads the synced .reliant/presets.
func GetProjectPresetsJSON(ctx context.Context, db orm.Context, projectID string) (*string, error) {
	return projectConfigColumn(ctx, db, projectID, ProjectConfigFieldProjectPresetsJSON)
}

// GetProjectScenariosJSON reads the synced .reliant scenarios.
func GetProjectScenariosJSON(ctx context.Context, db orm.Context, projectID string) (*string, error) {
	return projectConfigColumn(ctx, db, projectID, ProjectConfigFieldProjectScenariosJSON)
}

// GetProjectMCPConfigsJSON reads the stored scoped MCP configs.
func GetProjectMCPConfigsJSON(ctx context.Context, db orm.Context, projectID string) (*string, error) {
	return projectConfigColumn(ctx, db, projectID, ProjectConfigFieldMCPConfigs)
}

// GetProjectCatalogJSON reads the project's synced workflows and presets in
// one statement, so the pair comes from one committed sync: two reads could
// straddle a push and pair one sync's workflows with the next one's presets.
// Both are nil with no error when the project has no record.
func GetProjectCatalogJSON(ctx context.Context, db orm.Context, projectID string) (workflows, presets *string, err error) {
	var row struct {
		Workflows *string `bun:"project_workflows_json"`
		Presets   *string `bun:"project_presets_json"`
	}
	err = db.Bun().NewSelect().Model((*ProjectConfig)(nil)).
		Column(ProjectConfigFieldProjectWorkflowsJSON, ProjectConfigFieldProjectPresetsJSON).
		Where("project_id = ?", projectID).
		Limit(1).
		Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load the workflow catalog of project %s: %w", projectID, err)
	}
	return row.Workflows, row.Presets, nil
}

// projectConfigColumn reads one nullable text column of the project's
// record. column is one of the ProjectConfigField* names, never caller input.
func projectConfigColumn(ctx context.Context, db orm.Context, projectID, column string) (*string, error) {
	var value *string
	err := db.Bun().NewSelect().Model((*ProjectConfig)(nil)).
		Column(column).
		Where("project_id = ?", projectID).
		Limit(1).
		Scan(ctx, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing("project config")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load %s for project %s: %w", column, projectID, err)
	}
	return value, nil
}

// ProjectConfigExtra carries fields on ProjectConfig that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProjectConfig is
// embedded on ProjectConfigExtra (see project_config_orm.go), so a field added here is a field on
// every ProjectConfig value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProjectConfigExtra struct {
}
