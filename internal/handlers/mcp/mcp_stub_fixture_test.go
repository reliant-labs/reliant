package mcp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
)

// daemonFunc answers a daemon command the way the caller's daemon would.
type daemonFunc func(commandType string, payload []byte) ([]byte, error)

func (f daemonFunc) SendDaemonCommand(_ context.Context, _, commandType string, payload []byte, _ int32) ([]byte, error) {
	return f(commandType, payload)
}

// ownedProjectRepo is a store where the harness caller (test-user) owns
// project p-1 at /work, whose stored project-scope MCP configuration is
// mcpConfigs. Every other project is not found.
func ownedProjectRepo(t *testing.T, mcpConfigs string) *mcp.StubRepo {
	t.Helper()
	return &mcp.StubRepo{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == "p-1" && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj", Path: "/work"}, nil
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
	}
}

// newService builds the service over store. router is the caller's daemon;
// nil builds it without a daemon router.
func newService(t *testing.T, store mcp.Repository, router mcp.Router) *mcp.Service {
	t.Helper()
	return mcp.NewTestMCP(t, mcp.WithMCPDeps(mcp.Deps{Repo: store, Router: router}))
}
