// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for IntegrationEventAccess. The generated integration_event_access_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: integrationEventAccessRepo (a *crud.Repo[IntegrationEventAccess]) and the
//     package-level GetIntegrationEventAccessByPK / ListIntegrationEventAccess / CreateIntegrationEventAccess / UpsertIntegrationEventAccess, keyed by
//     the whole composite key (user_id, integration_id, account_key, resource_key), plus UpdateIntegrationEventAccess / DeleteIntegrationEventAccessByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     IntegrationEventAccess struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindIntegrationEventAccessBy(ctx context.Context, db orm.Context, /* args */) ([]*IntegrationEventAccess, error) {
//		var rows []*IntegrationEventAccess
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("resource_label = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveIntegrationEventAccesses(ctx context.Context, db orm.Context) ([]*IntegrationEventAccess, error) {
//		return ListIntegrationEventAccess(ctx, db, orm.WhereEq("resource_label", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Access-gated routing for app-level provider events (P5b lane L5). The model
// is in the migration 20261005061008_integration_event_access.sql: an event
// about a resource (a GitHub repository) reaches a trigger only when its
// owner's own provider credential recently reported that resource as visible.

// upsertIntegrationAccessSQL records one refresh's findings: every (account,
// resource) the user's own credential can see now, all stamped with the same
// refreshed_at. Three parallel arrays are zipped by position (one unnest
// each, joined on ordinality).
const upsertIntegrationAccessSQL = `
INSERT INTO integration_event_access (
    user_id, integration_id, account_key, resource_key, resource_label, subject_id, refreshed_at
)
SELECT ?0::text, ?1::text, a.v, r.v, l.v, ?2::text, ?3::timestamptz
FROM unnest(?4::text[]) WITH ORDINALITY AS a(v, i)
JOIN unnest(?5::text[]) WITH ORDINALITY AS r(v, i) ON r.i = a.i
JOIN unnest(?6::text[]) WITH ORDINALITY AS l(v, i) ON l.i = a.i
ON CONFLICT (user_id, integration_id, account_key, resource_key) DO UPDATE SET
    resource_label = EXCLUDED.resource_label,
    subject_id = EXCLUDED.subject_id,
    refreshed_at = EXCLUDED.refreshed_at
-- Monotonic: a slower refresh that started earlier never stamps an older
-- time over a newer refresh's.
WHERE integration_event_access.refreshed_at <= EXCLUDED.refreshed_at`

// ReplaceIntegrationAccess records a refresh: the user's grants for the
// integration become exactly grants, all stamped at. It is one transaction,
// so routing never sees a half-replaced set — in particular never the moment
// between dropping a lost repository and writing the rest.
func ReplaceIntegrationAccess(ctx context.Context, db orm.Context, userID, integrationID, subjectID string, at time.Time, grants []core.IntegrationAccessGrant) error {
	if userID == "" || integrationID == "" {
		return errors.New("integration access: user and integration are required")
	}
	accounts := make([]string, 0, len(grants))
	resources := make([]string, 0, len(grants))
	labels := make([]string, 0, len(grants))
	for _, g := range grants {
		if g.AccountKey == "" || g.ResourceKey == "" {
			return fmt.Errorf("integration access: a grant needs an account and a resource, got %+v", g)
		}
		accounts = append(accounts, g.AccountKey)
		resources = append(resources, g.ResourceKey)
		labels = append(labels, g.ResourceLabel)
	}
	at = at.UTC()
	return db.RunTx(ctx, func(ctx context.Context) error {
		if len(grants) > 0 {
			if _, err := db.Bun().NewRaw(upsertIntegrationAccessSQL,
				userID, integrationID, subjectID, at, orm.Arg(pgdialect.Array(accounts)), orm.Arg(pgdialect.Array(resources)), orm.Arg(pgdialect.Array(labels))).Exec(ctx); err != nil {
				return fmt.Errorf("record integration access: %w", err)
			}
		}
		// Drop what the user could see before but this refresh no longer
		// reports: a repository they lost, an installation they left. Also
		// the whole set when a refresh finds the credential gone.
		if _, err := db.Bun().NewDelete().Model((*IntegrationEventAccess)(nil)).
			Where("? = ?", bun.Ident(IntegrationEventAccessFieldUserID), userID).
			Where("? = ?", bun.Ident(IntegrationEventAccessFieldIntegrationID), integrationID).
			Where("? < ?", bun.Ident(IntegrationEventAccessFieldRefreshedAt), at).
			Exec(ctx); err != nil {
			return fmt.Errorf("drop lost integration access: %w", err)
		}
		return nil
	})
}

// RevokeIntegrationAccess deletes the grants a provider revocation names, for
// every user, and returns how many it removed. Empty fields are wildcards,
// but the revocation must name an account or a subject: an installation
// removed (account), a repository removed from it (account + resource), a
// member removed (subject, optionally narrowed to an account).
func RevokeIntegrationAccess(ctx context.Context, db orm.Context, integrationID string, rev core.IntegrationAccessRevocation) (int64, error) {
	if integrationID == "" || (rev.AccountKey == "" && rev.SubjectID == "") {
		return 0, fmt.Errorf("integration access: a revocation must name an account or a subject, got %+v", rev)
	}
	q := db.Bun().NewDelete().Model((*IntegrationEventAccess)(nil)).
		Where("? = ?", bun.Ident(IntegrationEventAccessFieldIntegrationID), integrationID)
	if rev.AccountKey != "" {
		q = q.Where("? = ?", bun.Ident(IntegrationEventAccessFieldAccountKey), rev.AccountKey)
	}
	if rev.ResourceKey != "" {
		q = q.Where("? = ?", bun.Ident(IntegrationEventAccessFieldResourceKey), rev.ResourceKey)
	}
	if rev.SubjectID != "" {
		q = q.Where("? = ?", bun.Ident(IntegrationEventAccessFieldSubjectID), rev.SubjectID)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneIntegrationAccess drops the grants nobody has re-confirmed since
// before, and returns how many: they no longer route (routing requires a
// recent refresh) and their owner has no trigger left to refresh them for.
func PruneIntegrationAccess(ctx context.Context, db orm.Context, before time.Time) (int64, error) {
	res, err := db.Bun().NewDelete().Model((*IntegrationEventAccess)(nil)).
		Where("? < ?", bun.Ident(IntegrationEventAccessFieldRefreshedAt), before.UTC()).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IntegrationEventAccessExtra carries fields on IntegrationEventAccess that are NOT columns — computed
// values, transient state, anything derived rather than stored. IntegrationEventAccess is
// embedded on IntegrationEventAccessExtra (see integration_event_access_orm.go), so a field added here is a field on
// every IntegrationEventAccess value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type IntegrationEventAccessExtra struct {
}
