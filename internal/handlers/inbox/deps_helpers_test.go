package inbox

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// noRecoveries is a recovery list with nothing in it.
func noRecoveries() Recoveries {
	return &MockRecoveries{ListWorktreeRecoveriesFunc: func(context.Context, string, string) ([]*core.WorktreeRecovery, error) { return nil, nil }}
}

// realDeps is Deps over a real database: its stores are the generated ones.
func realDeps(repo *db.Repo) Deps {
	return Deps{
		Repo:       repo,
		Recoveries: store.NewWorktreeRecoveryExtStore(repo.ORM()),
		Marks:      store.NewWorktreeExtStore(repo.ORM()),
		OrgKeys:    &MockOrgKeys{},
	}
}

// mockDeps is Deps over repo: an in-flight mock gets its own recovery list; a
// wrapper of a real database gets the generated stores over that database.
func mockDeps(repo Repository) Deps {
	switch r := repo.(type) {
	case *inflightRepo:
		return r.deps()
	case *MockRepository:
		return Deps{Repo: r, Recoveries: noRecoveries(), Marks: &MockMarkDecisionStore{}}
	}
	o := repo.(interface{ ORM() *orm.Client }).ORM()
	return Deps{Repo: repo, Recoveries: store.NewWorktreeRecoveryExtStore(o), Marks: store.NewWorktreeExtStore(o)}
}

// withDeps is WithInboxDeps with the recovery list and marked-worktree store
// every inbox needs filled from d.Repo when d leaves them out.
func withDeps(d Deps) TestOption {
	fill := mockDeps(d.Repo)
	if d.Recoveries == nil {
		d.Recoveries = fill.Recoveries
	}
	if d.Marks == nil {
		d.Marks = fill.Marks
	}
	if d.OrgKeys == nil {
		d.OrgKeys = &MockOrgKeys{}
	}
	return WithInboxDeps(d)
}

// withOrgs sets d's org resolver.
func withOrgs(d Deps, o OrgResolver) Deps {
	d.Orgs = o
	return d
}
