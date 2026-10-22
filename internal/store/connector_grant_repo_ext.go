// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ConnectorGrant. The generated connector_grant_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: connectorGrantRepo (a *crud.Repo[ConnectorGrant]) and the
//     package-level GetConnectorGrantByID / ListConnectorGrant / CreateConnectorGrant / UpdateConnectorGrant / DeleteConnectorGrant
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ConnectorGrant struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindConnectorGrantBy(ctx context.Context, db orm.Context, /* args */) ([]*ConnectorGrant, error) {
//		var rows []*ConnectorGrant
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveConnectorGrants(ctx context.Context, db orm.Context) ([]*ConnectorGrant, error) {
//		return ListConnectorGrant(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
)

// ConnectorGrantExtra carries fields on ConnectorGrant that are NOT columns — computed
// values, transient state, anything derived rather than stored. ConnectorGrant is
// embedded on ConnectorGrantExtra (see connector_grant_orm.go), so a field added here is a field on
// every ConnectorGrant value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ConnectorGrantExtra struct {
}

// Connector grant queries the generated CRUD does not cover (P5b lane L5).
// internal/connectorgrant maps the rows onto its own Grant.

// GetLiveConnectorGrant returns a grant by id only while it is live — not
// revoked and not expired. Anything else is svcerr.NotFound (and
// orm.ErrNoRows), so a revoked or expired grant cannot be re-resolved.
func GetLiveConnectorGrant(ctx context.Context, db orm.Context, id string) (*ConnectorGrant, error) {
	var row ConnectorGrant
	err := db.Bun().NewSelect().Model(&row).
		Where("? = ?", bun.Ident(ConnectorGrantFieldID), id).
		Where("? IS NULL", bun.Ident(ConnectorGrantFieldRevokedAt)).
		Where("(? IS NULL OR ? > now())", bun.Ident(ConnectorGrantFieldExpiresAt), bun.Ident(ConnectorGrantFieldExpiresAt)).
		Scan(ctx)
	if err != nil {
		return nil, classifyMissing(err, "connector grant")
	}
	return &row, nil
}

// RevokeConnectorGrant marks the user's live grant revoked and reports
// whether it did, so a repeated revoke is distinguishable from one that took
// effect. Scoped by user so a caller can never revoke someone else's grant
// by guessing an id.
func RevokeConnectorGrant(ctx context.Context, db orm.Context, userID, id string) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*ConnectorGrant)(nil)).
		Set("? = now()", bun.Ident(ConnectorGrantFieldRevokedAt)).
		Set("? = now()", bun.Ident(ConnectorGrantFieldUpdatedAt)).
		Where("? = ?", bun.Ident(ConnectorGrantFieldUserID), userID).
		Where("? = ?", bun.Ident(ConnectorGrantFieldID), id).
		Where("? IS NULL", bun.Ident(ConnectorGrantFieldRevokedAt)).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchConnectorGrant stamps last use. It is telemetry: updated_at is left
// alone, and a missing grant is no error.
func TouchConnectorGrant(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().NewUpdate().Model((*ConnectorGrant)(nil)).
		Set("? = now()", bun.Ident(ConnectorGrantFieldLastUsedAt)).
		Where("? = ?", bun.Ident(ConnectorGrantFieldID), id).
		Exec(ctx)
	return err
}

// SetConnectorGrantTokenPrefix records the display prefix of a grant's
// credential once it is minted.
func SetConnectorGrantTokenPrefix(ctx context.Context, db orm.Context, id, prefix string) error {
	_, err := db.Bun().NewUpdate().Model((*ConnectorGrant)(nil)).
		Set("? = ?", bun.Ident(ConnectorGrantFieldTokenPrefix), prefix).
		Set("? = now()", bun.Ident(ConnectorGrantFieldUpdatedAt)).
		Where("? = ?", bun.Ident(ConnectorGrantFieldID), id).
		Exec(ctx)
	return err
}
