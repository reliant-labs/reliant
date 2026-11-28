package db

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// SyncProjectConfigSnapshot stores a machine's project config snapshot.
//
// The snapshot's workflows and presets replace that MACHINE's catalog
// (project_machine_catalogs) and nothing else; the record's
// project_workflows_json and project_presets_json are then rewritten as the
// union over every machine's catalog (config.UnionWorkflows). So a machine
// without a checkout cannot erase the workflows another machine provides —
// the incident where a new machine's empty snapshot made every trigger's
// workflow vanish.
//
// The rest of the snapshot is stored on the machine's row too
// (store.ProjectMachineConfig), which is what a run on that machine reads
// (GetProjectMachineConfigRecord). On the record, skills, repo memories and
// scenarios are likewise the union over every machine's
// (config.UnionSkills), so a machine syncing without a nested checkout no
// longer erases that checkout's skills; only the single-valued parts (config
// YAML, global and project memory, MCP) are the last pusher's.
//
// Unless force, a snapshot no newer than the one this machine last pushed is
// dropped whole, and one no newer than the record's last push from any
// machine updates only the union. Staleness is judged per machine because
// each machine's watcher sends only when its own content changes: one
// machine's clock or recent push must not drop another's catalog.
//
// The snapshot is the machine's, of a project it holds: under the project's
// catalog lock and its row locked FOR SHARE (the order the machine-org
// cleanup and every observed-checkout write take), it is stored only while
// the project's org, re-read there, is in scope — the projects the machine
// may hold (authz.MachineScopeFor; nil: every org's, a deployment with no
// orgs) — and the machine has a checkout record of it that no user detached.
// So a snapshot resolved before a detach, a machine-org cleanup or an org
// move commits never recreates the machine's catalog after it, nor puts its
// files back in the union. A dropped snapshot is not lost for good: the
// connect reconcile records a checkout the machine has and asks for its
// snapshot again.
//
// It reports whether the machine's catalog was stored.
func (r *Repo) SyncProjectConfigSnapshot(ctx context.Context, record *ProjectConfigRecord, force bool, scope *core.OrgScope) (bool, error) {
	if record == nil {
		return false, fmt.Errorf("project config record cannot be nil")
	}
	if record.ProjectID == "" {
		return false, fmt.Errorf("project ID cannot be empty")
	}
	if record.DaemonID == "" {
		return false, fmt.Errorf("daemon ID cannot be empty")
	}
	next := config.MachineCatalog{DaemonID: record.DaemonID, PushedAt: record.PushedAt}
	var err error
	if next.Workflows, err = config.ParseStoredWorkflows(record.ProjectWorkflowsJSON); err != nil {
		return false, err
	}
	if next.Presets, err = config.ParseStoredPresets(record.ProjectPresetsJSON); err != nil {
		return false, err
	}

	stored := false
	// The advisory lock is the whole guarantee (one writer of a project's
	// catalogs, so two machines pushing at once cannot each write a union
	// missing the other's files), hence READ COMMITTED: a writer queued
	// behind another reads the winner's commit after the lock.
	err = r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		stored = false
		// The catalog lock, then the project row FOR SHARE; the org is
		// re-read under it.
		inScope, err := r.lockProjectInScope(txCtx, record.ProjectID, scope)
		if err != nil || !inScope {
			return err
		}
		// A snapshot of a pair with no checkout record, or a detached one, is
		// dropped here, under the lock every detach (the user's, and the
		// machine-org cleanup) holds: one the caller resolved before a detach
		// committed must not recreate the machine's catalog afterwards.
		if held, err := store.ProjectDaemonExists(txCtx, r.ORM(), record.ProjectID, record.DaemonID); err != nil || !held {
			return err
		}
		if detached, err := store.ProjectDaemonDetached(txCtx, r.ORM(), record.ProjectID, record.DaemonID); err != nil || detached {
			return err
		}
		prevRow, err := store.GetProjectMachineCatalog(txCtx, r.ORM(), record.ProjectID, record.DaemonID)
		if err != nil {
			return err
		}
		if !force && prevRow != nil && !record.PushedAt.After(prevRow.PushedAt) {
			return nil
		}
		prev, history, err := store.MachineCatalogFromRow(prevRow)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		removedJSON, err := config.EncodeCatalogJSON(config.NextRemovedCatalogFiles(prev, next, history, now))
		if err != nil {
			return err
		}
		if err := store.UpsertProjectMachineCatalog(txCtx, r.ORM(), &store.ProjectMachineCatalog{
			ProjectID:     record.ProjectID,
			DaemonID:      record.DaemonID,
			WorkflowsJSON: record.ProjectWorkflowsJSON,
			PresetsJSON:   record.ProjectPresetsJSON,
			RemovedJSON:   removedJSON,
			PushedAt:      record.PushedAt,
			UpdatedAt:     now,
		}); err != nil {
			return err
		}
		if err := store.SetProjectMachineConfig(txCtx, r.ORM(), &store.ProjectMachineConfig{
			ProjectID:         record.ProjectID,
			DaemonID:          record.DaemonID,
			UserConfigYAML:    record.UserConfigYAML,
			ProjectConfigYAML: record.ProjectConfigYAML,
			LocalConfigYAML:   record.LocalConfigYAML,
			GlobalMemoryMD:    record.GlobalMemoryMD,
			ProjectMemoryMD:   record.ProjectMemoryMD,
			MCPConfigs:        record.MCPConfigs,
			ScenariosJSON:     record.ProjectScenariosJSON,
			SkillsJSON:        record.ProjectSkillsJSON,
			RepoMemoriesJSON:  record.RepoMemoriesJSON,
			RuntimeType:       record.RuntimeType,
		}); err != nil {
			return err
		}
		if err := store.SetProjectMachineRepoStates(txCtx, r.ORM(), record.ProjectID, record.DaemonID, record.RepoStatesJSON); err != nil {
			return err
		}
		stored = true

		catalogs, err := store.ProjectMachineCatalogs(txCtx, r.ORM(), record.ProjectID)
		if err != nil {
			return err
		}
		workflowsJSON, err := config.EncodeCatalogJSON(config.UnionWorkflows(catalogs))
		if err != nil {
			return err
		}
		presetsJSON, err := config.EncodeCatalogJSON(config.UnionPresets(catalogs))
		if err != nil {
			return err
		}
		union, err := store.ProjectFilesUnion(txCtx, r.ORM(), record.ProjectID)
		if err != nil {
			return err
		}
		union.WorkflowsJSON, union.PresetsJSON = workflowsJSON, presetsJSON

		recordPushedAt, err := store.GetProjectConfigPushedAt(txCtx, r.ORM(), record.ProjectID)
		hasRecord := err == nil
		if err != nil && !svcerr.IsNotFound(err) {
			return err
		}
		if hasRecord && !force && !record.PushedAt.After(recordPushedAt) {
			return store.SetProjectConfigCatalogUnion(txCtx, r.ORM(), record.ProjectID, union, now)
		}
		whole := *record
		if whole.ID == "" {
			whole.ID = uuid.New().String()
		}
		whole.ProjectWorkflowsJSON = workflowsJSON
		whole.ProjectPresetsJSON = presetsJSON
		whole.ProjectSkillsJSON = union.SkillsJSON
		whole.RepoMemoriesJSON = union.RepoMemoriesJSON
		whole.ProjectScenariosJSON = union.ScenariosJSON
		return store.UpsertProjectConfigRecord(txCtx, r.ORM(), &whole)
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

// GetProjectMachineCatalogPushedAt is when daemonID last pushed its catalog
// of a project, or svcerr.NotFound when it never has.
func (r *Repo) GetProjectMachineCatalogPushedAt(ctx context.Context, projectID, daemonID string) (time.Time, error) {
	if err := requireMachineCatalogKey(projectID, daemonID); err != nil {
		return time.Time{}, err
	}
	pushedAt, found, err := store.GetProjectMachineCatalogPushedAt(ctx, r.ORM(), projectID, daemonID)
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return time.Time{}, svcerr.NotFound("project machine catalog")
	}
	return pushedAt, nil
}

// GetProjectMachineWorkflowsJSON reads one machine's synced workflows of a
// project, and no other column of its catalog row. synced is false when that
// machine has never synced the project, which is not the same as having
// synced none. A value this Repo read before is served again while its row
// is unchanged (catalogColumnCache).
func (r *Repo) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	if err := requireMachineCatalogKey(projectID, daemonID); err != nil {
		return nil, false, err
	}
	return r.catalogColumns().read(ctx, catalogColumnKey{column: "machine.workflows", projectID: projectID, daemonID: daemonID}, catalogColumnReads{
		version: func(ctx context.Context) (string, bool, error) {
			return store.ProjectMachineCatalogVersion(ctx, r.ORM(), projectID, daemonID)
		},
		versioned: func(ctx context.Context) (*string, string, bool, error) {
			return store.GetProjectMachineWorkflowsJSONVersioned(ctx, r.ORM(), projectID, daemonID)
		},
	}, r.fillsCatalogColumns(ctx))
}

// GetProjectMachinePresetsJSON is GetProjectMachineWorkflowsJSON for presets.
func (r *Repo) GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	if err := requireMachineCatalogKey(projectID, daemonID); err != nil {
		return nil, false, err
	}
	return r.catalogColumns().read(ctx, catalogColumnKey{column: "machine.presets", projectID: projectID, daemonID: daemonID}, catalogColumnReads{
		version: func(ctx context.Context) (string, bool, error) {
			return store.ProjectMachineCatalogVersion(ctx, r.ORM(), projectID, daemonID)
		},
		versioned: func(ctx context.Context) (*string, string, bool, error) {
			return store.GetProjectMachinePresetsJSONVersioned(ctx, r.ORM(), projectID, daemonID)
		},
	}, r.fillsCatalogColumns(ctx))
}

func requireMachineCatalogKey(projectID, daemonID string) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID are required")
	}
	return nil
}

// machineWorkflowFiles is every machine's workflow files of a project:
// what says where a workflow lives, read in one row read per machine.
type machineWorkflowFiles struct {
	// catalogs carry each machine's workflows only: no presets.
	catalogs []config.MachineCatalog
	// removals is each machine's removal history, keyed by daemon id.
	removals map[string][]config.RemovedCatalogFile
	// repoStates is the git state each machine synced its files from, keyed
	// by daemon id; a machine that reported none is absent.
	repoStates map[string][]config.SyncedRepoState
}

// projectMachineWorkflowFiles reads every machine's workflow files of a
// project (store.ListProjectMachineWorkflowFiles): its workflows, removal
// history and git state, never its presets or config.
func (r *Repo) projectMachineWorkflowFiles(ctx context.Context, projectID string) (*machineWorkflowFiles, error) {
	rows, err := store.ListProjectMachineWorkflowFiles(ctx, r.ORM(), projectID)
	if err != nil {
		return nil, err
	}
	out := &machineWorkflowFiles{
		catalogs:   make([]config.MachineCatalog, 0, len(rows)),
		removals:   map[string][]config.RemovedCatalogFile{},
		repoStates: map[string][]config.SyncedRepoState{},
	}
	for _, row := range rows {
		workflows, err := config.ParseStoredWorkflows(row.WorkflowsJSON)
		if err != nil {
			return nil, err
		}
		out.catalogs = append(out.catalogs, config.MachineCatalog{DaemonID: row.DaemonID, Workflows: workflows})
		if out.removals[row.DaemonID], err = config.ParseRemovedCatalogFiles(row.RemovedJSON); err != nil {
			return nil, err
		}
		states, err := config.ParseSyncedRepoStates(row.RepoStatesJSON)
		if err != nil {
			return nil, err
		}
		if len(states) > 0 {
			out.repoStates[row.DaemonID] = states
		}
	}
	return out, nil
}

// ProjectWorkflowProvenance is, for every machine's copy of every synced
// workflow of a project, which machine and file it is and the git state it
// was synced at, ordered by workflow path and then machine.
func (r *Repo) ProjectWorkflowProvenance(ctx context.Context, projectID string) ([]config.WorkflowProvenance, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	files, err := r.projectMachineWorkflowFiles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	catalogs, states := files.catalogs, files.repoStates
	names := r.machineNames(ctx, catalogs)
	var out []config.WorkflowProvenance
	for _, c := range catalogs {
		for _, w := range c.Workflows {
			p := config.WorkflowProvenance{
				Name: w.Name, RelativePath: config.WorkflowFileKey(w), DaemonID: c.DaemonID, Machine: names[c.DaemonID],
				ContentHash: config.DefinitionHash([]byte(w.YAMLContent)),
			}
			if s := config.RepoStateFor(states[c.DaemonID], p.RelativePath); s != nil {
				state := *s
				p.Repo = &state
			}
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RelativePath != out[j].RelativePath {
			return out[i].RelativePath < out[j].RelativePath
		}
		return out[i].Machine < out[j].Machine
	})
	return out, nil
}
