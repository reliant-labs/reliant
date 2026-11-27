package mcp

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/store"
)

// newMCPService builds the service over a repository and a daemon router,
// the two collaborators server.go hands it, through the generated harness
// (which fills Logger and Config). The project read is the store over the
// repository's database, as the composition root wires it, or the
// repository itself when it is a fixture that answers it.
func newMCPService(t *testing.T, repo Repository, router Router) *Service {
	t.Helper()
	configs, ok := repo.(ProjectConfigs)
	if !ok {
		configs = store.NewProjectConfigExtStore(repo.(interface{ ORM() *orm.Client }).ORM())
	}
	var rows ProjectReader
	if r, ok := repo.(interface{ ORM() *orm.Client }); ok {
		rows = store.NewProjectExtStore(r.ORM())
	} else if r, ok := repo.(ProjectReader); ok {
		rows = r
	}
	return NewTestMCP(t, WithMCPDeps(Deps{Repo: repo, ProjectRows: rows, Projects: projectaccesstest.For(repo), ProjectConfigs: configs, Router: router}))
}
