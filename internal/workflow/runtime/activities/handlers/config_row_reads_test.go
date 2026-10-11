// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// fullConfigRowReads records every read of the WHOLE project config record,
// with the caller that made it. The record carries every skill body the
// daemon indexed — 18 MB in prod — and these worker paths need only the
// presets column. getSpawnTool alone (every orchestrating turn) was 3.3 GB of
// worker allocation per 22 minutes.
type fullConfigRowReads struct {
	db.Repository

	mu      sync.Mutex
	callers []string
}

func (r *fullConfigRowReads) GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	pc, _, _, _ := runtime.Caller(1)
	r.mu.Lock()
	r.callers = append(r.callers, runtime.FuncForPC(pc).Name())
	r.mu.Unlock()
	return r.Repository.GetProjectConfigRecord(ctx, projectID)
}

// The worker's preset paths read only the presets column — and a project
// preset, which lives in that column, is still found by each of them.
func TestWorkerPresetPathsReadOnlyThePresetsColumn(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()

	presets := `[{"name":"project-reviewer","yaml_content":"name: project-reviewer\ndescription: Reviews with house rules\ntag: agent\nparams:\n  mode: plan\n"}]`
	skills := `[{"name":"big","body":"` + strings.Repeat("x", 64<<10) + `"}]`
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: "test-project", DaemonID: "daemon-1",
		ProjectPresetsJSON: &presets, ProjectSkillsJSON: &skills,
	}))
	spy := &fullConfigRowReads{Repository: repo}

	// CallLLM: the spawn tool's description lists the project preset's own.
	spawn := (&CallLLMActivity{repo: spy}).getSpawnTool(ctx, "test-project", &SpawnConfig{Presets: []string{"project-reviewer"}})
	require.NotNil(t, spawn)
	require.Contains(t, spawn.Description(), "- project-reviewer: Reviews with house rules")

	// LoadPresetParams (spawned workflows).
	loadPreset := NewLoadPresetParamsActivity(spy)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(loadPreset.Execute)
	val, err := env.ExecuteActivity(loadPreset.Execute, LoadPresetParamsInput{ProjectID: "test-project", PresetName: "project-reviewer"})
	require.NoError(t, err)
	var params map[string]interface{}
	require.NoError(t, val.Get(&params))
	require.Equal(t, "plan", params["mode"])

	// LoadWorkflow's validation preset loader.
	params, err = (&LoadWorkflowActivity{repo: spy}).createPresetLoader(ctx, "test-project")("project-reviewer")
	require.NoError(t, err)
	require.Equal(t, "plan", params["mode"])

	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.Empty(t, spy.callers, "worker preset paths read the whole project config row")
}
