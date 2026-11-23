package mcp

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/store"
)

// newMCPService builds the service over a repository and a daemon router,
// the two collaborators server.go hands it, through the generated harness
// (which fills Logger and Config).
func newMCPService(t *testing.T, repo Repository, router Router) *Service {
	t.Helper()
	configs, ok := repo.(ProjectConfigs)
	if !ok {
		configs = store.NewProjectConfigExtStore(repo.(*db.Repo).ORM())
	}
	return NewTestMCP(t, WithMCPDeps(Deps{Repo: repo, Projects: projectaccesstest.For(repo), ProjectConfigs: configs, Router: router}))
}
