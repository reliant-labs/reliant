// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for VaultKey. The generated vault_key_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: vaultKeyRepo (a *crud.Repo[VaultKey]) and the
//     package-level GetVaultKeyByID / ListVaultKey / CreateVaultKey / UpdateVaultKey / DeleteVaultKey
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     VaultKey struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindVaultKeyBy(ctx context.Context, db orm.Context, /* args */) ([]*VaultKey, error) {
//		var rows []*VaultKey
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("tenant_kind = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveVaultKeys(ctx context.Context, db orm.Context) ([]*VaultKey, error) {
//		return ListVaultKey(ctx, db, orm.WhereEq("tenant_kind", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
)

// The credential vault's wrapped data keys (P5b lane L6). internal/vault owns every rule
// (wrapping, caching, tenant binding); these are its statements. The vault runs each one
// outside any transaction its caller carries, so a key row commits on its own.

// WrappedVaultKey is one vault_keys row as the vault reads it: whose key it is, its state, and
// the data key still wrapped under the KEK.
type WrappedVaultKey struct {
	ID, TenantKind, TenantID, State string
	WrappedDEK                      []byte
}

// GetTenantVaultKey returns the tenant's key in state (the vault's primary), or found=false.
func GetTenantVaultKey(ctx context.Context, db orm.Context, tenantKind, tenantID, state string) (key WrappedVaultKey, found bool, err error) {
	rows, err := ListVaultKey(ctx, db,
		orm.WhereEq(VaultKeyFieldTenantKind, tenantKind), orm.WhereEq(VaultKeyFieldTenantID, tenantID),
		orm.WhereEq(VaultKeyFieldState, state), orm.WithLimit(1))
	if err != nil || len(rows) == 0 {
		return WrappedVaultKey{}, false, err
	}
	return wrappedVaultKey(rows[0]), true, nil
}

// GetWrappedVaultKey returns the key with that id, or found=false.
func GetWrappedVaultKey(ctx context.Context, db orm.Context, id string) (key WrappedVaultKey, found bool, err error) {
	row, err := GetVaultKeyByID(ctx, db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return WrappedVaultKey{}, false, nil
	}
	if err != nil {
		return WrappedVaultKey{}, false, err
	}
	return wrappedVaultKey(row), true, nil
}

func wrappedVaultKey(r *VaultKey) WrappedVaultKey {
	return WrappedVaultKey{ID: r.ID, TenantKind: r.TenantKind, TenantID: r.TenantID, State: r.State, WrappedDEK: r.WrappedDek}
}

// InsertVaultKeyIfAbsent stores a tenant's first data key, version 1, unless a conflicting key
// (another first use of the same tenant) is already stored: concurrent first uses converge on
// one row, and the loser re-reads the winner's.
func InsertVaultKeyIfAbsent(ctx context.Context, db orm.Context, key WrappedVaultKey, kekID string) error {
	_, err := db.Bun().NewInsert().Model(&VaultKey{
		ID: key.ID, TenantKind: key.TenantKind, TenantID: key.TenantID, Version: 1,
		KekID: kekID, WrappedDek: key.WrappedDEK, State: key.State,
	}).On("CONFLICT DO NOTHING").Exec(ctx)
	return err
}

// ListLiveVaultKeys returns every data key that is not destroyed, by id.
func ListLiveVaultKeys(ctx context.Context, db orm.Context) ([]*VaultKey, error) {
	var rows []*VaultKey
	err := db.Bun().NewSelect().Model(&rows).Where("state <> ?", "destroyed").OrderExpr("id").Scan(ctx)
	return rows, err
}

// ListLiveVaultKeysForUpdate is ListLiveVaultKeys with the rows locked FOR UPDATE, so two
// rewraps of the same key take turns.
func ListLiveVaultKeysForUpdate(ctx context.Context, db orm.Context) ([]*VaultKey, error) {
	var rows []*VaultKey
	err := db.Bun().NewSelect().Model(&rows).Where("state <> ?", "destroyed").OrderExpr("id").For("UPDATE").Scan(ctx)
	return rows, err
}

// RewrapVaultKey replaces one data key's wrapping: the same DEK, wrapped under kekID.
func RewrapVaultKey(ctx context.Context, db orm.Context, id, kekID string, wrapped []byte) error {
	res, err := db.Bun().NewUpdate().Model((*VaultKey)(nil)).
		Set("kek_id = ?", kekID).
		Set("wrapped_dek = ?", wrapped).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("rewrap vault key %s: %d rows updated, want 1", id, n)
	}
	return nil
}

// SealedCredentialsExist reports whether anything was ever sealed: a vault key, or an api_keys
// row with a sealed value. A key-encryption key generated now could open none of it.
func SealedCredentialsExist(ctx context.Context, db orm.Context) (bool, error) {
	var exists bool
	err := db.Bun().NewRaw(`SELECT EXISTS (SELECT 1 FROM vault_keys) OR EXISTS (SELECT 1 FROM api_keys WHERE api_key_sealed IS NOT NULL)`).
		Scan(ctx, &exists)
	return exists, err
}

// VaultKeyExtra carries fields on VaultKey that are NOT columns — computed
// values, transient state, anything derived rather than stored. VaultKey is
// embedded on VaultKeyExtra (see vault_key_orm.go), so a field added here is a field on
// every VaultKey value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type VaultKeyExtra struct {
}
