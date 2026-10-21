// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ConnectorClientBinding. The generated connector_client_binding_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: connectorClientBindingRepo (a *crud.Repo[ConnectorClientBinding]) and the
//     package-level GetConnectorClientBindingByID / ListConnectorClientBinding / CreateConnectorClientBinding / UpdateConnectorClientBinding / DeleteConnectorClientBinding
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ConnectorClientBinding struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindConnectorClientBindingBy(ctx context.Context, db orm.Context, /* args */) ([]*ConnectorClientBinding, error) {
//		var rows []*ConnectorClientBinding
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveConnectorClientBindings(ctx context.Context, db orm.Context) ([]*ConnectorClientBinding, error) {
//		return ListConnectorClientBinding(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
)

// ConnectorClientBindingExtra carries fields on ConnectorClientBinding that are NOT columns — computed
// values, transient state, anything derived rather than stored. ConnectorClientBinding is
// embedded on ConnectorClientBindingExtra (see connector_client_binding_orm.go), so a field added here is a field on
// every ConnectorClientBinding value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ConnectorClientBindingExtra struct {
}

// Connector client binding queries (P5b lane L5): which connector an OAuth
// client acts through.

// GetLiveConnectorClientBinding returns the connector a client was authorized
// to use, or orm.ErrNoRows. Only a live grant resolves: a binding to a
// revoked or expired connector is absent.
func GetLiveConnectorClientBinding(ctx context.Context, db orm.Context, userID, clientID string) (*ConnectorClientBinding, error) {
	var row ConnectorClientBinding
	err := db.Bun().NewSelect().Model(&row).
		Join("JOIN connector_grants AS g ON g.id = ?", bun.Ident("connector_client_bindings.grant_id")).
		Where("? = ?", bun.Ident("connector_client_bindings.user_id"), userID).
		Where("? = ?", bun.Ident("connector_client_bindings.client_id"), clientID).
		Where("g.revoked_at IS NULL").
		Where("(g.expires_at IS NULL OR g.expires_at > now())").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// PutConnectorClientBinding records the user's choice for a client: a
// re-consent MOVES the client to another connector (ON CONFLICT on the
// client), never adds a second authorization.
func PutConnectorClientBinding(ctx context.Context, db orm.Context, b *ConnectorClientBinding) error {
	_, err := db.Bun().NewInsert().Model(b).
		Column(ConnectorClientBindingFieldID, ConnectorClientBindingFieldUserID, ConnectorClientBindingFieldClientID,
			ConnectorClientBindingFieldGrantID, ConnectorClientBindingFieldClientName).
		On("CONFLICT (user_id, client_id) DO UPDATE").
		Set("grant_id = EXCLUDED.grant_id").
		Set("client_name = EXCLUDED.client_name").
		Set("updated_at = now()").
		Exec(ctx)
	return err
}

// UnbindConnectorClient removes a client's authorization and reports
// whether there was one. It leaves the grant alone: one connector may serve
// several clients.
func UnbindConnectorClient(ctx context.Context, db orm.Context, userID, clientID string) (bool, error) {
	res, err := db.Bun().NewDelete().Model((*ConnectorClientBinding)(nil)).
		Where("? = ?", bun.Ident(ConnectorClientBindingFieldUserID), userID).
		Where("? = ?", bun.Ident(ConnectorClientBindingFieldClientID), clientID).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
