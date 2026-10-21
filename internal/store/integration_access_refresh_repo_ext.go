// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for IntegrationAccessRefresh. The generated integration_access_refresh_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: integrationAccessRefreshRepo (a *crud.Repo[IntegrationAccessRefresh]) and the
//     package-level GetIntegrationAccessRefreshByPK / ListIntegrationAccessRefresh / CreateIntegrationAccessRefresh / UpsertIntegrationAccessRefresh, keyed by
//     the whole composite key (user_id, integration_id), plus UpdateIntegrationAccessRefresh / DeleteIntegrationAccessRefreshByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     IntegrationAccessRefresh struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindIntegrationAccessRefreshBy(ctx context.Context, db orm.Context, /* args */) ([]*IntegrationAccessRefresh, error) {
//		var rows []*IntegrationAccessRefresh
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("refreshed_at = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveIntegrationAccessRefreshes(ctx context.Context, db orm.Context) ([]*IntegrationAccessRefresh, error) {
//		return ListIntegrationAccessRefresh(ctx, db, orm.WhereEq("refreshed_at", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The refresh bookkeeping behind access-gated routing (P5b lane L5): one row
// per (user, integration) the periodic refresher keeps fresh, with a lease so
// one replica refreshes a pair at a time.

// ListIntegrationTriggerOwners lists the users with at least one enabled
// trigger of the integration: whose access the periodic refresher keeps
// fresh.
func ListIntegrationTriggerOwners(ctx context.Context, db orm.Context, integration string) ([]string, error) {
	owners := []string{}
	err := db.Bun().NewRaw(`
		SELECT DISTINCT t.user_id
		FROM triggers t
		WHERE t.kind = 'integration'
		    AND t.enabled
		    AND t.config ->> 'integration' = ?0
		ORDER BY t.user_id`, integration).
		Scan(ctx, &owners)
	return owners, err
}

// ClaimIntegrationAccessRefresh takes the refresh lease for (user,
// integration) until leaseUntil, when no other replica holds it and the last
// attempt was before dueBefore. One row per pair; the insert covers a pair
// never refreshed. claimed=false means another replica has it, or it is not
// due.
func ClaimIntegrationAccessRefresh(ctx context.Context, db orm.Context, userID, integrationID string, now, leaseUntil, dueBefore time.Time) (bool, error) {
	res, err := db.Bun().NewRaw(`
		INSERT INTO integration_access_refresh (user_id, integration_id, leased_until, last_attempt_at)
		VALUES (?0, ?1, ?2, ?3)
		ON CONFLICT (user_id, integration_id) DO UPDATE SET
		    leased_until = EXCLUDED.leased_until,
		    last_attempt_at = EXCLUDED.last_attempt_at
		WHERE (integration_access_refresh.leased_until IS NULL OR integration_access_refresh.leased_until < ?3)
		    AND (integration_access_refresh.last_attempt_at IS NULL OR integration_access_refresh.last_attempt_at < ?4)`,
		userID, integrationID, leaseUntil.UTC(), now.UTC(), dueBefore.UTC()).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FinishIntegrationAccessRefresh releases the lease and records the outcome:
// refreshed_at moves to at only on success (ok), and lastError replaces the
// stored one (empty on success).
func FinishIntegrationAccessRefresh(ctx context.Context, db orm.Context, userID, integrationID string, at time.Time, ok bool, lastError string) error {
	q := db.Bun().NewUpdate().Model((*IntegrationAccessRefresh)(nil)).
		Set("? = NULL", bun.Ident(IntegrationAccessRefreshFieldLeasedUntil)).
		Set("? = ?", bun.Ident(IntegrationAccessRefreshFieldLastError), lastError)
	if ok {
		q = q.Set("? = ?", bun.Ident(IntegrationAccessRefreshFieldRefreshedAt), at.UTC())
	}
	_, err := q.
		Where("? = ?", bun.Ident(IntegrationAccessRefreshFieldUserID), userID).
		Where("? = ?", bun.Ident(IntegrationAccessRefreshFieldIntegrationID), integrationID).
		Exec(ctx)
	return err
}

// GetIntegrationAccessRefresh returns the refresh bookkeeping of (user,
// integration). A pair never refreshed is svcerr.NotFound, which also
// satisfies errors.Is(err, sql.ErrNoRows).
func GetIntegrationAccessRefresh(ctx context.Context, db orm.Context, userID, integrationID string) (*core.IntegrationAccessRefresh, error) {
	row, err := GetIntegrationAccessRefreshByPK(ctx, db, userID, integrationID)
	if err != nil {
		return nil, err
	}
	return &core.IntegrationAccessRefresh{
		UserID: row.UserID, IntegrationID: row.IntegrationID,
		RefreshedAt: utcPtr(row.RefreshedAt), LastError: row.LastError, LastAttemptAt: utcPtr(row.LastAttemptAt),
	}, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// IntegrationAccessRefreshExtra carries fields on IntegrationAccessRefresh that are NOT columns — computed
// values, transient state, anything derived rather than stored. IntegrationAccessRefresh is
// embedded on IntegrationAccessRefreshExtra (see integration_access_refresh_orm.go), so a field added here is a field on
// every IntegrationAccessRefresh value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type IntegrationAccessRefreshExtra struct {
}
