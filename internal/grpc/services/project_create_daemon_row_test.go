// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// createProjectRowRouter drives the two daemon states CreateProject must
// distinguish: a connected daemon (mkdir returns success) vs. a pending one
// (mkdir errors, command is enqueued for a later connect). resolveID is the
// daemon id ResolveDaemonID reports, and mkdirFails flips fs.mkdir into the
// pending path.
type createProjectRowRouter struct {
	worktreeTestDaemonRouter
	resolveID  string
	mkdirFails bool
	enqueued   int
}

func (r *createProjectRowRouter) ResolveDaemonID(_ context.Context, _ string) (string, error) {
	if r.resolveID == "" {
		return "", fmt.Errorf("no daemon available")
	}
	return r.resolveID, nil
}

func (r *createProjectRowRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *createProjectRowRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	switch commandType {
	case "fs.mkdir":
		if r.mkdirFails {
			return nil, fmt.Errorf("no daemon connected")
		}
		return json.Marshal(map[string]any{})
	case "repo.discover":
		return json.Marshal(map[string]any{"discovered": []any{}})
	case "project.init_git_repo":
		return json.Marshal(map[string]any{"success": true})
	default:
		return json.Marshal(map[string]any{})
	}
}

func (r *createProjectRowRouter) EnqueueDaemonCommand(_ context.Context, _, _ string, _ []byte, _ int32) (int, error) {
	r.enqueued++
	return 1, nil
}

// TestCreateProjectRecordsProjectDaemonRow_ConnectedDaemon pins the immediacy
// fix: when a daemon is connected and resolvable, CreateProject writes a
// project_daemons row against the resolved daemon id and project path so the
// picker marks the fresh project openable without a reconnect.
func TestCreateProjectRecordsProjectDaemonRow_ConnectedDaemon(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-create-project-daemon-row"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	router := &createProjectRowRouter{resolveID: "daemon-abc"}
	s := NewProjectService(repo, router)

	// Unique path: the test DB is shared, so a fixed path collides across runs.
	projectPath := "/home/workspace/projects/connected-" + uuid.New().String()
	resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "connected-project",
		Path: projectPath,
	}))
	require.NoError(t, err)

	rows, err := repo.ListProjectDaemonsForProject(ctx, resp.Msg.Project.Id)
	require.NoError(t, err)
	require.Len(t, rows, 1, "connected daemon must get a project_daemons row at creation")
	assert.Equal(t, "daemon-abc", rows[0].DaemonID)
	assert.Equal(t, projectPath, rows[0].Path)
}

// TestCreateProjectSkipsProjectDaemonRow_PendingDaemon covers the provisioning
// race: no daemon is connected yet (mkdir fails and is enqueued). Creation must
// still succeed, but NO project_daemons row is written — the reconcile-on-connect
// flow will create it once the daemon comes up.
func TestCreateProjectSkipsProjectDaemonRow_PendingDaemon(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-create-project-pending"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	// resolveID empty => ResolveDaemonID errors; mkdir also fails => pending path.
	router := &createProjectRowRouter{mkdirFails: true}
	s := NewProjectService(repo, router)

	projectPath := "/home/workspace/projects/pending-" + uuid.New().String()
	resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "pending-project",
		Path: projectPath,
	}))
	require.NoError(t, err, "creation must still succeed when the daemon is pending")
	require.Positive(t, router.enqueued, "mkdir should be enqueued for the pending daemon")

	rows, err := repo.ListProjectDaemonsForProject(ctx, resp.Msg.Project.Id)
	require.NoError(t, err)
	assert.Empty(t, rows, "pending daemon must NOT get a project_daemons row (reconcile heals it)")
}