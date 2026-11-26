// Copyright (c) 2025 Reliant Labs

// Package rowreads is the by-id reads of the rows every area shares, served by
// the generated stores over one database handle, for a consumer whose store
// role names both the repository facade's remaining methods and these reads.
//
// The reads check no owner: each caller already holds a row (a chat, a
// trigger) that names the id and decides what its user may see. They keep the
// guards of the repository reads they replace: an empty id fails closed rather
// than reaching the store, and a machine read that fails other than not-found
// names the machine. A handle with no database fails every read.
//
//forge:exclude-contract: a set of by-id reads over a store handle; nothing to fake behind it
package rowreads

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ErrNoDatabase is what a read over a handle with no database answers.
var ErrNoDatabase = errors.New("rowreads: no database")

// Reads is the by-id row reads over one database handle. The zero value reads
// nothing.
type Reads struct {
	daemons store.DaemonExtStore
	repos   store.RepoExtStore
}

// New returns the reads over db. A nil db yields reads that all fail with
// ErrNoDatabase.
func New(db orm.Context) Reads {
	if db == nil {
		return Reads{}
	}
	return Reads{daemons: store.NewDaemonExtStore(db), repos: store.NewRepoExtStore(db)}
}

// Of is the reads over the database handle of s (a *db.Repo), or reads that
// fail every call when s has none.
func Of(s interface{ ORM() *orm.Client }) Reads {
	if s == nil {
		return Reads{}
	}
	c := s.ORM()
	if c == nil {
		return Reads{}
	}
	return New(c)
}

// GetDaemon reads a machine's record; a missing row is svcerr.NotFound.
func (r Reads) GetDaemon(ctx context.Context, id string) (*core.Daemon, error) {
	if r.daemons == nil {
		return nil, ErrNoDatabase
	}
	if id == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	d, err := r.daemons.GetDaemon(ctx, id)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get daemon %s: %w", id, err)
	}
	return d, err
}

// ListReposByProject lists a project's repos, ordered by relative path.
func (r Reads) ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error) {
	if r.repos == nil {
		return nil, ErrNoDatabase
	}
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.repos.ListReposByProject(ctx, projectID)
}

// Repo is the repository facade with the by-id row reads served by the
// generated stores. The facade no longer carries those reads, so the embedded
// Reads are the only source of them.
type Repo struct {
	*db.Repo
	Reads
}

// GetChat retains the computed chat read moved out of the facade on main.
func (r Repo) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return chatviews.Of(r.Repo).GetChat(ctx, id)
}

// WithRepo returns repo with its row reads. A nil repo yields a Repo whose
// every read fails.
func WithRepo(repo *db.Repo) Repo {
	if repo == nil {
		return Repo{}
	}
	return Repo{Repo: repo, Reads: Of(repo)}
}
