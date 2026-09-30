// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A clone dispatched by CreateProjectFromRepo leaves a project_daemons row in
// "installing". Something has to settle it, or the project spins forever on a
// clone that already finished — or already failed. These pin both directions.

// seedInstallingProject creates a project with a queued clone row against a
// daemon, as CreateProjectFromRepo would leave it.
func seedInstallingProject(t *testing.T, repo db.Repository, userID, daemonID, requestID string) *core.Project {
	t.Helper()
	ctx := context.Background()
	project := &core.Project{
		ID:        uuid.NewString(),
		Name:      "widgets-" + uuid.NewString(),
		Path:      "/home/workspace/projects/widgets-" + uuid.NewString(),
		UserID:    userID,
		IsGitRepo: true,
	}
	require.NoError(t, repo.CreateProject(ctx, project))
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, project.ID, daemonID, project.Path, nil, requestID))

	rows, err := repo.ListProjectDaemonsForProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "installing", string(rows[0].InstallState), "precondition: the row starts queued")
	return project
}

func TestQueuedClone_FailureMarksTheProjectFailedWithAReason(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	userID := "user-install-" + uuid.NewString()
	daemonID := uuid.NewString()
	requestID := "clone:req:" + uuid.NewString()
	project := seedInstallingProject(t, repo, userID, daemonID, requestID)

	conn := &daemonConnection{userID: userID, daemonID: daemonID, done: make(chan struct{})}
	require.NoError(t, svc.handleDaemonCommandFailed(context.Background(), conn, &reliantv1.DaemonCommandFailed{
		RequestId:    requestID,
		CommandType:  "git.clone",
		ErrorMessage: "Authentication failed for 'https://github.com/acme/widgets'",
	}))

	rows, err := repo.ListProjectDaemonsForProject(context.Background(), project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "failed", string(rows[0].InstallState),
		"a failed clone must not leave the project stuck in installing")
	assert.Contains(t, rows[0].InstallError, "Authentication failed",
		"the reason is the whole point — without it the user cannot tell a bad token from a bad repo")
}

func TestQueuedClone_FailureWithNoMessageStillReadsAsFailed(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	userID := "user-install-" + uuid.NewString()
	daemonID := uuid.NewString()
	requestID := "clone:req:" + uuid.NewString()
	project := seedInstallingProject(t, repo, userID, daemonID, requestID)

	conn := &daemonConnection{userID: userID, daemonID: daemonID, done: make(chan struct{})}
	require.NoError(t, svc.handleDaemonCommandFailed(context.Background(), conn, &reliantv1.DaemonCommandFailed{
		RequestId:   requestID,
		CommandType: "git.clone",
	}))

	rows, err := repo.ListProjectDaemonsForProject(context.Background(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", string(rows[0].InstallState))
	// A blank reason renders as an empty line pretending to be an
	// explanation, which reads as a rendering bug.
	assert.NotEmpty(t, rows[0].InstallError)
}

func TestQueuedClone_FilesystemAnnouncementSettlesTheInstall(t *testing.T) {
	// The success side carries no request id — only the path — so the row is
	// matched by (project, daemon) instead.
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	userID := "user-install-" + uuid.NewString()
	daemonID := uuid.NewString()
	project := seedInstallingProject(t, repo, userID, daemonID, "clone:req:"+uuid.NewString())

	conn := &daemonConnection{userID: userID, daemonID: daemonID, done: make(chan struct{})}
	require.NoError(t, svc.handleFileSystemChanged(context.Background(), conn, &reliantv1.FileSystemChanged{
		ProjectPath: project.Path,
	}))

	rows, err := repo.ListProjectDaemonsForProject(context.Background(), project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "installed", string(rows[0].InstallState),
		"the daemon reported the directory exists; the project must stop saying it is installing")
}

func TestQueuedClone_UnrelatedCommandFailureLeavesTheInstallAlone(t *testing.T) {
	// Only git.clone failures settle an install row. A failed fs.mkdir on
	// the same daemon must not mark somebody's project as a failed clone.
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	userID := "user-install-" + uuid.NewString()
	daemonID := uuid.NewString()
	requestID := "clone:req:" + uuid.NewString()
	project := seedInstallingProject(t, repo, userID, daemonID, requestID)

	conn := &daemonConnection{userID: userID, daemonID: daemonID, done: make(chan struct{})}
	require.NoError(t, svc.handleDaemonCommandFailed(context.Background(), conn, &reliantv1.DaemonCommandFailed{
		RequestId:    requestID,
		CommandType:  "fs.mkdir",
		ErrorMessage: "permission denied",
	}))

	rows, err := repo.ListProjectDaemonsForProject(context.Background(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, "installing", string(rows[0].InstallState))
}
