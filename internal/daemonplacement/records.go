// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Records reads the rows placement decides over — a machine, a worktree, a
// project, and where a project is installed — from the generated stores. It
// satisfies WorktreeReader, DaemonReader and Installs (and, with a GetChat
// beside it, WorkspaceReader), so a handler can carry it instead of reaching
// these reads through *db.Repo.
//
// Like the *db.Repo methods it replaces it checks no owner: every caller
// checks the returned row against the requester (authz, ErrForeignWorkspace).
// It keeps their behaviour too: an empty id is an error, and a daemon read
// that fails other than not-found names the daemon.
type Records struct {
	daemons        store.DaemonExtStore
	worktrees      store.WorktreeExtStore
	projects       store.ProjectExtStore
	projectDaemons store.ProjectDaemonExtStore
}

// NewRecords returns the placement reads over db.
func NewRecords(db orm.Context) Records {
	return Records{
		daemons:        store.NewDaemonExtStore(db),
		worktrees:      store.NewWorktreeExtStore(db),
		projects:       store.NewProjectExtStore(db),
		projectDaemons: store.NewProjectDaemonExtStore(db),
	}
}

// GetDaemon reads a machine's record.
func (r Records) GetDaemon(ctx context.Context, id string) (*db.Daemon, error) {
	if id == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	d, err := r.daemons.GetDaemon(ctx, id)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get daemon %s: %w", id, err)
	}
	return d, err
}

// GetWorktree reads a worktree row.
func (r Records) GetWorktree(ctx context.Context, id string) (*db.Worktree, error) {
	if id == "" {
		return nil, fmt.Errorf("worktree ID cannot be empty")
	}
	return r.worktrees.GetWorktree(ctx, id)
}

// GetProject reads a project row.
func (r Records) GetProject(ctx context.Context, id string) (*db.Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.projects.GetProject(ctx, id)
}

// ListProjectDaemonsForProject lists the machines a project is installed on.
func (r Records) ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.projectDaemons.ListProjectDaemonsForProject(ctx, projectID)
}
