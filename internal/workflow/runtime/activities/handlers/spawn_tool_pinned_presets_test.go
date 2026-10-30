// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// The spawn tool describes the presets a run will spawn with: those of the
// catalog the run pinned at its start, as LoadPresetParams loads them, not a
// version a machine pushed since, and its machine's rather than the union's.
func TestSpawnToolDescribesThePresetsOfTheRunsCatalog(t *testing.T) {
	t.Parallel()

	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID := "user-"+uuid.NewString(), "project-"+uuid.NewString()
	cicd, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{cicd, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "labs", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	push := func(daemonID string, at time.Time, description string) {
		t.Helper()
		pj, err := config.EncodeCatalogJSON([]config.StoredPreset{{
			Name: "reviewer", RelativePath: ".reliant/presets/reviewer.yaml",
			YAMLContent: "name: reviewer\ndescription: " + description + "\ntag: agent\nparams:\n  mode: plan\n",
		}})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectPresetsJSON: pj, PushedAt: at,
		}, true)
		require.NoError(t, err)
	}
	push(cicd, now.Add(-time.Hour), "Reviews v1")
	pin, err := repo.PinProjectCatalog(ctx, db.CatalogPinScope{ProjectID: projectID, DaemonID: cicd, UserID: userID})
	require.NoError(t, err)
	push(cicd, now, "Reviews v2")
	push(laptop, now.Add(time.Minute), "Reviews from the laptop")

	activity := &CallLLMActivity{repo: repo}
	describe := func(source spawnPresetSource) string {
		t.Helper()
		spawn := activity.getSpawnTool(ctx, source, &SpawnConfig{Presets: []string{"reviewer"}})
		require.NotNil(t, spawn)
		return spawn.Description()
	}
	assert.Contains(t, describe(spawnPresetSource{projectID: projectID, daemonID: cicd, pin: pin}), "- reviewer: Reviews v1",
		"a pinned run is shown the preset it started with")
	assert.Contains(t, describe(spawnPresetSource{projectID: projectID, daemonID: cicd}), "- reviewer: Reviews v2",
		"an unpinned run is shown its machine's current preset, not the union's newest")
}
