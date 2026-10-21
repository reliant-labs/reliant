// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ConnectionEvent. The generated connection_event_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: connectionEventRepo (a *crud.Repo[ConnectionEvent]) and the
//     package-level GetConnectionEventByID / ListConnectionEvent / CreateConnectionEvent / UpdateConnectionEvent / DeleteConnectionEvent
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ConnectionEvent struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindConnectionEventBy(ctx context.Context, db orm.Context, /* args */) ([]*ConnectionEvent, error) {
//		var rows []*ConnectionEvent
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("connection_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveConnectionEvents(ctx context.Context, db orm.Context) ([]*ConnectionEvent, error) {
//		return ListConnectionEvent(ctx, db, orm.WhereEq("connection_id", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A connection's append-only audit trail (P5b lane L5).

// AppendConnectionEvent records ev; its id and time are the database's.
func AppendConnectionEvent(ctx context.Context, db orm.Context, ev core.ConnectionEvent) error {
	return CreateConnectionEvent(ctx, db, &ConnectionEvent{
		ConnectionID: ev.ConnectionID, UserID: ev.UserID, Kind: ev.Kind,
		RunID: emptyToNil(ev.RunID), NodeID: emptyToNil(ev.NodeID), ToolCallID: emptyToNil(ev.ToolCallID),
		Actor: ev.Actor,
	})
}

// ListConnectionEvents pages the user's events of one connection, newest
// first: limit of them (100 when unset or above 500), older than beforeID
// when it is set.
func ListConnectionEvents(ctx context.Context, db orm.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	opts := []orm.QueryOption{
		orm.WhereEq(ConnectionEventFieldConnectionID, id),
		orm.WhereEq(ConnectionEventFieldUserID, userID),
		orm.WithOrderBy(ConnectionEventFieldID, orm.Desc),
		orm.WithLimit(limit),
	}
	if beforeID != 0 {
		opts = append(opts, orm.WhereLt(ConnectionEventFieldID, beforeID))
	}
	rows, err := ListConnectionEvent(ctx, db, opts...)
	if err != nil {
		return nil, err
	}
	out := make([]core.ConnectionEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, core.ConnectionEvent{
			ID: row.ID, ConnectionID: row.ConnectionID, UserID: row.UserID, Kind: row.Kind,
			RunID: deref(row.RunID), NodeID: deref(row.NodeID), ToolCallID: deref(row.ToolCallID),
			Actor: row.Actor, At: row.At,
		})
	}
	return out, nil
}

// ConnectionEventExtra carries fields on ConnectionEvent that are NOT columns — computed
// values, transient state, anything derived rather than stored. ConnectionEvent is
// embedded on ConnectionEventExtra (see connection_event_orm.go), so a field added here is a field on
// every ConnectionEvent value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ConnectionEventExtra struct {
}
