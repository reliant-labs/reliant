// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Access-gated routing for app-level provider events. The model is in the
// migration 20261005061008_integration_event_access.sql: an event about a
// resource (a GitHub repository) reaches a trigger only when its owner's own
// provider credential recently reported that resource as visible.

// ReplaceIntegrationAccess records a refresh: the user's grants for the
// integration become exactly grants, all stamped at, in one transaction.
func (r *Repo) ReplaceIntegrationAccess(ctx context.Context, userID, integrationID, subjectID string, at time.Time, grants []core.IntegrationAccessGrant) error {
	return store.ReplaceIntegrationAccess(ctx, r.ORM(), userID, integrationID, subjectID, at, grants)
}

// GetIntegrationAccessSubject is the user's id at the provider as the access
// refresher last recorded it, or "" when nothing refreshed at or after
// freshAfter records one.
func (r *Repo) GetIntegrationAccessSubject(ctx context.Context, userID, integrationID string, freshAfter time.Time) (string, error) {
	return store.GetIntegrationAccessSubject(ctx, r.ORM(), userID, integrationID, freshAfter)
}

// RevokeIntegrationAccess deletes the grants a provider revocation names, for
// every user. It returns how many it removed.
func (r *Repo) RevokeIntegrationAccess(ctx context.Context, integrationID string, rev core.IntegrationAccessRevocation) (int64, error) {
	n, err := store.RevokeIntegrationAccess(ctx, r.ORM(), integrationID, rev)
	if err != nil {
		return 0, fmt.Errorf("revoke integration access: %w", err)
	}
	return n, nil
}

// ListAccessRoutedTriggers lists the enabled integration triggers an event
// about (account, resource) reaches: those whose owner holds a grant for it
// refreshed at or after freshAfter, and whose named connection (if any) is
// the owner's and active.
func (r *Repo) ListAccessRoutedTriggers(ctx context.Context, integration, account, resource string, freshAfter time.Time) ([]*core.Trigger, error) {
	return store.ListAccessRoutedTriggers(ctx, r.ORM(), integration, account, resource, freshAfter)
}

// ListIntegrationTriggerOwners lists the users with an enabled trigger of the
// integration: whose access the refresher keeps fresh.
func (r *Repo) ListIntegrationTriggerOwners(ctx context.Context, integration string) ([]string, error) {
	owners, err := store.ListIntegrationTriggerOwners(ctx, r.ORM(), integration)
	if err != nil {
		return nil, fmt.Errorf("list integration trigger owners: %w", err)
	}
	return owners, nil
}

// ClaimIntegrationAccessRefresh takes the refresh lease for (user,
// integration) until leaseUntil, when no other replica holds it and the last
// attempt was before dueBefore. claimed=false means another replica has it,
// or it is not due.
func (r *Repo) ClaimIntegrationAccessRefresh(ctx context.Context, userID, integrationID string, now, leaseUntil, dueBefore time.Time) (bool, error) {
	claimed, err := store.ClaimIntegrationAccessRefresh(ctx, r.ORM(), userID, integrationID, now, leaseUntil, dueBefore)
	if err != nil {
		return false, fmt.Errorf("claim integration access refresh: %w", err)
	}
	return claimed, nil
}

// FinishIntegrationAccessRefresh releases the lease and records the outcome:
// refreshErr nil is a success at at.
func (r *Repo) FinishIntegrationAccessRefresh(ctx context.Context, userID, integrationID string, at time.Time, refreshErr error) error {
	lastError := ""
	if refreshErr != nil {
		lastError = truncateString(refreshErr.Error(), 500)
	}
	if err := store.FinishIntegrationAccessRefresh(ctx, r.ORM(), userID, integrationID, at, refreshErr == nil, lastError); err != nil {
		return fmt.Errorf("finish integration access refresh: %w", err)
	}
	return nil
}

// GetIntegrationAccessRefresh returns the refresh bookkeeping, or
// sql.ErrNoRows when the pair was never refreshed.
func (r *Repo) GetIntegrationAccessRefresh(ctx context.Context, userID, integrationID string) (*core.IntegrationAccessRefresh, error) {
	return store.GetIntegrationAccessRefresh(ctx, r.ORM(), userID, integrationID)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// PruneIntegrationAccess deletes grants last refreshed before before.
func (r *Repo) PruneIntegrationAccess(ctx context.Context, before time.Time) (int64, error) {
	n, err := store.PruneIntegrationAccess(ctx, r.ORM(), before)
	if err != nil {
		return 0, fmt.Errorf("prune integration access: %w", err)
	}
	return n, nil
}
