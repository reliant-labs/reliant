package tools_daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The project paths a machine registers with are where THAT machine holds
// the user's projects, never where each project was created on another one.
func TestDaemonProjectPaths_AreThisMachinesOwnCheckouts(t *testing.T) {
	asked := ""
	repo := &MockRepository{
		ListProjectDaemonsForDaemonFunc: func(_ context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
			asked = daemonID
			return []*core.ProjectDaemon{
				{ProjectID: "a", DaemonID: daemonID, Path: "/home/workspace/projects/cp", InstallState: core.ProjectInstallInstalled},
				{ProjectID: "b", DaemonID: daemonID, Path: "/home/workspace/projects/cp", InstallState: core.ProjectInstallInstalled},
				{ProjectID: "c", DaemonID: daemonID, Path: "/home/workspace/projects/forge", InstallState: core.ProjectInstallInstalling},
				{ProjectID: "d", DaemonID: daemonID, Path: "/home/workspace/projects/api", InstallState: core.ProjectInstallInstalled},
			}, nil
		},
	}
	got, err := newTestService(repo).daemonProjectPaths(context.Background(), "cloud-box")
	require.NoError(t, err)
	assert.Equal(t, "cloud-box", asked)
	assert.Equal(t, []string{"/home/workspace/projects/api", "/home/workspace/projects/cp"}, got,
		"installed checkouts only, deduplicated and sorted; a clone still landing is not there yet")
}
