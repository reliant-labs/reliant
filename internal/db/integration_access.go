// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// Access-gated routing for app-level provider events. The model is in the
// migration 20261005061008_integration_event_access.sql: an event about a
// resource (a GitHub repository) reaches a trigger only when its owner's own
// provider credential recently reported that resource as visible.

// ReplaceIntegrationAccess records a refresh: the user's grants for the
// integration become exactly grants, all stamped at. It is one transaction,
// so routing never sees a half-replaced set — in particular never the moment
// between dropping a lost repository and writing the rest.
func (r *Repo) ReplaceIntegrationAccess(ctx context.Context, userID, integrationID, subjectID string, at time.Time, grants []core.IntegrationAccessGrant) error {
	if userID == "" || integrationID == "" {
		return errors.New("integration access: user and integration are required")
	}
	accounts := make([]string, 0, len(grants))
	resources := make([]string, 0, len(grants))
	labels := make([]string, 0, len(grants))
	for _, g := range grants {
		if g.AccountKey == "" || g.ResourceKey == "" {
			return fmt.Errorf("integration access: a grant needs an account and a resource, got %+v", g)
		}
		accounts = append(accounts, g.AccountKey)
		resources = append(resources, g.ResourceKey)
		labels = append(labels, g.ResourceLabel)
	}
	at = at.UTC()
	return r.RunTx(ctx, func(ctx context.Context) error {
		q := pgdb.New(r.DB.DB(ctx))
		if len(grants) > 0 {
			if err := q.UpsertIntegrationAccess(ctx, pgdb.UpsertIntegrationAccessParams{
				UserID: userID, IntegrationID: integrationID, SubjectID: subjectID, RefreshedAt: at,
				AccountKeys: accounts, ResourceKeys: resources, ResourceLabels: labels,
			}); err != nil {
				return fmt.Errorf("record integration access: %w", err)
			}
		}
		if _, err := q.DeleteStaleIntegrationAccess(ctx, pgdb.DeleteStaleIntegrationAccessParams{
			UserID: userID, IntegrationID: integrationID, RefreshedAt: at,
		}); err != nil {
			return fmt.Errorf("drop lost integration access: %w", err)
		}
		return nil
	})
}

// RevokeIntegrationAccess deletes the grants a provider revocation names, for
// every user. It returns how many it removed.
func (r *Repo) RevokeIntegrationAccess(ctx context.Context, integrationID string, rev core.IntegrationAccessRevocation) (int64, error) {
	if integrationID == "" || (rev.AccountKey == "" && rev.SubjectID == "") {
		return 0, fmt.Errorf("integration access: a revocation must name an account or a subject, got %+v", rev)
	}
	n, err := pgdb.New(r.DB.DB(ctx)).RevokeIntegrationAccess(ctx, pgdb.RevokeIntegrationAccessParams{
		IntegrationID: integrationID, AccountKey: rev.AccountKey, ResourceKey: rev.ResourceKey, SubjectID: rev.SubjectID,
	})
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
	rows, err := pgdb.New(r.DB.DB(ctx)).ListAccessRoutedTriggers(ctx, pgdb.ListAccessRoutedTriggersParams{
		Integration: integration, AccountKey: account, ResourceKey: resource, FreshAfter: freshAfter.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("list access-routed triggers: %w", err)
	}
	out := make([]*core.Trigger, 0, len(rows))
	for _, row := range rows {
		t, err := postgresstore.TriggerFromRow(row.Trigger)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ListIntegrationTriggerOwners lists the users with an enabled trigger of the
// integration: whose access the refresher keeps fresh.
func (r *Repo) ListIntegrationTriggerOwners(ctx context.Context, integration string) ([]string, error) {
	owners, err := pgdb.New(r.DB.DB(ctx)).ListIntegrationTriggerOwners(ctx, integration)
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
	n, err := pgdb.New(r.DB.DB(ctx)).ClaimIntegrationAccessRefresh(ctx, pgdb.ClaimIntegrationAccessRefreshParams{
		UserID: userID, IntegrationID: integrationID, LeasedUntil: leaseUntil.UTC(), Now: now.UTC(), DueBefore: dueBefore.UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("claim integration access refresh: %w", err)
	}
	return n > 0, nil
}

// FinishIntegrationAccessRefresh releases the lease and records the outcome:
// refreshErr nil is a success at at.
func (r *Repo) FinishIntegrationAccessRefresh(ctx context.Context, userID, integrationID string, at time.Time, refreshErr error) error {
	params := pgdb.FinishIntegrationAccessRefreshParams{
		Ok: refreshErr == nil, At: at.UTC(), UserID: userID, IntegrationID: integrationID,
	}
	if refreshErr != nil {
		params.LastError = truncateString(refreshErr.Error(), 500)
	}
	if err := pgdb.New(r.DB.DB(ctx)).FinishIntegrationAccessRefresh(ctx, params); err != nil {
		return fmt.Errorf("finish integration access refresh: %w", err)
	}
	return nil
}

// GetIntegrationAccessRefresh returns the refresh bookkeeping, or
// sql.ErrNoRows when the pair was never refreshed.
func (r *Repo) GetIntegrationAccessRefresh(ctx context.Context, userID, integrationID string) (*core.IntegrationAccessRefresh, error) {
	row, err := pgdb.New(r.DB.DB(ctx)).GetIntegrationAccessRefresh(ctx, pgdb.GetIntegrationAccessRefreshParams{
		UserID: userID, IntegrationID: integrationID,
	})
	if err != nil {
		return nil, err
	}
	return &core.IntegrationAccessRefresh{
		UserID: row.UserID, IntegrationID: row.IntegrationID,
		RefreshedAt: nullTimePtr(row.RefreshedAt), LastError: row.LastError, LastAttemptAt: nullTimePtr(row.LastAttemptAt),
	}, nil
}

func nullTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// PruneIntegrationAccess deletes grants last refreshed before before.
func (r *Repo) PruneIntegrationAccess(ctx context.Context, before time.Time) (int64, error) {
	n, err := pgdb.New(r.DB.DB(ctx)).PruneIntegrationAccess(ctx, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune integration access: %w", err)
	}
	return n, nil
}
