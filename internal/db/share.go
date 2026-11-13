// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ApplyShareChange sets a project's, chat's or saved workflow's visibility
// and records it in share_events in one transaction (store.ApplyShareChange).
// It reports whether anything changed: a no-op, a missing row, and a row
// whose owner or org no longer match c write nothing. Deciding whether the
// actor may make the change is the caller's (internal/authz).
//
// ReadCommitted: the UPDATE's own row lock and its re-checked predicate
// (visibility IS [NOT] NULL, owner, org) are what make a concurrent share and
// unshare serialize; the event is written only by the one that changed it.
func (r *Repo) ApplyShareChange(ctx context.Context, c core.ShareChange) (bool, error) {
	var changed bool
	err := r.RunTx(ctx, orm.ReadCommitted, func(txCtx context.Context) error {
		var err error
		changed, err = store.ApplyShareChange(txCtx, r.ORM(), c)
		return err
	})
	return changed, err
}

// ListSharedChats lists the chats shared with an org that the viewer did not
// create (store.ListSharedChats). The caller passes them through authz.
func (r *Repo) ListSharedChats(ctx context.Context, f core.SharedChatFilters) ([]*core.Chat, error) {
	return store.ListSharedChats(ctx, r.ORM(), f)
}

// ListSharedProjects lists the projects shared with an org that the viewer
// did not create (store.ListSharedProjects). The caller passes them through
// authz.
func (r *Repo) ListSharedProjects(ctx context.Context, f core.SharedProjectFilters) ([]*core.Project, error) {
	return store.ListSharedProjects(ctx, r.ORM(), f)
}

// ListShareEvents is one resource's share history, newest first.
func (r *Repo) ListShareEvents(ctx context.Context, kind core.ShareKind, resourceID string) ([]*core.ShareEvent, error) {
	return store.ListShareEvents(ctx, r.ORM(), kind, resourceID)
}

// LatestOrgUpdateSequence is the newest sequence of an org's feed.
func (r *Repo) LatestOrgUpdateSequence(ctx context.Context, orgID string) (int64, error) {
	return store.LatestOrgUpdateSequence(ctx, r.ORM(), orgID)
}

// OrgUpdatesSince reads an org's feed after sinceSeq, ascending.
func (r *Repo) OrgUpdatesSince(ctx context.Context, orgID string, sinceSeq int64, limit int) ([]core.OrgUpdate, error) {
	return store.OrgUpdatesSince(ctx, r.ORM(), orgID, sinceSeq, limit)
}
