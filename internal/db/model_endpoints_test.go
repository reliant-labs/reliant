// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sp(s string) *string { return &s }

func TestModelEndpointCRUDAndUserIsolation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	mine := &ModelEndpoint{ID: "ep-1", UserID: "user-a", Name: "Lab GPU", BaseURL: "https://llm.example.com/v1", Route: ModelEndpointRouteDirect, HeaderNames: []string{"X-Org"}, ModelsJSON: `[{"name":"m"}]`}
	require.NoError(t, repo.CreateModelEndpoint(ctx, mine))
	assert.False(t, mine.CreatedAt.IsZero())

	viaDaemon := &ModelEndpoint{ID: "ep-2", UserID: "user-a", Name: "Home vLLM", BaseURL: "http://10.0.0.5:8000/v1", Route: ModelEndpointRouteViaDaemon, DaemonID: sp("d-1")}
	require.NoError(t, repo.CreateModelEndpoint(ctx, viaDaemon))

	t.Run("get round-trips every column", func(t *testing.T) {
		got, err := repo.GetModelEndpoint(ctx, "user-a", "ep-1")
		require.NoError(t, err)
		assert.Equal(t, "Lab GPU", got.Name)
		assert.Equal(t, []string{"X-Org"}, got.HeaderNames)
		assert.Equal(t, `[{"name":"m"}]`, got.ModelsJSON)
		assert.Nil(t, got.DaemonID)
		assert.Nil(t, got.CredentialConnectionID)

		got2, err := repo.GetModelEndpoint(ctx, "user-a", "ep-2")
		require.NoError(t, err)
		require.NotNil(t, got2.DaemonID)
		assert.Equal(t, "d-1", *got2.DaemonID)
	})

	t.Run("another user sees nothing and changes nothing", func(t *testing.T) {
		_, err := repo.GetModelEndpoint(ctx, "user-b", "ep-1")
		assert.ErrorIs(t, err, ErrModelEndpointNotFound)

		list, err := repo.ListModelEndpoints(ctx, "user-b")
		require.NoError(t, err)
		assert.Empty(t, list)

		hijack := *mine
		hijack.UserID, hijack.Name = "user-b", "pwned"
		assert.ErrorIs(t, repo.UpdateModelEndpoint(ctx, &hijack), ErrModelEndpointNotFound)
		assert.ErrorIs(t, repo.DeleteModelEndpoint(ctx, "user-b", "ep-1"), ErrModelEndpointNotFound)
		require.NoError(t, repo.SetModelEndpointProbe(ctx, "user-b", "ep-1", "tampered"))

		still, err := repo.GetModelEndpoint(ctx, "user-a", "ep-1")
		require.NoError(t, err)
		assert.Equal(t, "Lab GPU", still.Name)
		assert.Empty(t, still.ProbeJSON, "another user's probe write must not land")
	})

	t.Run("list is scoped and ordered by name", func(t *testing.T) {
		list, err := repo.ListModelEndpoints(ctx, "user-a")
		require.NoError(t, err)
		require.Len(t, list, 2)
		assert.Equal(t, "Home vLLM", list[0].Name)
		assert.Equal(t, "Lab GPU", list[1].Name)
	})

	t.Run("names are unique per user, not globally", func(t *testing.T) {
		dup := &ModelEndpoint{ID: "ep-3", UserID: "user-a", Name: "Lab GPU", BaseURL: "https://x.example.com", Route: ModelEndpointRouteDirect}
		assert.ErrorIs(t, repo.CreateModelEndpoint(ctx, dup), ErrModelEndpointNameTaken)

		other := &ModelEndpoint{ID: "ep-4", UserID: "user-b", Name: "Lab GPU", BaseURL: "https://x.example.com", Route: ModelEndpointRouteDirect}
		require.NoError(t, repo.CreateModelEndpoint(ctx, other))

		rename := *mine
		rename.Name = "Home vLLM"
		assert.ErrorIs(t, repo.UpdateModelEndpoint(ctx, &rename), ErrModelEndpointNameTaken)
	})

	t.Run("update and probe", func(t *testing.T) {
		upd := *mine
		upd.Name, upd.CredentialConnectionID, upd.HeaderNames = "Lab GPU 2", sp("conn-9"), []string{"A", "B"}
		require.NoError(t, repo.UpdateModelEndpoint(ctx, &upd))
		require.NoError(t, repo.SetModelEndpointProbe(ctx, "user-a", "ep-1", `{"id":"x"}`))

		got, err := repo.GetModelEndpoint(ctx, "user-a", "ep-1")
		require.NoError(t, err)
		assert.Equal(t, "Lab GPU 2", got.Name)
		require.NotNil(t, got.CredentialConnectionID)
		assert.Equal(t, "conn-9", *got.CredentialConnectionID)
		assert.Equal(t, []string{"A", "B"}, got.HeaderNames)
		assert.Equal(t, `{"id":"x"}`, got.ProbeJSON)
	})

	t.Run("route and daemon_id must agree (schema CHECK)", func(t *testing.T) {
		bad := &ModelEndpoint{ID: "ep-5", UserID: "user-a", Name: "bad", BaseURL: "https://x.example.com", Route: ModelEndpointRouteViaDaemon}
		assert.Error(t, repo.CreateModelEndpoint(ctx, bad))
	})

	t.Run("delete", func(t *testing.T) {
		require.NoError(t, repo.DeleteModelEndpoint(ctx, "user-a", "ep-2"))
		assert.ErrorIs(t, repo.DeleteModelEndpoint(ctx, "user-a", "ep-2"), ErrModelEndpointNotFound)
		_, err := repo.GetModelEndpoint(ctx, "user-a", "ep-2")
		assert.ErrorIs(t, err, ErrModelEndpointNotFound)
	})
}
