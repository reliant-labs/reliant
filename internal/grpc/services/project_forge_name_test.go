// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// forgeDiscoverRouter is a live daemon whose repo.discover reports a forge.yaml
// with the given name.
type forgeDiscoverRouter struct {
	stalePathRouter
	forgeProjectName string
}

func (r *forgeDiscoverRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *forgeDiscoverRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "repo.discover" {
		return json.Marshal(map[string]any{
			"discovered":         []any{},
			"has_forge":          true,
			"forge_project_name": r.forgeProjectName,
		})
	}
	return r.stalePathRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// A forge project's name is the key the web joins it to its control-plane
// environments on, so creating the project persists the name discovery read
// from forge.yaml — onto the row and onto the wire — and GetProject serves it
// back with no daemon involved.
func TestCreateProject_PersistsForgeProjectName(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forge-name-" + uuid.New().String()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	s := NewProjectService(repo, &forgeDiscoverRouter{
		stalePathRouter:  stalePathRouter{dirExists: true},
		forgeProjectName: "hounders",
	})

	resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "barksocial",
		Path: "/home/workspace/projects/forge-" + uuid.New().String(),
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetProject().GetIsForge())
	assert.Equal(t, "hounders", resp.Msg.GetProject().GetForgeProjectName())

	row, err := repo.GetProjectWithUserCheck(ctx, resp.Msg.GetProject().GetId(), userID)
	require.NoError(t, err)
	require.NotNil(t, row.ForgeProjectName)
	assert.Equal(t, "hounders", *row.ForgeProjectName)
}

// SetProjectForgeName is what GetTopology calls on every successful forge
// read, so it must be idempotent, follow a rename, and be scoped to the owner.
func TestSetProjectForgeName_IdempotentAndOwnerScoped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forge-name-" + uuid.New().String()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	s := NewProjectService(repo, &stalePathRouter{dirExists: true})
	resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "legacy",
		Path: "/home/workspace/projects/legacy-" + uuid.New().String(),
	}))
	require.NoError(t, err)
	projectID := resp.Msg.GetProject().GetId()
	assert.Nil(t, resp.Msg.GetProject().ForgeProjectName, "never read → unset, not guessed")

	changed, err := repo.SetProjectForgeName(ctx, projectID, userID, "hounders")
	require.NoError(t, err)
	assert.True(t, changed)

	changed, err = repo.SetProjectForgeName(ctx, projectID, userID, "hounders")
	require.NoError(t, err)
	assert.False(t, changed, "an unchanged name is a no-op")

	changed, err = repo.SetProjectForgeName(ctx, projectID, "someone-else", "stolen")
	require.NoError(t, err)
	assert.False(t, changed, "another user's call must not touch the row")

	changed, err = repo.SetProjectForgeName(ctx, projectID, userID, "barksocial")
	require.NoError(t, err)
	assert.True(t, changed)

	row, err := repo.GetProjectWithUserCheck(ctx, projectID, userID)
	require.NoError(t, err)
	assert.True(t, row.IsForge, "naming a project in forge's terms marks it a forge project")
	require.NotNil(t, row.ForgeProjectName)
	assert.Equal(t, "barksocial", *row.ForgeProjectName)
}
