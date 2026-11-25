package inbox_test

import (
	"github.com/reliant-labs/forge/pkg/orm"

	inbox "github.com/reliant-labs/reliant/internal/handlers/inbox"
	"github.com/reliant-labs/reliant/internal/store"
)

// withDeps is inbox.WithInboxDeps with the project read wired as the composition
// root wires it: the store over d.Repo's database when it has one, else
// d.Repo itself when it answers the read (a fixture).
func withDeps(d inbox.Deps) inbox.TestOption {
	if r, ok := d.Repo.(interface{ ORM() *orm.Client }); ok && d.ProjectRows == nil {
		d.ProjectRows = store.NewProjectExtStore(r.ORM())
	}
	if p, ok := d.Repo.(inbox.ProjectReader); ok && d.ProjectRows == nil {
		d.ProjectRows = p
	}
	if m, ok := d.Repo.(interface{ ORM() *orm.Client }); ok && d.Machines == nil {
		d.Machines = store.NewDaemonExtStore(m.ORM())
	}
	return inbox.WithInboxDeps(d)
}
