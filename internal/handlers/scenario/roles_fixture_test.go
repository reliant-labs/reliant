package scenario_test

import (
	"context"

	"github.com/reliant-labs/forge/pkg/contractkit"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
	"github.com/reliant-labs/reliant/internal/store"
)

// stubRepo is a mock repository that also answers the project read the
// handlers take as Deps.ProjectRows, so a fixture programs it beside the rest
// of its store.
type stubRepo struct {
	*scenario.MockRepository
	GetProjectFunc func(ctx context.Context, id string) (*db.Project, error)
}

func (r *stubRepo) GetProject(ctx context.Context, id string) (*db.Project, error) {
	if r.GetProjectFunc == nil {
		return nil, contractkit.MockNotSet("stubRepo", "GetProject")
	}
	return r.GetProjectFunc(ctx, id)
}

// withDeps is scenario.WithScenarioDeps with the project read wired as the composition
// root wires it: the store over d.Repo's database when it has one, else
// d.Repo itself when it answers the read (a fixture).
func withDeps(d scenario.Deps) scenario.TestOption {
	if r, ok := d.Repo.(interface{ ORM() *orm.Client }); ok && d.ProjectRows == nil {
		d.ProjectRows = store.NewProjectExtStore(r.ORM())
	}
	if p, ok := d.Repo.(scenario.ProjectReader); ok && d.ProjectRows == nil {
		d.ProjectRows = p
	}
	return scenario.WithScenarioDeps(d)
}
