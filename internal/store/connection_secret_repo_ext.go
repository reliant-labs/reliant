// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ConnectionSecret. The generated connection_secret_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: connectionSecretRepo (a *crud.Repo[ConnectionSecret]) and the
//     package-level GetConnectionSecretByPK / ListConnectionSecret / CreateConnectionSecret / UpsertConnectionSecret, keyed by
//     the whole composite key (connection_id, field), plus UpdateConnectionSecret / DeleteConnectionSecretByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ConnectionSecret struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindConnectionSecretBy(ctx context.Context, db orm.Context, /* args */) ([]*ConnectionSecret, error) {
//		var rows []*ConnectionSecret
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("vault_key_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveConnectionSecrets(ctx context.Context, db orm.Context) ([]*ConnectionSecret, error) {
//		return ListConnectionSecret(ctx, db, orm.WhereEq("vault_key_id", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A connection's sealed fields (P5b lane L5). The ciphertext arrives sealed
// by internal/connections and leaves sealed: nothing here sees a plaintext.
// Every read is scoped to a connection the caller has already proven it owns
// (or proves it in the same statement).

// upsertConnectionSecret writes one sealed field: a new field starts at
// generation 1, a rewrite bumps it, which is what Replace's
// compare-and-swap reads.
func upsertConnectionSecret(ctx context.Context, db orm.Context, s core.ConnectionSecret) error {
	_, err := db.Bun().NewInsert().Model(&ConnectionSecret{
		ConnectionID: s.ConnectionID, Field: s.Field, VaultKeyID: s.VaultKeyID, Ciphertext: s.Ciphertext,
	}).
		On("CONFLICT (connection_id, field) DO UPDATE").
		Set("vault_key_id = EXCLUDED.vault_key_id").
		Set("ciphertext = EXCLUDED.ciphertext").
		Set("generation = connection_secrets.generation + 1").
		Set("updated_at = now()").
		Exec(ctx)
	return err
}

// readConnectionSecrets returns a connection's sealed fields by name, locking
// them FOR UPDATE when forUpdate.
func readConnectionSecrets(ctx context.Context, db orm.Context, connectionID string, forUpdate bool) (map[string]core.ConnectionSecret, error) {
	var rows []*ConnectionSecret
	q := db.Bun().NewSelect().Model(&rows).
		Where("? = ?", bun.Ident(ConnectionSecretFieldConnectionID), connectionID).
		OrderExpr("?", bun.Ident(ConnectionSecretFieldField))
	if forUpdate {
		q = q.For("UPDATE")
	}
	if err := q.Scan(ctx); err != nil {
		return nil, err
	}
	return connectionSecretsToCore(rows), nil
}

// GetConnectionSecrets returns the sealed fields of the user's live
// connection, or core.ErrConnectionNotFound. Ownership is checked in the same
// statement that reads the ciphertext, so a caller that forgot to Get first
// still cannot read another user's.
func GetConnectionSecrets(ctx context.Context, db orm.Context, userID, id string) (map[string]core.ConnectionSecret, error) {
	var rows []*ConnectionSecret
	if err := db.Bun().NewSelect().Model(&rows).
		Join("JOIN connections AS c ON c.id = ?", bun.Ident("connection_secrets.connection_id")).
		Where("? = ?", bun.Ident("connection_secrets.connection_id"), id).
		Where("c.user_id = ?", userID).
		Where("c.owner_kind = ?", core.ConnectionOwnerUser).
		Where("c.deleted_at IS NULL").
		OrderExpr("?", bun.Ident("connection_secrets.field")).
		Scan(ctx); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, core.ErrConnectionNotFound
	}
	return connectionSecretsToCore(rows), nil
}

// WithConnectionSecretsLock runs fn with the user's live connection row and
// its secret rows locked (SELECT … FOR UPDATE) inside one transaction,
// serializing token refreshes across worker processes. The connection row is
// locked too: a connection with no secret rows still needs a lock target. fn
// receives the ctx carrying that transaction; the writes it makes through
// ReplaceConnectionSecrets, MarkConnectionStatus and AppendConnectionEvent
// join it only when given that ctx.
func WithConnectionSecretsLock(ctx context.Context, db orm.Context, userID, id string,
	fn func(txCtx context.Context, conn *core.Connection, secrets map[string]core.ConnectionSecret) error,
) error {
	return connectionTx(ctx, db, func(txCtx context.Context) error {
		conn, err := selectConnection(txCtx, db, true, whereConnection(ConnectionFieldID, id), whereUserOwned(userID))
		if err != nil {
			return err
		}
		secrets, err := readConnectionSecrets(txCtx, db, id, true)
		if err != nil {
			return err
		}
		return fn(txCtx, conn, secrets)
	})
}

// ReplaceConnectionSecrets rewrites the given sealed fields of a connection
// (bumping their generations) and its access expiry. The rewrite outlives a
// cancelled caller: a refreshed token the provider has already issued must
// not be lost.
func ReplaceConnectionSecrets(ctx context.Context, db orm.Context, connectionID string, secrets []core.ConnectionSecret, accessExpiresAt *time.Time) error {
	for _, sec := range secrets {
		sec.ConnectionID = connectionID
		if err := upsertConnectionSecret(context.WithoutCancel(ctx), db, sec); err != nil {
			return err
		}
	}
	_, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
		Set("? = ?", bun.Ident(ConnectionFieldAccessExpiresAt), accessExpiry(accessExpiresAt)).
		Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
		Where("? = ?", bun.Ident(ConnectionFieldID), connectionID).
		Exec(ctx)
	return err
}

// MarkConnectionStatus sets a connection's status and its reason (empty
// clears it).
func MarkConnectionStatus(ctx context.Context, db orm.Context, connectionID, status, reason string) error {
	_, err := db.Bun().NewUpdate().Model((*Connection)(nil)).
		Set("? = ?", bun.Ident(ConnectionFieldStatus), status).
		Set("? = ?", bun.Ident(ConnectionFieldStatusReason), emptyToNil(reason)).
		Set("? = now()", bun.Ident(ConnectionFieldUpdatedAt)).
		Where("? = ?", bun.Ident(ConnectionFieldID), connectionID).
		Exec(ctx)
	return err
}

func connectionSecretsToCore(rows []*ConnectionSecret) map[string]core.ConnectionSecret {
	out := make(map[string]core.ConnectionSecret, len(rows))
	for _, row := range rows {
		sec := core.ConnectionSecret{
			ConnectionID: row.ConnectionID, Field: row.Field, VaultKeyID: row.VaultKeyID,
			Ciphertext: row.Ciphertext, UpdatedAt: row.UpdatedAt,
		}
		if row.Generation != nil {
			sec.Generation = *row.Generation
		}
		out[sec.Field] = sec
	}
	return out
}

// ConnectionSecretExtra carries fields on ConnectionSecret that are NOT columns — computed
// values, transient state, anything derived rather than stored. ConnectionSecret is
// embedded on ConnectionSecretExtra (see connection_secret_orm.go), so a field added here is a field on
// every ConnectionSecret value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ConnectionSecretExtra struct {
}
