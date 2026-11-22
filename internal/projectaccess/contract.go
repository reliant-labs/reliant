// Copyright (c) 2025 Reliant Labs

// Package projectaccess is the one place a caller's right to WRITE a project
// is decided and its row read in a single call: the owner-filtered store read,
// then authz's org-aware decision on the row it returned.
//
// The store read alone filters on the owner, which is not enough once a project
// belongs to an org: an owner who has left the org, a credential bound to
// another org, or an outage of the membership answer must all read as "no such
// project". Handlers that held *db.Repo got this from
// Repo.GetProjectWithUserCheck; a handler that calls the store directly would
// skip it, so they take this package instead and the check-plus-read stays one
// call (facade plan §1.1).
//
// Every refusal is core.ErrProjectNotFound, so a caller cannot probe which ids
// exist. When the refusal has a reason that is an answer or an outage
// (authz.ErrNotOrgMember, authz.ErrMembershipUnavailable) it is attached as the
// withheld cause (svcerr.WithCause): the launcher tells a creator who left the
// org from a membership outage, which must never be mistaken for an answer.
//
//forge:provided
package projectaccess

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Service reads a project the caller may write.
type Service interface {
	// GetProjectWithUserCheck returns the project when userID owns it and may
	// write it (in its org, through this ctx's credential), else
	// core.ErrProjectNotFound. An empty id or userID is an error, never a read.
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*core.Project, error)
}

// Store is the owner-filtered project read. store.ProjectExtStore satisfies it.
type Store interface {
	// GetProjectWithUserCheck returns the project when it is the user's, else
	// core.ErrProjectNotFound. It decides nothing about the project's org.
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*core.Project, error)
}

// Deps are the service's collaborators.
type Deps struct {
	Store Store
}

// NewFromORM builds the service over the owner-filtered read of db's projects.
func NewFromORM(db orm.Context) Service {
	return New(Deps{Store: store.NewProjectExtStore(db)})
}

// New builds the service over deps.
func New(deps Deps) Service {
	return &service{store: deps.Store}
}
