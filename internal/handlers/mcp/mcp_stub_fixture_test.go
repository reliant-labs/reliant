package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
)

// daemonFunc answers a daemon command the way the machine holding the
// checkout would. It answers only commands pinned to that machine
// (checkoutDaemon): a command for any other is the bug.
type daemonFunc func(commandType string, payload []byte) ([]byte, error)

func (f daemonFunc) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if daemonID != checkoutDaemon {
		return nil, fmt.Errorf("%s sent to %q, not to %q, the machine holding the checkout", commandType, daemonID, checkoutDaemon)
	}
	return f(commandType, payload)
}

func (daemonFunc) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	return daemonID, nil
}

// checkoutDaemon holds p-1's checkout, at /work. The project row's own path
// is another machine's (creatorPath), so every test asserting /work also
// proves the per-machine path is the one sent.
const (
	checkoutDaemon = "daemon-1"
	creatorPath    = "/Users/creator/src/proj"
)

// ownedProjectRepo is a store where the harness caller (test-user) owns
// project p-1, checked out at /work on checkoutDaemon, whose stored project-scope MCP configuration is
// mcpConfigs. Every other project is not found.
func ownedProjectRepo(t *testing.T, mcpConfigs string) *mcp.MockRepository {
	t.Helper()
	return &mcp.MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == "p-1" && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj", Path: creatorPath}, nil
			}
			// The store's real answer for a project the caller does not own.
			return nil, core.ErrProjectNotFound
		},
		GetProjectMCPConfigsJSONFunc: func(_ context.Context, projectID string) (*string, error) {
			if projectID != "p-1" {
				return nil, errors.New("project config not found")
			}
			return &mcpConfigs, nil
		},
		ListProjectDaemonsForProjectFunc: func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
			return []*core.ProjectDaemon{{ProjectID: projectID, DaemonID: checkoutDaemon, Path: "/work", InstallState: core.ProjectInstallInstalled}}, nil
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			return &db.Daemon{ID: id, UserID: "test-user"}, nil
		},
	}
}

// newService builds the service over store. router is the caller's daemon;
// nil builds it without a daemon router.
func newService(t *testing.T, store mcp.Repository, router mcp.Router) *mcp.Service {
	t.Helper()
	return mcp.NewTestMCP(t, mcp.WithMCPDeps(mcp.Deps{Repo: store, Router: router}))
}
