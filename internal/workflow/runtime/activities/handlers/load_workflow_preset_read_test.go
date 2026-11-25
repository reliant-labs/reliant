// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// presetReadFailingStore is the real store, read unpinned (so presets come
// from the machine's synced catalog), whose machine presets read fails with
// err when it is set.
type presetReadFailingStore struct {
	LoadWorkflowStore
	err error
}

func (s presetReadFailingStore) PinProjectCatalog(context.Context, db.CatalogPinScope) (string, error) {
	return "", nil
}

func (s presetReadFailingStore) GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return s.LoadWorkflowStore.GetProjectMachinePresetsJSON(ctx, projectID, daemonID)
}

// selfSchedulingRetry is the runtime's contract for an activity error that
// decides its own retry (runtime.classifyError): what makes it retryable
// whatever its text says.
type selfSchedulingRetry interface {
	error
	RetryAfter() (time.Duration, bool)
	TemporalErrorType() string
}

// A preset the store could not read is not a missing one. main-red-fix run
// b9a6dd4d (2026-11-21) read its presets as the worker shut down; the read
// failed, the loader fell back to the builtins and said "preset not found:
// codex_limit_fallback", and validation made that a terminal failure of the
// run. The read failure now retries the load; a preset truly absent from a
// catalog that was read stays the terminal validation failure.
func TestLoadWorkflowActivity_PresetReadFailureRetriesInsteadOfNotFound(t *testing.T) {
	t.Parallel()

	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID, daemonID := "user-"+uuid.NewString(), "project-"+uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "labs", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	root := func(name, preset string) string {
		return "name: " + name + "\napiVersion: v2\nentry: [review]\nnodes:\n  - id: review\n    type: workflow\n    ref: step\n    presets: {default: " + preset + "}\n"
	}
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{
		{Slug: "release", Name: "release", RelativePath: ".reliant/workflows/release.yaml", YAMLContent: root("release", "impl")},
		{Slug: "orphan", Name: "orphan", RelativePath: ".reliant/workflows/orphan.yaml", YAMLContent: root("orphan", "absent")},
		{Slug: "step", Name: "step", RelativePath: ".reliant/workflows/step.yaml", YAMLContent: "name: step\napiVersion: v2\ninputs:\n  mode:\n    type: string\n    default: build\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"},
	})
	require.NoError(t, err)
	pj, err := config.EncodeCatalogJSON([]config.StoredPreset{
		{Name: "impl", RelativePath: ".reliant/presets/impl.yaml", YAMLContent: "name: impl\nparams:\n  mode: plan\n"},
	})
	require.NoError(t, err)
	db.HoldProjectOnMachineForTest(t, repo, projectID, daemonID)
	_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: daemonID, ProjectWorkflowsJSON: wj, ProjectPresetsJSON: pj, PushedAt: now,
	}, true, nil)
	require.NoError(t, err)
	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, ProjectID: projectID, Title: "run", State: db.ChatStateIdle,
		ActiveDaemonID: &daemonID, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	load := func(readErr error, workflow string) error {
		activity := NewLoadWorkflowActivity(presetReadFailingStore{LoadWorkflowStore: repo, err: readErr}, workspaceRowsOf(repo))
		_, err := activity.Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: workflow})
		return err
	}

	t.Run("a preset that reads loads", func(t *testing.T) {
		require.NoError(t, load(nil, "release"))
	})

	t.Run("a preset read that fails retries the load", func(t *testing.T) {
		for _, readErr := range []error{context.Canceled, errors.New("connection reset by peer")} {
			err := load(readErr, "release")
			require.Error(t, err)
			var retryable selfSchedulingRetry
			require.ErrorAs(t, err, &retryable, "a read failure is a retryable catalog read, not a validation verdict")
			_, retry := retryable.RetryAfter()
			assert.True(t, retry)
			assert.Equal(t, "WorkflowCatalogUnreadable", retryable.TemporalErrorType())
			assert.ErrorIs(t, err, readErr)
			assert.NotContains(t, err.Error(), "not found", "the runtime makes any \"not found\" terminal")
			assert.NotContains(t, err.Error(), "validation failed")
			assert.Contains(t, err.Error(), "read preset impl of project "+projectID)
		}
	})

	t.Run("a preset absent from a catalog that was read is still a terminal validation failure", func(t *testing.T) {
		err := load(nil, "orphan")
		require.Error(t, err)
		var retryable selfSchedulingRetry
		assert.False(t, errors.As(err, &retryable), "an absent preset is not retried")
		assert.Contains(t, err.Error(), "workflow tree validation failed for orphan")
		assert.Contains(t, err.Error(), "preset not found: absent")
	})
}
