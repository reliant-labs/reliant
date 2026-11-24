package mcp_test

import (
	"github.com/reliant-labs/forge/pkg/orm"

	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
	"github.com/reliant-labs/reliant/internal/store"
)

// withDeps is mcp.WithMCPDeps with the project read wired as the composition
// root wires it: the store over d.Repo's database when it has one, else
// d.Repo itself when it answers the read (a fixture).
func withDeps(d mcp.Deps) mcp.TestOption {
	if r, ok := d.Repo.(interface{ ORM() *orm.Client }); ok && d.ProjectRows == nil {
		d.ProjectRows = store.NewProjectExtStore(r.ORM())
	}
	if p, ok := d.Repo.(mcp.ProjectReader); ok && d.ProjectRows == nil {
		d.ProjectRows = p
	}
	return mcp.WithMCPDeps(d)
}
