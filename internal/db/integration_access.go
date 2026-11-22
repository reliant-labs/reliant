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
