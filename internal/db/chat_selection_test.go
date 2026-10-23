// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chatselection"
)

func selectionTestChat(t *testing.T, repo *Repo, id string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &Project{
		ID: "p-" + id, UserID: "u1", Name: "P", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	wf := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: id, UserID: "u1", Title: "t", ProjectID: "p-" + id, State: ChatStateIdle,
		WorkflowName: &wf, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
}

func TestChatSelection_RoundTripAndCascade(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	selectionTestChat(t, repo, "c1")
	svc := chatselection.New(repo)

	none, err := svc.Get(ctx, "c1")
	require.NoError(t, err)
	assert.Nil(t, none)

	patch := map[string]any{
		"model": map[string]any{"id": "gpt-5.6-sol@codex", "thinking_level": "high"},
		"mode":  "auto",
		"tools": []any{"read", "write"},
	}
	saved, err := svc.Merge(ctx, "c1", "u1", "builtin://agent", patch, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, saved.Version)

	got, err := svc.Get(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, patch, got.Params, "nested values must survive the jsonb round trip")
	assert.Equal(t, "u1", got.UserID)
	assert.WithinDuration(t, time.Now(), got.UpdatedAt, time.Minute)

	policy := chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitFallback, FallbackModel: "claude-5.5-opus@claude"}
	withPolicy, err := svc.SetPolicy(ctx, "c1", "u1", "builtin://agent", policy, saved.Version)
	require.NoError(t, err)
	assert.EqualValues(t, 2, withPolicy.Version)
	assert.Equal(t, policy, withPolicy.Policy)
	assert.Equal(t, patch, withPolicy.Params)

	require.NoError(t, repo.DeleteChat(ctx, "c1"))
	gone, err := svc.Get(ctx, "c1")
	require.NoError(t, err)
	assert.Nil(t, gone, "the selection goes with its chat")
}

func TestChatSelection_StaleVersionIsRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	selectionTestChat(t, repo, "c1")
	svc := chatselection.New(repo)

	_, err := svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{"mode": "auto"}, 0)
	require.NoError(t, err)
	_, err = svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{"mode": "plan"}, 0)
	require.NoError(t, err)

	_, err = svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{"mode": "manual"}, 1)
	assert.ErrorIs(t, err, chatselection.ErrVersionConflict)

	got, err := svc.Get(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, "plan", got.Params["mode"])
	assert.EqualValues(t, 2, got.Version)
}

func TestChatSelection_ConcurrentEditsOfDifferentKeysBothLand(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	selectionTestChat(t, repo, "c1")
	svc := chatselection.New(repo)

	var wg sync.WaitGroup
	keys := []string{"mode", "temperature", "model", "tools", "skills", "agent"}
	for _, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{key: "v-" + key}, 0)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	got, err := svc.Get(ctx, "c1")
	require.NoError(t, err)
	for _, key := range keys {
		assert.Equal(t, "v-"+key, got.Params[key], "a concurrent edit must not lose %s", key)
	}
	assert.EqualValues(t, len(keys), got.Version)
}

func TestChatSelection_CopyForABranch(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	selectionTestChat(t, repo, "src")
	selectionTestChat(t, repo, "branch")
	svc := chatselection.New(repo)

	policy := chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitWait}
	_, err := svc.Merge(ctx, "src", "u1", "builtin://agent", map[string]any{"model": map[string]any{"id": "x"}}, 0)
	require.NoError(t, err)
	_, err = svc.SetPolicy(ctx, "src", "u1", "builtin://agent", policy, 0)
	require.NoError(t, err)

	require.NoError(t, svc.Copy(ctx, "src", "branch", "u1"))
	got, err := svc.Get(ctx, "branch")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"model": map[string]any{"id": "x"}}, got.Params)
	assert.Equal(t, policy, got.Policy)
	assert.EqualValues(t, 1, got.Version, "a copy starts its own history")

	// A source with nothing copies nothing, and does not fail.
	selectionTestChat(t, repo, "empty")
	selectionTestChat(t, repo, "empty-branch")
	require.NoError(t, svc.Copy(ctx, "empty", "empty-branch", "u1"))
	none, err := svc.Get(ctx, "empty-branch")
	require.NoError(t, err)
	assert.Nil(t, none)
}
