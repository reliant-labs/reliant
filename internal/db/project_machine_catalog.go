package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/config"
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
// workflow vanish. The rest of the record (config, MCP, memories, skills,
// scenarios) is still the last pusher's.
//
// Unless force, a snapshot no newer than the one this machine last pushed is
// dropped whole, and one no newer than the record's last push from any
// machine updates only the union. Staleness is judged per machine because
// each machine's watcher sends only when its own content changes: one
// machine's clock or recent push must not drop another's catalog.
//
// It reports whether the machine's catalog was stored.
func (r *Repo) SyncProjectConfigSnapshot(ctx context.Context, record *ProjectConfigRecord, force bool) (bool, error) {
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
		if err := store.LockProjectCatalog(txCtx, r.ORM(), record.ProjectID); err != nil {
			return err
		}
		prevRow, err := store.GetProjectMachineCatalog(txCtx, r.ORM(), record.ProjectID, record.DaemonID)
		if err != nil {
			return err
		}
		if !force && prevRow != nil && !record.PushedAt.After(prevRow.PushedAt) {
			return nil
		}
		prev, history, err := machineCatalogFromRow(prevRow)
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
		if err := store.SetProjectMachineRepoStates(txCtx, r.ORM(), record.ProjectID, record.DaemonID, record.RepoStatesJSON); err != nil {
			return err
		}
		stored = true

		catalogs, err := r.projectMachineCatalogs(txCtx, record.ProjectID)
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

		recordPushedAt, err := store.GetProjectConfigPushedAt(txCtx, r.ORM(), record.ProjectID)
		hasRecord := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if hasRecord && !force && !record.PushedAt.After(recordPushedAt) {
			return store.SetProjectConfigCatalogUnion(txCtx, r.ORM(), record.ProjectID, workflowsJSON, presetsJSON, now)
		}
		whole := *record
		if whole.ID == "" {
			whole.ID = uuid.New().String()
		}
		whole.ProjectWorkflowsJSON = workflowsJSON
		whole.ProjectPresetsJSON = presetsJSON
		return store.UpsertProjectConfigRecord(txCtx, r.ORM(), &whole)
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

// ProjectMachineRepoStates reads the git state each machine's synced files of
// a project were read from, keyed by daemon id; a machine whose daemon has not
// reported one is absent.
func (r *Repo) ProjectMachineRepoStates(ctx context.Context, projectID string) (map[string][]config.SyncedRepoState, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	rows, err := store.ListProjectMachineRepoStates(ctx, r.ORM(), projectID)
	if err != nil {
		return nil, err
	}
	out := map[string][]config.SyncedRepoState{}
	for _, row := range rows {
		states, err := config.ParseSyncedRepoStates(row.RepoStatesJSON)
		if err != nil {
			return nil, err
		}
		if len(states) > 0 {
			out[row.DaemonID] = states
		}
	}
	return out, nil
}

// ProjectMachineCatalogs reads every machine's catalog of a project.
func (r *Repo) ProjectMachineCatalogs(ctx context.Context, projectID string) ([]config.MachineCatalog, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.projectMachineCatalogs(ctx, projectID)
}

func (r *Repo) projectMachineCatalogs(ctx context.Context, projectID string) ([]config.MachineCatalog, error) {
	rows, err := store.ListProjectMachineCatalogs(ctx, r.ORM(), projectID)
	if err != nil {
		return nil, err
	}
	out := make([]config.MachineCatalog, 0, len(rows))
	for _, row := range rows {
		c, _, err := machineCatalogFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// GetProjectMachineCatalogPushedAt is when daemonID last pushed its catalog
// of a project, or sql.ErrNoRows, unwrapped, when it never has.
func (r *Repo) GetProjectMachineCatalogPushedAt(ctx context.Context, projectID, daemonID string) (time.Time, error) {
	row, err := r.machineCatalogRow(ctx, projectID, daemonID)
	if err != nil {
		return time.Time{}, err
	}
	if row == nil {
		return time.Time{}, sql.ErrNoRows
	}
	return row.PushedAt, nil
}

// GetProjectMachineWorkflowsJSON reads one machine's synced workflows of a
// project. synced is false when that machine has never synced the project,
// which is not the same as having synced none.
func (r *Repo) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	row, err := r.machineCatalogRow(ctx, projectID, daemonID)
	if err != nil || row == nil {
		return nil, false, err
	}
	return row.WorkflowsJSON, true, nil
}

// GetProjectMachinePresetsJSON is GetProjectMachineWorkflowsJSON for presets.
func (r *Repo) GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	row, err := r.machineCatalogRow(ctx, projectID, daemonID)
	if err != nil || row == nil {
		return nil, false, err
	}
	return row.PresetsJSON, true, nil
}

func (r *Repo) machineCatalogRow(ctx context.Context, projectID, daemonID string) (*store.ProjectMachineCatalog, error) {
	if projectID == "" || daemonID == "" {
		return nil, fmt.Errorf("project ID and daemon ID are required")
	}
	return store.GetProjectMachineCatalog(ctx, r.ORM(), projectID, daemonID)
}

func machineCatalogFromRow(row *store.ProjectMachineCatalog) (config.MachineCatalog, []config.RemovedCatalogFile, error) {
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

// projectMachineRemovals reads every machine's removal history of a project,
// keyed by daemon id.
func (r *Repo) projectMachineRemovals(ctx context.Context, projectID string) (map[string][]config.RemovedCatalogFile, error) {
	rows, err := store.ListProjectMachineCatalogs(ctx, r.ORM(), projectID)
	if err != nil {
		return nil, err
	}
	out := map[string][]config.RemovedCatalogFile{}
	for _, row := range rows {
		removed, err := config.ParseRemovedCatalogFiles(row.RemovedJSON)
		if err != nil {
			return nil, err
		}
		out[row.DaemonID] = removed
	}
	return out, nil
}

// ProjectWorkflowProvenance is, for every machine's copy of every synced
// workflow of a project, which machine and file it is and the git state it
// was synced at, ordered by workflow path and then machine.
func (r *Repo) ProjectWorkflowProvenance(ctx context.Context, projectID string) ([]config.WorkflowProvenance, error) {
	catalogs, err := r.ProjectMachineCatalogs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	states, err := r.ProjectMachineRepoStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	names := r.machineNames(ctx, catalogs)
	var out []config.WorkflowProvenance
	for _, c := range catalogs {
		for _, w := range c.Workflows {
			p := config.WorkflowProvenance{
				Name: w.Name, RelativePath: config.WorkflowFileKey(w), DaemonID: c.DaemonID, Machine: names[c.DaemonID],
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
