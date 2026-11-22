// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ApplyShareChange sets a project's, chat's or saved workflow's visibility
// and records it in share_events in one transaction (store.ApplyShareChange).
// It reports whether anything changed: a no-op, a missing row, and a row
// whose owner or org no longer match c write nothing. Deciding whether the
// actor may make the change is the caller's (internal/authz).
//
// ListSharedProjects lists the projects shared with an org that the viewer
// did not create (store.ListSharedProjects). The caller passes them through
// authz. The project handler still reads it here; it leaves with that service.
func (r *Repo) ListSharedProjects(ctx context.Context, f core.SharedProjectFilters) ([]*core.Project, error) {
	return store.ListSharedProjects(ctx, r.ORM(), f)
}

// LatestOrgUpdateSequence is the newest sequence of an org's feed.
func (r *Repo) LatestOrgUpdateSequence(ctx context.Context, orgID string) (int64, error) {
	return store.LatestOrgUpdateSequence(ctx, r.ORM(), orgID)
}

// OrgUpdatesSince reads an org's feed after sinceSeq, ascending.
func (r *Repo) OrgUpdatesSince(ctx context.Context, orgID string, sinceSeq int64, limit int) ([]core.OrgUpdate, error) {
	return store.OrgUpdatesSince(ctx, r.ORM(), orgID, sinceSeq, limit)
}
