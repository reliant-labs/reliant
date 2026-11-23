package workflow_test

import (
	"github.com/reliant-labs/forge/pkg/orm"

	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/store"
)

// withDeps is workflow.WithWorkflowDeps with the project read wired as the composition
// root wires it: the store over d.Repo's database when it has one, else
// d.Repo itself when it answers the read (a fixture).
func withDeps(d workflow.Deps) workflow.TestOption {
	if r, ok := d.Repo.(interface{ ORM() *orm.Client }); ok && d.ProjectRows == nil {
		d.ProjectRows = store.NewProjectExtStore(r.ORM())
	}
	if p, ok := d.Repo.(workflow.ProjectReader); ok && d.ProjectRows == nil {
		d.ProjectRows = p
	}
	return workflow.WithWorkflowDeps(d)
}
