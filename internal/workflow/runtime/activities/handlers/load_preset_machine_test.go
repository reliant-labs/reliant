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
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A child node's preset, applied at run time, is the run's machine's: the
// same catalog its tree was admitted and validated against. The activity was
// given only a project path, so it never read a project preset at all.
func TestLoadPresetParams_ReadsTheRunsMachinesPreset(t *testing.T) {
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
	push := func(daemonID string, at time.Time, mode string) {
		pj, err := config.EncodeCatalogJSON([]config.StoredPreset{
			{Name: "impl", RelativePath: "control-plane/.reliant/presets/impl.yaml", YAMLContent: "name: impl\nparams:\n  mode: " + mode + "\n"},
		})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectPresetsJSON: pj, PushedAt: at,
		}, true)
		require.NoError(t, err)
	}
	push(cicd, now.Add(-time.Hour), "plan")
	push(laptop, now, "build")

	chatOn := func(daemonID string) string {
		id := "chat-" + uuid.NewString()
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, UserID: userID, ProjectID: projectID, Title: "run", State: db.ChatStateIdle,
			ActiveDaemonID: &daemonID, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		return id
	}
	load := NewLoadPresetParamsActivity(repo)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(load.Execute)
	mode := func(chatID string) any {
		val, err := env.ExecuteActivity(load.Execute, LoadPresetParamsInput{ChatID: chatID, PresetName: "impl"})
		require.NoError(t, err)
		var params map[string]interface{}
		require.NoError(t, val.Get(&params))
		return params["mode"]
	}
	assert.Equal(t, "plan", mode(chatOn(cicd)), "cicd's preset, not the union's newest")
	assert.Equal(t, "build", mode(chatOn(laptop)))
}
