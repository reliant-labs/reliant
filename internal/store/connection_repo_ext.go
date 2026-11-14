// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Connection. The generated connection_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: connectionRepo (a *crud.Repo[Connection]) and the
//     package-level GetConnectionByID / ListConnection / CreateConnection / UpdateConnection / DeleteConnection
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Connection struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindConnectionBy(ctx context.Context, db orm.Context, /* args */) ([]*Connection, error) {
//		var rows []*Connection
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("owner_kind = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveConnections(ctx context.Context, db orm.Context) ([]*Connection, error) {
//		return ListConnection(ctx, db, orm.WhereEq("owner_kind", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Connections — the credential vault's metadata rows (P5b lane L5). The
// ciphertext lives in connection_secrets (connection_secret_repo_ext.go), so
// no query here can return it; the audit trail is connection_events
// (connection_event_repo_ext.go). Every read and write that takes an owner
// (a core.ConnectionOwner, or a userID meaning that user's personal ones)
// enforces ownership in SQL, owner_kind included: an org's connection is never
// read or changed as a person's, even by the admin whose user_id it carries
// for attribution. connections.deleted_at is bun's soft-delete column, so
// every model query below already excludes deleted rows.
//
// The writes run in connectionTx: one READ COMMITTED transaction (joining
// one ctx already carries) that is never retried. Their consistency comes
// from row locks and the advisory lock, not from snapshot isolation — under
// the ORM's SERIALIZABLE default two workers refreshing one token would fail
// one with a serialization error instead of letting it read the winner's
// generation. And WithConnectionSecretsLock hands its callback a locked row
// while it refreshes a token over the network, so a retry would refresh
// twice.

func connectionTx(ctx context.Context, db orm.Context, fn func(ctx context.Context) error) error {
	return db.RunTx(ctx, orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}, fn)
}

// InsertUserConnection inserts c, its secrets and its created event in one
// transaction. The owner's (c.Owner(): the user, or the org of an org
// connection) first connection for an integration becomes its default;
// concurrent first-creates for the same (owner, integration) are serialized
// so the partial unique index is never what decides.
func InsertUserConnection(ctx context.Context, db orm.Context, c *core.Connection, secrets []core.ConnectionSecret, ev core.ConnectionEvent) error {
	owner := c.Owner()
	return connectionTx(ctx, db, func(ctx context.Context) error {
		// A personal key keeps the spelling it always had, so a replica of
		// the previous release serializes with this one on the same lock.
		lockKey := fmt.Sprintf("connections:%d:%s:%s", len(c.UserID), c.UserID, c.IntegrationID)
		if owner.Kind == core.ConnectionOwnerOrg {
			lockKey = fmt.Sprintf("connections:org:%d:%s:%s", len(owner.ID), owner.ID, c.IntegrationID)
		}
		if _, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock(hashtextextended(?0, 0))`, lockKey).Exec(ctx); err != nil {
			return err
		}
		pred, args := ownerPredicate(owner)
		defaults, err := db.Bun().NewSelect().Model((*Connection)(nil)).
			Where(pred, args...).
			Where("? = ?", bun.Ident(ConnectionFieldIntegrationID), c.IntegrationID).
			Where("?", bun.Ident(ConnectionFieldIsDefault)).
			Count(ctx)
		if err != nil {
			return err
		}
		c.IsDefault = defaults == 0
		row, err := connectionFromCore(c)
		if err != nil {
			return err
		}
		if err := CreateConnection(ctx, db, row); err != nil {
			if isConnectionNameTaken(err) {
				return core.ErrConnectionNameTaken
			}
			return err
		}
		c.CreatedAt, c.UpdatedAt = row.CreatedAt, row.UpdatedAt
		for _, sec := range secrets {
			sec.ConnectionID = c.ID
			if err := upsertConnectionSecret(ctx, db, sec); err != nil {
				return err
			}
		}
		ev.ConnectionID = c.ID
		return AppendConnectionEvent(ctx, db, ev)
	})
}

// ReauthorizeConnection rewrites the identity and secrets of the user's live
// personal connection, returns it to active, and returns the row as stored.
func ReauthorizeConnection(ctx context.Context, db orm.Context, userID, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error) {
	return ReauthorizeOwnedConnection(ctx, db, core.UserConnectionOwner(userID), id, upd, secrets, ev)
}

// ReauthorizeOwnedConnection is ReauthorizeConnection for owner's live
// connection.
func ReauthorizeOwnedConnection(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error) {
	pred, args := ownerPredicate(owner)
	var out *core.Connection
	err := connectionTx(ctx, db, func(ctx context.Context) error {
		scopes := upd.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		res, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
			Set("? = ?", bun.Ident(ConnectionFieldAccountLabel), emptyToNil(upd.AccountLabel)).
			Set("? = ?", bun.Ident(ConnectionFieldExternalAccountID), emptyToNil(upd.ExternalAccountID)).
			Set("? = ?", bun.Ident(ConnectionFieldScopes), pgdialect.Array(scopes)).
			Set("? = ?", bun.Ident(ConnectionFieldOauthClient), emptyToNil(upd.OAuthClient)).
			Set("? = ?", bun.Ident(ConnectionFieldAccessExpiresAt), accessExpiry(upd.AccessExpiresAt)).
			Set("? = ?", bun.Ident(ConnectionFieldSenderID), emptyToNil(upd.SenderID)).
			Set("? = ?", bun.Ident(ConnectionFieldStatus), core.ConnectionStatusActive).
			Set("? = NULL", bun.Ident(ConnectionFieldStatusReason)).
			Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
			Where("? = ?", bun.Ident(ConnectionFieldID), id).
			Where(pred, args...).
			Exec(ctx)
		if err := requireConnectionRow(res, err); err != nil {
			return err
		}
		for _, sec := range secrets {
			sec.ConnectionID = id
			if err := upsertConnectionSecret(ctx, db, sec); err != nil {
				return err
			}
		}
		ev.ConnectionID = id
		if err := AppendConnectionEvent(ctx, db, ev); err != nil {
			return err
		}
		out, err = selectConnection(ctx, db, false, whereConnection(ConnectionFieldID, id), whereOwned(owner))
		return err
	})
	return out, err
}

// GetUserConnection returns the user's live personal connection, or
// core.ErrConnectionNotFound (missing, deleted, someone else's, or an org's).
func GetUserConnection(ctx context.Context, db orm.Context, userID, id string) (*core.Connection, error) {
	return GetOwnedConnection(ctx, db, core.UserConnectionOwner(userID), id)
}

// GetOwnedConnection returns owner's live connection, or
// core.ErrConnectionNotFound (missing, deleted, or not owner's).
func GetOwnedConnection(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id string) (*core.Connection, error) {
	return selectConnection(ctx, db, false, whereConnection(ConnectionFieldID, id), whereOwned(owner))
}

// ListUserConnections returns the user's live personal connections, of one
// integration when integrationID is set, ordered by integration then age.
func ListUserConnections(ctx context.Context, db orm.Context, userID, integrationID string) ([]*core.Connection, error) {
	return ListOwnedConnections(ctx, db, core.UserConnectionOwner(userID), integrationID)
}

// ListOwnedConnections returns owner's live connections, of one integration
// when integrationID is set, ordered by integration then age.
func ListOwnedConnections(ctx context.Context, db orm.Context, owner core.ConnectionOwner, integrationID string) ([]*core.Connection, error) {
	var rows []*Connection
	q := db.Bun().NewSelect().Model(&rows)
	whereOwned(owner)(q)
	if integrationID != "" {
		whereConnection(ConnectionFieldIntegrationID, integrationID)(q)
	}
	if err := q.OrderExpr("?, ?, ?", bun.Ident(ConnectionFieldIntegrationID), bun.Ident(ConnectionFieldCreatedAt), bun.Ident(ConnectionFieldID)).
		Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]*core.Connection, 0, len(rows))
	for _, row := range rows {
		c, err := connectionToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// DefaultUserConnection returns the user's default connection for the
// integration, or core.ErrConnectionNotFound.
func DefaultUserConnection(ctx context.Context, db orm.Context, userID, integrationID string) (*core.Connection, error) {
	return selectConnection(ctx, db, false, whereUserOwned(userID),
		whereConnection(ConnectionFieldIntegrationID, integrationID), whereConnection(ConnectionFieldIsDefault, true))
}

// FindConnectionByExternalAccount returns the user's oldest live personal
// connection to that provider account, or core.ErrConnectionNotFound.
func FindConnectionByExternalAccount(ctx context.Context, db orm.Context, userID, integrationID, externalAccountID string) (*core.Connection, error) {
	return FindOwnedConnectionByExternalAccount(ctx, db, core.UserConnectionOwner(userID), integrationID, externalAccountID)
}

// FindOwnedConnectionByExternalAccount returns owner's oldest live
// connection to that provider account, or core.ErrConnectionNotFound.
func FindOwnedConnectionByExternalAccount(ctx context.Context, db orm.Context, owner core.ConnectionOwner, integrationID, externalAccountID string) (*core.Connection, error) {
	return selectConnection(ctx, db, false, whereOwned(owner),
		whereConnection(ConnectionFieldIntegrationID, integrationID),
		whereConnection(ConnectionFieldExternalAccountID, externalAccountID),
		func(q *bun.SelectQuery) { q.OrderExpr("?", bun.Ident(ConnectionFieldCreatedAt)) })
}

// RenameConnection renames the user's live personal connection and records ev.
func RenameConnection(ctx context.Context, db orm.Context, userID, id, name string, ev core.ConnectionEvent) error {
	return RenameOwnedConnection(ctx, db, core.UserConnectionOwner(userID), id, name, ev)
}

// RenameOwnedConnection renames owner's live connection and records ev.
func RenameOwnedConnection(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id, name string, ev core.ConnectionEvent) error {
	pred, args := ownerPredicate(owner)
	return connectionTx(ctx, db, func(ctx context.Context) error {
		res, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
			Set("? = ?", bun.Ident(ConnectionFieldName), name).
			Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
			Where("? = ?", bun.Ident(ConnectionFieldID), id).
			Where(pred, args...).
			Exec(ctx)
		if isConnectionNameTaken(err) {
			return core.ErrConnectionNameTaken
		}
		if err := requireConnectionRow(res, err); err != nil {
			return err
		}
		ev.ConnectionID = id
		return AppendConnectionEvent(ctx, db, ev)
	})
}

// SetDefaultConnection makes the user's live personal connection its
// integration's default and records ev.
func SetDefaultConnection(ctx context.Context, db orm.Context, userID, id string, ev core.ConnectionEvent) error {
	return SetDefaultOwnedConnection(ctx, db, core.UserConnectionOwner(userID), id, ev)
}

// SetDefaultOwnedConnection makes owner's live connection its integration's
// default among owner's connections and records ev.
func SetDefaultOwnedConnection(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id string, ev core.ConnectionEvent) error {
	pred, args := ownerPredicate(owner)
	return connectionTx(ctx, db, func(ctx context.Context) error {
		c, err := selectConnection(ctx, db, true, whereConnection(ConnectionFieldID, id), whereOwned(owner))
		if err != nil {
			return err
		}
		// Clear first: the partial unique index allows only one default.
		if _, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
			Set("? = false", bun.Ident(ConnectionFieldIsDefault)).
			Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
			Where(pred, args...).
			Where("? = ?", bun.Ident(ConnectionFieldIntegrationID), c.IntegrationID).
			Where("?", bun.Ident(ConnectionFieldIsDefault)).
			Where("? <> ?", bun.Ident(ConnectionFieldID), id).
			Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
			Set("? = true", bun.Ident(ConnectionFieldIsDefault)).
			Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
			Where("? = ?", bun.Ident(ConnectionFieldID), id).
			Where(pred, args...).
			Exec(ctx); err != nil {
			return err
		}
		ev.ConnectionID = id
		return AppendConnectionEvent(ctx, db, ev)
	})
}

// SoftDeleteConnection deletes the user's live personal connection: the row
// is kept, revoked, for the audit trail; its secrets are destroyed.
func SoftDeleteConnection(ctx context.Context, db orm.Context, userID, id string, ev core.ConnectionEvent) error {
	return SoftDeleteOwnedConnection(ctx, db, core.UserConnectionOwner(userID), id, ev)
}

// SoftDeleteOwnedConnection is SoftDeleteConnection for owner's live
// connection.
func SoftDeleteOwnedConnection(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id string, ev core.ConnectionEvent) error {
	pred, args := ownerPredicate(owner)
	return connectionTx(ctx, db, func(ctx context.Context) error {
		res, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
			Set("? = now()", bun.Ident(ConnectionFieldDeletedAt)).
			Set("? = false", bun.Ident(ConnectionFieldIsDefault)).
			Set("? = ?", bun.Ident(ConnectionFieldStatus), core.ConnectionStatusRevoked).
			Set("? = 'deleted'", bun.Ident(ConnectionFieldStatusReason)).
			Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
			Where("? = ?", bun.Ident(ConnectionFieldID), id).
			Where(pred, args...).
			Exec(ctx)
		if err := requireConnectionRow(res, err); err != nil {
			return err
		}
		if _, err := db.Bun().NewDelete().Model((*ConnectionSecret)(nil)).
			Where("? = ?", bun.Ident(ConnectionSecretFieldConnectionID), id).
			Exec(ctx); err != nil {
			return err
		}
		ev.ConnectionID = id
		return AppendConnectionEvent(ctx, db, ev)
	})
}

// RecordConnectionTestResult stores what a successful probe of the user's
// personal connection read; an empty value keeps the stored one.
func RecordConnectionTestResult(ctx context.Context, db orm.Context, userID, id, accountLabel, senderID string) error {
	return RecordOwnedConnectionTestResult(ctx, db, core.UserConnectionOwner(userID), id, accountLabel, senderID)
}

// RecordOwnedConnectionTestResult is RecordConnectionTestResult for owner's
// connection.
func RecordOwnedConnectionTestResult(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id, accountLabel, senderID string) error {
	pred, args := ownerPredicate(owner)
	res, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
		Set("? = COALESCE(?, ?)", bun.Ident(ConnectionFieldAccountLabel), emptyToNil(accountLabel), bun.Ident(ConnectionFieldAccountLabel)).
		Set("? = COALESCE(?, ?)", bun.Ident(ConnectionFieldSenderID), emptyToNil(senderID), bun.Ident(ConnectionFieldSenderID)).
		Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
		Where("? = ?", bun.Ident(ConnectionFieldID), id).
		Where(pred, args...).
		Exec(ctx)
	return requireConnectionRow(res, err)
}

// TouchConnectionUsed stamps the user's personal connection's last use. It
// is telemetry: updated_at is left alone.
func TouchConnectionUsed(ctx context.Context, db orm.Context, userID, id string) error {
	return TouchOwnedConnectionUsed(ctx, db, core.UserConnectionOwner(userID), id)
}

// TouchOwnedConnectionUsed is TouchConnectionUsed for owner's connection.
func TouchOwnedConnectionUsed(ctx context.Context, db orm.Context, owner core.ConnectionOwner, id string) error {
	pred, args := ownerPredicate(owner)
	_, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
		Set("? = now()", bun.Ident(ConnectionFieldLastUsedAt)).
		Where("? = ?", bun.Ident(ConnectionFieldID), id).
		Where(pred, args...).
		Exec(ctx)
	return err
}

// connectionFilter narrows a select over connections.
type connectionFilter func(*bun.SelectQuery)

func whereConnection(column string, value any) connectionFilter {
	return func(q *bun.SelectQuery) { q.Where("? = ?", bun.Ident(column), value) }
}

// whereUserOwned is a connection the user owns personally (not an org's).
func whereUserOwned(userID string) connectionFilter {
	return whereOwned(core.UserConnectionOwner(userID))
}

// whereOwned is a connection owner owns.
func whereOwned(owner core.ConnectionOwner) connectionFilter {
	pred, args := ownerPredicate(owner)
	return func(q *bun.SelectQuery) { q.Where(pred, args...) }
}

// ownerPredicate is the SQL that scopes a connections query to owner: a
// person's own rows (user_id, owner_kind 'user'), or an org's (org_id,
// owner_kind 'org'). An owner with no id or an unknown kind matches nothing.
func ownerPredicate(owner core.ConnectionOwner) (string, []any) {
	if owner.ID == "" {
		return "FALSE", nil
	}
	switch owner.Kind {
	case core.ConnectionOwnerUser:
		return "? = ? AND ? = ?", []any{
			bun.Ident(ConnectionFieldUserID), owner.ID, bun.Ident(ConnectionFieldOwnerKind), core.ConnectionOwnerUser,
		}
	case core.ConnectionOwnerOrg:
		return "? = ? AND ? = ?", []any{
			bun.Ident(ConnectionFieldOrgID), owner.ID, bun.Ident(ConnectionFieldOwnerKind), core.ConnectionOwnerOrg,
		}
	}
	return "FALSE", nil
}

// isConnectionNameTaken reports whether err is a write that lost to a live
// connection of the same name: a person's (connections_user_name) or an
// org's (connections_org_name).
func isConnectionNameTaken(err error) bool {
	switch orm.ConstraintName(err) {
	case ConnectionConstraintUserName, ConnectionConstraintOrgName:
		return true
	}
	return false
}

// selectConnection reads the one live connection the filters name (the
// first by their order), locking it FOR UPDATE when forUpdate, or returns
// core.ErrConnectionNotFound.
func selectConnection(ctx context.Context, db orm.Context, forUpdate bool, filters ...connectionFilter) (*core.Connection, error) {
	var row Connection
	q := db.Bun().NewSelect().Model(&row)
	for _, f := range filters {
		f(q)
	}
	if forUpdate {
		q = q.For("UPDATE")
	}
	if err := q.Limit(1).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrConnectionNotFound
		}
		return nil, err
	}
	return connectionToCore(&row)
}

// requireConnectionRow turns a write that matched no row into
// core.ErrConnectionNotFound.
func requireConnectionRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrConnectionNotFound
	}
	return nil
}

func connectionFromCore(c *core.Connection) (*Connection, error) {
	params, err := encodeConnectionParams(c.Params)
	if err != nil {
		return nil, err
	}
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return &Connection{
		ID: c.ID, OwnerKind: c.OwnerKind, UserID: c.UserID, OrgID: c.OrgID, IntegrationID: c.IntegrationID,
		AuthKind: c.AuthKind, Name: c.Name, AccountLabel: c.AccountLabel, ExternalAccountID: c.ExternalAccountID,
		Scopes: scopes, OauthClient: c.OAuthClient, AuthHeader: c.AuthHeader, Status: c.Status,
		StatusReason: c.StatusReason, IsDefault: c.IsDefault, AccessExpiresAt: c.AccessExpiresAt,
		Params: params, SenderID: c.SenderID,
	}, nil
}

func connectionToCore(row *Connection) (*core.Connection, error) {
	params, err := decodeConnectionParams(row.Params)
	if err != nil {
		return nil, fmt.Errorf("connection %s params: %w", row.ID, err)
	}
	scopes := row.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return &core.Connection{
		ID: row.ID, OwnerKind: row.OwnerKind, UserID: row.UserID, OrgID: row.OrgID, IntegrationID: row.IntegrationID,
		AuthKind: row.AuthKind, Name: row.Name, AccountLabel: row.AccountLabel, ExternalAccountID: row.ExternalAccountID,
		SenderID: row.SenderID, Scopes: scopes, OAuthClient: row.OauthClient, AuthHeader: row.AuthHeader,
		Params: params, Status: row.Status, StatusReason: row.StatusReason, IsDefault: row.IsDefault,
		AccessExpiresAt: row.AccessExpiresAt, LastUsedAt: row.LastUsedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}, nil
}

// encodeConnectionParams stores a param map as jsonb; nil is the empty
// object.
func encodeConnectionParams(m map[string]string) (json.RawMessage, error) {
	if m == nil {
		m = map[string]string{}
	}
	return json.Marshal(m)
}

func decodeConnectionParams(raw json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// accessExpiry is the bound value of a nullable timestamp: NULL for nil.
func accessExpiry(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// ConnectionExtra carries fields on Connection that are NOT columns — computed
// values, transient state, anything derived rather than stored. Connection is
// embedded on ConnectionExtra (see connection_orm.go), so a field added here is a field on
// every Connection value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ConnectionExtra struct {
}
