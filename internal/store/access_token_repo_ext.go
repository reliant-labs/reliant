// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for AccessToken. The generated access_token_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: accessTokenRepo (a *crud.Repo[AccessToken]) and the
//     package-level GetAccessTokenByID / ListAccessToken / CreateAccessToken / UpdateAccessToken / DeleteAccessToken
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     AccessToken struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindAccessTokenBy(ctx context.Context, db orm.Context, /* args */) ([]*AccessToken, error) {
//		var rows []*AccessToken
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveAccessTokens(ctx context.Context, db orm.Context) ([]*AccessToken, error) {
//		return ListAccessToken(ctx, db, orm.WhereEq("org_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// access_tokens (P5b lane L6): the self-hosted token store's statements
// (internal/tokenauthority's LocalStore, which owns every rule: minting, hashing, scope sets,
// the per-user cap). Only a token's hash is stored. Revocation, expiry and the last-used
// throttle are evaluated on the database clock, as every release has.

// AccessTokenRecord is one access_tokens row as the token store reads it. "" is NULL for the
// nullable text columns.
type AccessTokenRecord struct {
	ID, OrgID, Name, TokenHash, TokenPrefix string
	Scopes                                  []string
	ActingUserID, ResourceKind, ResourceID  string
	Ephemeral                               bool
	CreatedAt                               time.Time
	ExpiresAt, LastUsedAt                   *time.Time
}

func accessTokenRecord(r *AccessToken) AccessTokenRecord {
	return AccessTokenRecord{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, TokenHash: r.TokenHash, TokenPrefix: r.TokenPrefix, Scopes: r.Scopes,
		ActingUserID: acctText(r.ActingUserID), ResourceKind: acctText(r.ResourceKind), ResourceID: acctText(r.ResourceID),
		Ephemeral: r.Ephemeral, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt,
	}
}

// accessTokenInfoColumns is what a listing or an update returns.
var accessTokenInfoColumns = []string{
	AccessTokenFieldID, AccessTokenFieldName, AccessTokenFieldTokenPrefix, AccessTokenFieldScopes,
	AccessTokenFieldResourceKind, AccessTokenFieldResourceID, AccessTokenFieldEphemeral,
	AccessTokenFieldCreatedAt, AccessTokenFieldExpiresAt, AccessTokenFieldLastUsedAt,
}

// liveAccessTokens keeps the tokens that are not revoked and, on the database clock, not expired.
func liveAccessTokens(q *bun.SelectQuery) *bun.SelectQuery {
	return q.Where("revoked_at IS NULL").Where("(expires_at IS NULL OR expires_at > now())")
}

// FindLiveAccessToken returns the live token with that hash, or found=false (unknown, revoked or
// expired alike).
func FindLiveAccessToken(ctx context.Context, db orm.Context, tokenHash string) (rec AccessTokenRecord, found bool, err error) {
	var row AccessToken
	err = liveAccessTokens(db.Bun().NewSelect().Model(&row).
		Column(AccessTokenFieldID, AccessTokenFieldOrgID, AccessTokenFieldTokenHash, AccessTokenFieldScopes,
			AccessTokenFieldActingUserID, AccessTokenFieldResourceKind, AccessTokenFieldResourceID,
			AccessTokenFieldEphemeral, AccessTokenFieldExpiresAt).
		Where("token_hash = ?", tokenHash)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessTokenRecord{}, false, nil
	}
	if err != nil {
		return AccessTokenRecord{}, false, err
	}
	return accessTokenRecord(&row), true, nil
}

// TouchAccessToken stamps the token's last use, at most once a minute.
func TouchAccessToken(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().NewUpdate().Model((*AccessToken)(nil)).Set("last_used_at = now()").
		Where("id = ?", id).Where("(last_used_at IS NULL OR last_used_at < now() - interval '1 minute')").Exec(ctx)
	return err
}

// LockUserAccessTokens serializes the user's token rotations until the transaction ctx carries
// ends. Every release takes this key, so its text must never change.
func LockUserAccessTokens(ctx context.Context, db orm.Context, userID string) error {
	_, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock(hashtext('accesstoken:user:' || ?0))`, userID).Exec(ctx)
	return err
}

// RevokeUserAccessTokensNamed revokes the user's live tokens of that name and exactly that scope
// set, and returns how many.
func RevokeUserAccessTokensNamed(ctx context.Context, db orm.Context, userID, name string, scopes []string) (int64, error) {
	return revokeAccessTokens(ctx, db.Bun().NewUpdate().Model((*AccessToken)(nil)).
		Where("acting_user_id = ?", userID).Where("name = ?", name).Where("scopes = ?", pgdialect.Array(scopes)))
}

// CountLiveUserAccessTokens counts the user's live tokens with exactly that scope set.
func CountLiveUserAccessTokens(ctx context.Context, db orm.Context, userID string, scopes []string) (int64, error) {
	return liveAccessTokens(db.Bun().NewSelect().Model((*AccessToken)(nil)).
		Where("acting_user_id = ?", userID).Where("scopes = ?", pgdialect.Array(scopes))).Count(ctx)
}

// NewAccessToken is a token to store: its hash and display prefix, never the token.
type NewAccessToken struct {
	ID, OrgID, Name, TokenHash, TokenPrefix string
	Scopes                                  []string
	// ActingUserID is also recorded as the creator; "" for an org automation token.
	ActingUserID             string
	ResourceKind, ResourceID string // "" for an unbound token
	Ephemeral                bool
	ExpiresAt                *time.Time
}

// InsertAccessToken stores t, created now on the database clock.
func InsertAccessToken(ctx context.Context, db orm.Context, t NewAccessToken) error {
	acting := acctNullText(t.ActingUserID)
	var kind, id *string
	if t.ResourceKind != "" {
		kind, id = &t.ResourceKind, &t.ResourceID
	}
	_, err := db.Bun().NewInsert().Model(&AccessToken{
		ID: t.ID, OrgID: t.OrgID, Name: t.Name, TokenHash: t.TokenHash, TokenPrefix: t.TokenPrefix,
		Scopes: orm.EmptyIfNil(t.Scopes), CreatedByUserID: acting, ActingUserID: acting,
		ResourceKind: kind, ResourceID: id, Ephemeral: t.Ephemeral, ExpiresAt: t.ExpiresAt,
	}).Exec(ctx)
	return err
}

// ListUserAccessTokens returns the user's unrevoked tokens, newest first; only those carrying
// scope when scope is not "".
func ListUserAccessTokens(ctx context.Context, db orm.Context, userID, scope string) ([]AccessTokenRecord, error) {
	var rows []*AccessToken
	q := db.Bun().NewSelect().Model(&rows).Column(accessTokenInfoColumns...).
		Where("acting_user_id = ?", userID).Where("revoked_at IS NULL")
	if scope != "" {
		q = q.Where("? = ANY(scopes)", scope)
	}
	if err := q.OrderExpr("created_at DESC").Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]AccessTokenRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, accessTokenRecord(r))
	}
	return out, nil
}

// UpdateUserAccessToken renames and/or re-scopes one of the user's unrevoked tokens (a nil name
// or scopes keeps it) and returns it; found=false when the user has no such unrevoked token.
// The hash is never touched: the credential keeps working with its new authority.
func UpdateUserAccessToken(ctx context.Context, db orm.Context, userID, tokenID string, name *string, scopes []string) (rec AccessTokenRecord, found bool, err error) {
	var row AccessToken
	q := db.Bun().NewUpdate().Model(&row)
	if name != nil {
		q = q.Set("name = ?", *name)
	}
	if scopes != nil {
		q = q.Set("scopes = ?", pgdialect.Array(scopes))
	}
	err = q.Where("id = ?", tokenID).Where("acting_user_id = ?", userID).Where("revoked_at IS NULL").
		Returning(strings.Join(accessTokenInfoColumns, ", ")).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessTokenRecord{}, false, nil
	}
	if err != nil {
		return AccessTokenRecord{}, false, err
	}
	return accessTokenRecord(&row), true, nil
}

// RevokeUserAccessToken revokes one of the user's tokens and reports how many rows it revoked
// (0 when absent, not the user's, or already revoked).
func RevokeUserAccessToken(ctx context.Context, db orm.Context, userID, tokenID string) (int64, error) {
	return revokeAccessTokens(ctx, db.Bun().NewUpdate().Model((*AccessToken)(nil)).
		Where("id = ?", tokenID).Where("acting_user_id = ?", userID))
}

// UserAccessTokenExists reports whether the user has a token with that id, revoked or not.
func UserAccessTokenExists(ctx context.Context, db orm.Context, userID, tokenID string) (bool, error) {
	return db.Bun().NewSelect().Model((*AccessToken)(nil)).
		Where("id = ?", tokenID).Where("acting_user_id = ?", userID).Exists(ctx)
}

// RevokeResourceAccessTokens revokes every unrevoked token bound to the resource.
func RevokeResourceAccessTokens(ctx context.Context, db orm.Context, kind, id string) (int64, error) {
	return revokeAccessTokens(ctx, db.Bun().NewUpdate().Model((*AccessToken)(nil)).
		Where("resource_kind = ?", kind).Where("resource_id = ?", id))
}

// RevokeEphemeralAccessTokens revokes the user's unrevoked ephemeral tokens.
func RevokeEphemeralAccessTokens(ctx context.Context, db orm.Context, userID string) (int64, error) {
	return revokeAccessTokens(ctx, db.Bun().NewUpdate().Model((*AccessToken)(nil)).
		Where("acting_user_id = ?", userID).Where("ephemeral"))
}

// revokeAccessTokens stamps revoked_at, on the database clock, on the unrevoked rows q selects.
func revokeAccessTokens(ctx context.Context, q *bun.UpdateQuery) (int64, error) {
	res, err := q.Set("revoked_at = now()").Where("revoked_at IS NULL").Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AccessTokenExtra carries fields on AccessToken that are NOT columns — computed
// values, transient state, anything derived rather than stored. AccessToken is
// embedded on AccessTokenExtra (see access_token_orm.go), so a field added here is a field on
// every AccessToken value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type AccessTokenExtra struct {
}
