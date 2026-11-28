package inbox

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// noRecoveries is a recovery list with nothing in it.
func noRecoveries() Recoveries {
	return &MockRecoveries{ListWorktreeRecoveriesFunc: func(context.Context, string, string) ([]*core.WorktreeRecovery, error) { return nil, nil }}
}

// realDeps is Deps over a real database: its stores are the generated ones.
func realDeps(repo *db.Repo) Deps {
	return Deps{
		Repo:          repo,
		Chats:         store.NewChatsWithActivityExtStore(repo.ORM()),
		ProjectRows:   store.NewProjectExtStore(repo.ORM()),
		Machines:      store.NewDaemonExtStore(repo.ORM()),
		Items:         store.NewInboxDismissalExtStore(repo.ORM()),
		Firings:       store.NewTriggerEventExtStore(repo.ORM()),
		Notices:       store.NewTriggerExtStore(repo.ORM()),
		Registrations: store.NewTriggerRegistrationExtStore(repo.ORM()),
		Recoveries:    store.NewWorktreeRecoveryExtStore(repo.ORM()),
		Marks:         store.NewWorktreeExtStore(repo.ORM()),
		OrgKeys:       &MockOrgKeys{},
	}
}

// mockDeps is Deps over repo: an in-flight mock gets its own recovery list; a
// wrapper of a real database gets the generated stores over that database.
func mockDeps(repo Repository) Deps {
	switch r := repo.(type) {
	case *inflightRepo:
		return r.deps()
	case *MockRepository:
		return Deps{Repo: r, Chats: &MockChats{}, ProjectRows: &MockProjectReader{}, Machines: &MockDaemonReader{}, Items: &MockItems{}, Firings: &MockFirings{}, Notices: &MockNotices{}, Registrations: &MockRegistrations{}, Storage: &MockStorage{}, Recoveries: noRecoveries(), Marks: &MockMarkDecisionStore{}}
	}
	o := repo.(interface{ ORM() *orm.Client }).ORM()
	return Deps{Repo: repo, Chats: store.NewChatsWithActivityExtStore(o), ProjectRows: store.NewProjectExtStore(o), Machines: store.NewDaemonExtStore(o), Items: store.NewInboxDismissalExtStore(o), Firings: store.NewTriggerEventExtStore(o), Notices: store.NewTriggerExtStore(o), Registrations: store.NewTriggerRegistrationExtStore(o), Storage: realStorage(repo), Recoveries: store.NewWorktreeRecoveryExtStore(o), Marks: store.NewWorktreeExtStore(o)}
}

// realStorage is the sweep's store over a real repository, as the composition
// root wires it.
func realStorage(repo Repository) worktreesweep.Stores {
	r := repo.(interface {
		ORM() *orm.Client
		ListReposByProject(context.Context, string) ([]*core.Repo, error)
		UpdateWorktreeCleanupMetadata(context.Context, string, *core.CleanupMetadata) error
		ListAttachedDaemonIDsForUser(context.Context, string, time.Duration) ([]string, error)
		TryAdvisoryLock(context.Context, int64) (func(), bool, error)
		ListDaemonStorageState(context.Context, string) (map[string]string, error)
	})
	return worktreesweep.NewStores(r, store.NewWorktreeExtStore(r.ORM()))
}

// withDeps is WithInboxDeps with the recovery list and marked-worktree store
// every inbox needs filled from d.Repo when d leaves them out.
func withDeps(d Deps) TestOption {
	fill := mockDeps(d.Repo)
	if d.ProjectRows == nil {
		d.ProjectRows = fill.ProjectRows
	}
	if d.Machines == nil {
		d.Machines = fill.Machines
	}
	if d.Recoveries == nil {
		d.Recoveries = fill.Recoveries
	}
	if d.Marks == nil {
		d.Marks = fill.Marks
	}
	if d.OrgKeys == nil {
		d.OrgKeys = &MockOrgKeys{}
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
	if d.Registrations == nil {
		d.Registrations = fill.Registrations
	}
	if d.Storage == nil {
		d.Storage = fill.Storage
	}
	return WithInboxDeps(d)
}

// withOrgs sets d's org resolver.
func withOrgs(d Deps, o OrgResolver) Deps {
	d.Orgs = o
	return d
}
