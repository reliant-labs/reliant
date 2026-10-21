// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for LegacyCredentialCopy. The generated legacy_credential_copy_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: legacyCredentialCopyRepo (a *crud.Repo[LegacyCredentialCopy]) and the
//     package-level GetLegacyCredentialCopyByID / ListLegacyCredentialCopy / CreateLegacyCredentialCopy / UpdateLegacyCredentialCopy / DeleteLegacyCredentialCopy
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     LegacyCredentialCopy struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindLegacyCredentialCopyBy(ctx context.Context, db orm.Context, /* args */) ([]*LegacyCredentialCopy, error) {
//		var rows []*LegacyCredentialCopy
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("completed_at = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveLegacyCredentialCopies(ctx context.Context, db orm.Context) ([]*LegacyCredentialCopy, error) {
//		return ListLegacyCredentialCopy(ctx, db, orm.WhereEq("completed_at", true))
//	}
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// The legacy credential copy's completion marker (P5b lane L6): one row, id 1, written in the
// transaction that copied the legacy credential tables into provider_accounts.

// legacyCopyLockName keys the global advisory lock the copy runs under. Every release that
// copies takes it, so its text must never change.
const legacyCopyLockName = "provider_accounts_legacy_copy"

// LegacyCredentialCopyDone reports whether the one-time copy has completed.
func LegacyCredentialCopyDone(ctx context.Context, db orm.Context) (bool, error) {
	n, err := CountLegacyCredentialCopy(ctx, db)
	return n > 0, err
}

// LockLegacyCredentialCopy serializes the copy across processes until the transaction ctx
// carries ends.
func LockLegacyCredentialCopy(ctx context.Context, db orm.Context) error {
	if _, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock(hashtext(?0))`, legacyCopyLockName).Exec(ctx); err != nil {
		return fmt.Errorf("lock legacy credential copy: %w", err)
	}
	return nil
}

// RecordLegacyCredentialCopy writes the completion marker.
//
// The builder, not CreateLegacyCredentialCopy: forge projects the plain `integer PRIMARY KEY`
// id as autoincrement, so Create leaves the id out and the insert fails NOT NULL.
func RecordLegacyCredentialCopy(ctx context.Context, db orm.Context, completedAt time.Time, created, skipped, failed int) error {
	_, err := db.Bun().NewInsert().Model(&LegacyCredentialCopy{
		ID: 1, CompletedAt: completedAt.UTC(), Created: int32(created), Skipped: int32(skipped), Failed: int32(failed),
	}).Exec(ctx)
	return err
}

// InLegacyImportSavepoint runs fn under a savepoint of the transaction ctx carries. An error
// from fn rolls back to the savepoint, leaving the transaction usable, and is returned as
// rowErr; err is a savepoint statement failing, which leaves the transaction unusable.
func InLegacyImportSavepoint(ctx context.Context, db orm.Context, fn func() error) (rowErr, err error) {
	if _, err := db.Exec(ctx, `SAVEPOINT legacy_import`); err != nil {
		return nil, err
	}
	if rowErr := fn(); rowErr != nil {
		if _, err := db.Exec(ctx, `ROLLBACK TO SAVEPOINT legacy_import`); err != nil {
			return rowErr, err
		}
		return rowErr, nil
	}
	if _, err := db.Exec(ctx, `RELEASE SAVEPOINT legacy_import`); err != nil {
		return nil, err
	}
	return nil, nil
}

// LegacyCredentialCopyExtra carries fields on LegacyCredentialCopy that are NOT columns — computed
// values, transient state, anything derived rather than stored. LegacyCredentialCopy is
// embedded on LegacyCredentialCopyExtra (see legacy_credential_copy_orm.go), so a field added here is a field on
// every LegacyCredentialCopy value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type LegacyCredentialCopyExtra struct {
}
