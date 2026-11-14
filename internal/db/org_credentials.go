// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The lifecycle of an org's credentials (control-plane
// docs/design/org-provider-accounts.md §6): the stale-grant sweep and the
// gone-org purge (internal/orgcredentials). The caller asks control-plane; these
// read and delete reliant's rows.

// ListProviderAccountGrantMembers returns one page of the distinct (org,
// member) pairs holding grants, after the pair after.
func (r *Repo) ListProviderAccountGrantMembers(ctx context.Context, after core.GrantMember, limit int) ([]core.GrantMember, error) {
	return store.ListProviderAccountGrantMembers(ctx, r.ORM(), after, limit)
}

// DeleteProviderAccountGrantsBefore deletes userID's grants in orgID made
// before before.
func (r *Repo) DeleteProviderAccountGrantsBefore(ctx context.Context, orgID, userID string, before time.Time) (int64, error) {
	return store.DeleteProviderAccountGrantsBefore(ctx, r.ORM(), orgID, userID, before)
}

// ListOrgCredentialHolders returns one page of the ids of the orgs reliant
// holds credentials for, after the id after.
func (r *Repo) ListOrgCredentialHolders(ctx context.Context, after string, limit int) ([]string, error) {
	return store.ListOrgCredentialHolders(ctx, r.ORM(), after, limit)
}

// PurgeOrgCredentials removes every credential reliant holds for orgID, which
// the caller has established is gone, in one transaction: all or nothing,
// and a repeat finds nothing (store.PurgeOrgCredentials).
func (r *Repo) PurgeOrgCredentials(ctx context.Context, orgID string) (core.OrgCredentialsPurged, error) {
	var out core.OrgCredentialsPurged
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		out, err = store.PurgeOrgCredentials(ctx, r.ORM(), orgID)
		return err
	})
	return out, err
}
