package project_test

import (
	"context"

	"github.com/reliant-labs/forge/pkg/contractkit"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/store"
)

// stubRepo is a mock repository that also answers the project read the
// handlers take as Deps.ProjectRows, so a fixture programs it beside the rest
// of its store.
type stubRepo struct {
	*project.MockRepository
	GetProjectFunc func(ctx context.Context, id string) (*db.Project, error)
}

func (r *stubRepo) GetProject(ctx context.Context, id string) (*db.Project, error) {
	if r.GetProjectFunc == nil {
		return nil, contractkit.MockNotSet("stubRepo", "GetProject")
	}
	return r.GetProjectFunc(ctx, id)
}

// withDeps is project.WithProjectDeps with the read roles wired as the
// composition root wires them: the stores over d.Repo's database when it has
// one, else d.Repo itself when it answers the read (a fixture).
func withDeps(d project.Deps) project.TestOption {
	if r, ok := d.Repo.(interface{ ORM() *orm.Client }); ok {
		if d.ProjectRows == nil {
			d.ProjectRows = store.NewProjectExtStore(r.ORM())
		}
		if d.Machines == nil {
			d.Machines = store.NewDaemonExtStore(r.ORM())
		}
		if d.Repos == nil {
			d.Repos = store.NewRepoExtStore(r.ORM())
		}
	}
	if p, ok := d.Repo.(project.ProjectReader); ok && d.ProjectRows == nil {
		d.ProjectRows = p
	}
	if m, ok := d.Repo.(project.DaemonReader); ok && d.Machines == nil {
		d.Machines = m
	}
	if l, ok := d.Repo.(project.RepoLister); ok && d.Repos == nil {
		d.Repos = l
	}
	return project.WithProjectDeps(d)
}
