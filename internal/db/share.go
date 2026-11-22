// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// LatestOrgUpdateSequence is the newest sequence of an org's feed.
func (r *Repo) LatestOrgUpdateSequence(ctx context.Context, orgID string) (int64, error) {
	return store.LatestOrgUpdateSequence(ctx, r.ORM(), orgID)
}

// OrgUpdatesSince reads an org's feed after sinceSeq, ascending.
func (r *Repo) OrgUpdatesSince(ctx context.Context, orgID string, sinceSeq int64, limit int) ([]core.OrgUpdate, error) {
	return store.OrgUpdatesSince(ctx, r.ORM(), orgID, sinceSeq, limit)
}
