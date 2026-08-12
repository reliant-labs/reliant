// Copyright (c) 2025 Reliant Labs

// Package configadapter bridges db.Repository to config.StoredConfigStore.
package configadapter

import (
	"context"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// NewRepoConfigStore creates a StoredConfigStore backed by a db.Repository.
func NewRepoConfigStore(repo db.Repository) config.StoredConfigStore {
	return &repoConfigStore{repo: repo}
}

type repoConfigStore struct {
	repo db.Repository
}

func (s *repoConfigStore) GetProjectConfigRecord(ctx context.Context, projectID string) (*config.StoredProjectConfigRecord, error) {
	record, err := s.repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		return nil, err
	}

	return &config.StoredProjectConfigRecord{
		ProjectID: record.ProjectID,
		// DaemonID distinguishes a real daemon push from CreateProject's seed
		// placeholder — the fact behind Config.SnapshotSynced.
		DaemonID:             record.DaemonID,
		UserConfigYAML:       record.UserConfigYAML,
		ProjectConfigYAML:    record.ProjectConfigYAML,
		LocalConfigYAML:      record.LocalConfigYAML,
		GlobalMemoryMD:       record.GlobalMemoryMD,
		ProjectMemoryMD:      record.ProjectMemoryMD,
		MCPConfigs:           record.MCPConfigs,
		ProjectWorkflowsJSON: record.ProjectWorkflowsJSON,
		ProjectPresetsJSON:   record.ProjectPresetsJSON,
		ProjectScenariosJSON: record.ProjectScenariosJSON,
		ProjectSkillsJSON:    record.ProjectSkillsJSON,
		// RepoMemoriesJSON was absent here while both the db record and the
		// config record carried the field, so per-repo reliant.md content the
		// daemon had already synced was dropped on every read through this
		// store and Config.RepoMemories was always empty.
		RepoMemoriesJSON: record.RepoMemoriesJSON,
		RuntimeType:      record.RuntimeType,
	}, nil
}
