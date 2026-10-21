// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ApiKey. The generated api_key_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: apiKeyRepo (a *crud.Repo[ApiKey]) and the
//     package-level GetApiKeyByID / ListApiKey / CreateApiKey / UpdateApiKey / DeleteApiKey
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ApiKey struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindApiKeyBy(ctx context.Context, db orm.Context, /* args */) ([]*ApiKey, error) {
//		var rows []*ApiKey
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveApiKeys(ctx context.Context, db orm.Context) ([]*ApiKey, error) {
//		return ListApiKey(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// api_keys (P5b lane L6). The table holds only automation credentials now, one per (user,
// provider), and only sealed: api_key is the retired plaintext column, kept empty until a later
// migration drops it. Sealing and opening are internal/db/postgres's settingStore's; nothing
// here sees a credential except the backfill of rows that predate sealing.

// GetAPIKeySealed returns the sealed value of the user's key for provider; sql.ErrNoRows when
// there is no such key. The value is nil for a row that was never sealed.
func GetAPIKeySealed(ctx context.Context, db orm.Context, userID, provider string) ([]byte, error) {
	var row ApiKey
	err := db.Bun().NewSelect().Model(&row).Column(ApiKeyFieldAPIKeySealed).
		Where("user_id = ?", userID).Where("provider = ?", provider).Scan(ctx)
	return row.APIKeySealed, err
}

// UpsertSealedAPIKey writes the user's key for provider, sealed, replacing an existing one in
// place (its id and created_at kept). The plaintext column is written empty.
func UpsertSealedAPIKey(ctx context.Context, db orm.Context, id, userID, provider string, sealed []byte, now time.Time) error {
	_, err := db.Bun().NewInsert().Model(&ApiKey{
		ID: id, UserID: userID, Provider: provider, APIKey: "", APIKeySealed: sealed, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}).On("CONFLICT (user_id, provider) DO UPDATE").
		Set("api_key = ''").
		Set("api_key_sealed = EXCLUDED.api_key_sealed").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return err
}

// DeleteAPIKey deletes the user's key for provider. A missing one is not an error.
func DeleteAPIKey(ctx context.Context, db orm.Context, userID, provider string) error {
	_, err := db.Bun().NewDelete().Model((*ApiKey)(nil)).
		Where("user_id = ?", userID).Where("provider = ?", provider).Exec(ctx)
	return err
}

// CountUnsealedAPIKeys counts the rows with no sealed value.
func CountUnsealedAPIKeys(ctx context.Context, db orm.Context) (int64, error) {
	return CountApiKey(ctx, db, orm.WhereIsNull(ApiKeyFieldAPIKeySealed))
}

// ValidateAPIKeysSealedConstraint validates the NOT VALID check the contract migration added; it
// fails while any row is unsealed.
func ValidateAPIKeysSealedConstraint(ctx context.Context, db orm.Context) error {
	_, err := db.Exec(ctx, `ALTER TABLE api_keys VALIDATE CONSTRAINT api_keys_sealed_not_null`)
	return err
}

// UnsealedAPIKey is a row that predates sealing, with its plaintext.
type UnsealedAPIKey struct {
	ID, UserID, Provider, APIKey string
}

// ListUnsealedAPIKeys returns up to limit unsealed rows, by id.
func ListUnsealedAPIKeys(ctx context.Context, db orm.Context, limit int) ([]UnsealedAPIKey, error) {
	rows, err := ListApiKey(ctx, db, orm.WhereIsNull(ApiKeyFieldAPIKeySealed),
		orm.WithOrderBy(ApiKeyFieldID, orm.Asc), orm.WithLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]UnsealedAPIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, UnsealedAPIKey{ID: r.ID, UserID: r.UserID, Provider: r.Provider, APIKey: r.APIKey})
	}
	return out, nil
}

// SealUnsealedAPIKey stores sealed as the row's value only while the row is still unsealed and
// still holds plaintext, so a concurrent write is never overwritten with stale data. It reports
// whether it wrote.
func SealUnsealedAPIKey(ctx context.Context, db orm.Context, id, plaintext string, sealed []byte) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*ApiKey)(nil)).Set("api_key_sealed = ?", sealed).
		Where("id = ?", id).Where("api_key_sealed IS NULL").Where("api_key = ?", plaintext).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ApiKeyExtra carries fields on ApiKey that are NOT columns — computed
// values, transient state, anything derived rather than stored. ApiKey is
// embedded on ApiKeyExtra (see api_key_orm.go), so a field added here is a field on
// every ApiKey value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ApiKeyExtra struct {
}
