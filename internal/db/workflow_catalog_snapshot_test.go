// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

// A union pin reads the project's workflows and presets from one committed
// sync. Two reads could straddle a push and pin one sync's workflows beside
// the next one's presets: a catalog that never existed, kept for the run.
// Syncs and pins race here; every stored pair must be one version's.
func TestPinProjectCatalogPairsWorkflowsAndPresetsFromOneSync(t *testing.T) {
	repo := NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID := "user-"+uuid.NewString(), "project-"+uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &Project{
		ID: projectID, UserID: userID, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: daemonID, UserID: userID}))
	push := func(version int) {
		wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "w", Name: "w", RelativePath: ".reliant/workflows/w.yaml",
			YAMLContent: fmt.Sprintf("name: w\ndescription: v%d\n", version)}})
		require.NoError(t, err)
		pj, err := config.EncodeCatalogJSON([]config.StoredPreset{{Name: "p", RelativePath: ".reliant/presets/p.yaml",
			YAMLContent: fmt.Sprintf("name: p\ndescription: v%d\n", version)}})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectWorkflowsJSON: wj, ProjectPresetsJSON: pj,
			PushedAt: now.Add(time.Duration(version) * time.Second),
		}, true)
		require.NoError(t, err)
	}
	push(0)

	var wg sync.WaitGroup
	pins := make(chan string, 400)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for v := 1; v <= 40; v++ {
			push(v)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			// No machine: the union, the read under test.
			pin, err := repo.PinProjectCatalog(ctx, CatalogPinScope{ProjectID: projectID})
			if err != nil {
				t.Error(err)
				return
			}
			pins <- pin
		}
	}()
	wg.Wait()
	close(pins)

	version := func(s *string) string {
		require.NotNil(t, s)
		i := strings.Index(*s, "description: v")
		require.GreaterOrEqual(t, i, 0, *s)
		rest := (*s)[i+len("description: v"):]
		return rest[:strings.IndexAny(rest, `\"`)]
	}
	seen := map[string]bool{}
	for pin := range pins {
		if seen[pin] {
			continue
		}
		seen[pin] = true
		catalog, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, version(catalog.Workflows), version(catalog.Presets), "pin %s pairs two syncs", pin)
	}
}

// The position checkpoint carries the run's catalog pin, so a restart from it
// can load the definitions the run started with.
func TestWorkflowCheckpointRecordsTheCatalogPin(t *testing.T) {
	repo := NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	workflowID := "wf-" + uuid.NewString()
	require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &WorkflowCheckpoint{
		WorkflowID: workflowID, ChatID: "chat-" + uuid.NewString(), NodeID: "loop", LoopIteration: 2, CatalogPin: "pin-1",
	}))
	cp, err := repo.GetWorkflowCheckpoint(ctx, workflowID)
	require.NoError(t, err)
	assert.Equal(t, "pin-1", cp.CatalogPin)

	require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &WorkflowCheckpoint{WorkflowID: workflowID, ChatID: cp.ChatID, NodeID: "loop"}))
	cp, err = repo.GetWorkflowCheckpoint(ctx, workflowID)
	require.NoError(t, err)
	assert.Empty(t, cp.CatalogPin, "a run with no pin records none")
}
