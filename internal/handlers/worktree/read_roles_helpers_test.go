package worktree

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/store"
)

// WithReadRoles fills the read roles a test left unset (Rows' GetWorktree,
// Projects, ProjectRows, Repos, Daemons) from the test's Repo: the real stores
// over a *db.Repo's ORM, or the same reads a mock repository is programmed
// with, so a fixture keeps programming one mock and the handler reads it
// through the roles it declares. Each mock read is resolved at call time, so
// a test that reprograms the repository after building the service still
// reaches the handler.
func WithReadRoles(d Deps) Deps {
	if r, ok := d.Repo.(*db.Repo); ok && r != nil {
		o := r.ORM()
		if d.Rows == nil {
			d.Rows = store.NewWorktreeExtStore(o)
		}
		if d.ProjectRows == nil {
			d.ProjectRows = store.NewProjectExtStore(o)
		}
		if d.Repos == nil {
			d.Repos = store.NewRepoExtStore(o)
		}
		if d.Chats == nil {
			d.Chats = store.NewChatsWithActivityExtStore(o)
		}
		if d.Daemons == nil {
			d.Daemons = store.NewDaemonExtStore(o)
		}
		if d.Projects == nil {
			d.Projects = projectaccesstest.For(r)
		}
		return d
	}
	repo := d.Repo
	if repo == nil {
		return d
	}
	if d.Rows == nil {
		d.Rows = &MockRows{}
	}
	if m, ok := d.Rows.(*MockRows); ok && m.GetWorktreeFunc == nil {
		m.GetWorktreeFunc = func(ctx context.Context, id string) (*core.Worktree, error) { return repo.GetWorktree(ctx, id) }
	}
	if d.Repos == nil {
		d.Repos = &MockRepoLister{ListReposByProjectFunc: func(ctx context.Context, id string) ([]*core.Repo, error) {
			return repo.ListReposByProject(ctx, id)
		}}
	}
	if d.Chats == nil {
		d.Chats = &MockChats{}
	}
	if d.Daemons == nil {
		d.Daemons = &MockDaemonReader{GetDaemonFunc: func(ctx context.Context, id string) (*core.Daemon, error) { return repo.GetDaemon(ctx, id) }}
	}
	if d.ProjectRows == nil {
		d.ProjectRows = &MockProjectReader{}
	}
	if d.Projects == nil {
		d.Projects = projectaccesstest.For(repo)
	}
	return d
}
