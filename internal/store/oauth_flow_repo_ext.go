// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for OauthFlow. The generated oauth_flow_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: oauthFlowRepo (a *crud.Repo[OauthFlow]) and the
//     package-level GetOauthFlowByID / ListOauthFlow / CreateOauthFlow / UpdateOauthFlow / DeleteOauthFlow
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     OauthFlow struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindOauthFlowBy(ctx context.Context, db orm.Context, /* args */) ([]*OauthFlow, error) {
//		var rows []*OauthFlow
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveOauthFlows(ctx context.Context, db orm.Context) ([]*OauthFlow, error) {
//		return ListOauthFlow(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// OAuth authorization state: single-use and session-bound, one row per
// authorization in flight (P5b lane L5, with the connection store it
// served).

// InsertOAuthFlow records f.
func InsertOAuthFlow(ctx context.Context, db orm.Context, f *core.OAuthFlow) error {
	params, err := encodeConnectionParams(f.Params)
	if err != nil {
		return err
	}
	return CreateOauthFlow(ctx, db, &OauthFlow{
		StateHash: f.StateHash, UserID: f.UserID, SessionIDHash: f.SessionIDHash, IntegrationID: f.IntegrationID,
		PkceVerifierSealed: f.PKCEVerifierSealed, RedirectAfter: emptyToNil(f.RedirectAfter),
		ReconnectConnectionID: emptyToNil(f.ReconnectConnectionID), ConnectionName: emptyToNil(f.ConnectionName),
		ExpiresAt: f.ExpiresAt, Params: params, ReturnTo: emptyToNil(f.ReturnTo),
	})
}

// PeekOAuthFlow returns an unexpired, unconsumed flow without consuming it;
// anything else is core.ErrOAuthFlowInvalid.
func PeekOAuthFlow(ctx context.Context, db orm.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	var row OauthFlow
	err := db.Bun().NewSelect().Model(&row).
		Where("? = ?", bun.Ident(OauthFlowFieldStateHash), stateHash).
		Where("? IS NULL", bun.Ident(OauthFlowFieldConsumedAt)).
		Where("? > ?", bun.Ident(OauthFlowFieldExpiresAt), now).
		Scan(ctx)
	return oauthFlowToCore(&row, err)
}

// ConsumeOAuthFlow marks an unexpired, unconsumed flow consumed and returns
// it; anything else is core.ErrOAuthFlowInvalid. It is one statement: the
// compare-and-swap on consumed_at is what makes the state single-use even
// under concurrent replays.
func ConsumeOAuthFlow(ctx context.Context, db orm.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	var row OauthFlow
	err := db.Bun().NewRaw(`
		UPDATE oauth_flows SET consumed_at = ?1
		 WHERE state_hash = ?0 AND consumed_at IS NULL AND expires_at > ?1
		RETURNING *`, stateHash, now).
		Scan(ctx, &row)
	return oauthFlowToCore(&row, err)
}

func oauthFlowToCore(row *OauthFlow, err error) (*core.OAuthFlow, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrOAuthFlowInvalid
	}
	if err != nil {
		return nil, err
	}
	params, err := decodeConnectionParams(row.Params)
	if err != nil {
		return nil, fmt.Errorf("oauth flow params: %w", err)
	}
	return &core.OAuthFlow{
		StateHash: row.StateHash, UserID: row.UserID, SessionIDHash: row.SessionIDHash, IntegrationID: row.IntegrationID,
		PKCEVerifierSealed: row.PkceVerifierSealed, RedirectAfter: deref(row.RedirectAfter),
		ReconnectConnectionID: deref(row.ReconnectConnectionID), ConnectionName: deref(row.ConnectionName),
		Params: params, ReturnTo: deref(row.ReturnTo), ExpiresAt: row.ExpiresAt,
	}, nil
}

// OauthFlowExtra carries fields on OauthFlow that are NOT columns — computed
// values, transient state, anything derived rather than stored. OauthFlow is
// embedded on OauthFlowExtra (see oauth_flow_orm.go), so a field added here is a field on
// every OauthFlow value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type OauthFlowExtra struct {
}
