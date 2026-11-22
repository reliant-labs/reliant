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
		Items:      store.NewInboxDismissalExtStore(repo.ORM()),
		Firings:    store.NewTriggerEventExtStore(repo.ORM()),
		Notices:    store.NewTriggerExtStore(repo.ORM()),
		Recoveries: store.NewWorktreeRecoveryExtStore(repo.ORM()),
		Marks:      store.NewWorktreeExtStore(repo.ORM()),
	}
}

// mockDeps is Deps over repo: an in-flight mock gets its own recovery list; a
// wrapper of a real database gets the generated stores over that database.
func mockDeps(repo Repository) Deps {
	switch r := repo.(type) {
	case *inflightRepo:
		return r.deps()
	case *MockRepository:
		return Deps{Repo: r, Items: &MockItems{}, Firings: &MockFirings{}, Notices: &MockNotices{}, Recoveries: noRecoveries(), Marks: &MockMarkDecisionStore{}}
	}
	o := repo.(interface{ ORM() *orm.Client }).ORM()
	return Deps{Repo: repo, Items: store.NewInboxDismissalExtStore(o), Firings: store.NewTriggerEventExtStore(o), Notices: store.NewTriggerExtStore(o), Recoveries: store.NewWorktreeRecoveryExtStore(o), Marks: store.NewWorktreeExtStore(o)}
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
	if d.Items == nil {
		d.Items = fill.Items
	}
	if d.Firings == nil {
		d.Firings = fill.Firings
	}
	if d.Notices == nil {
		d.Notices = fill.Notices
	}
	return WithInboxDeps(d)
}

// withOrgs sets d's org resolver.
func withOrgs(d Deps, o OrgResolver) Deps {
	d.Orgs = o
	return d
}
