// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ThreadFunding. The generated thread_funding_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: threadFundingRepo (a *crud.Repo[ThreadFunding]) and the
//     package-level GetThreadFundingByID / ListThreadFunding / CreateThreadFunding / UpdateThreadFunding / DeleteThreadFunding
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ThreadFunding struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindThreadFundingBy(ctx context.Context, db orm.Context, /* args */) ([]*ThreadFunding, error) {
//		var rows []*ThreadFunding
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("org_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveThreadFundings(ctx context.Context, db orm.Context) ([]*ThreadFunding, error) {
//		return ListThreadFunding(ctx, db, orm.WhereEq("org_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// GetThreadFunding returns who paid for threadID's calls on its current account (funding class,
// the run's org, the account), or found false when nothing is recorded.
func GetThreadFunding(ctx context.Context, db orm.Context, threadID string) (funding, orgID, accountID string, found bool, err error) {
	var row ThreadFunding
	err = db.Bun().NewSelect().Model(&row).Where("thread_id = ?", threadID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, fmt.Errorf("read who pays for the thread: %w", err)
	}
	return row.Funding, row.OrgID, row.AccountID, true, nil
}

// PutThreadFunding records that threadID's calls are now paid by funding, on accountID in orgID's
// run. It writes only when the account or the funding changed, so a call on the same account is a
// no-op statement.
func PutThreadFunding(ctx context.Context, db orm.Context, threadID, orgID, funding, accountID string, now time.Time) error {
	_, err := db.Bun().NewRaw(`INSERT INTO thread_funding (thread_id, org_id, funding, account_id, updated_at)
		VALUES (?0, ?1, ?2, ?3, ?4)
		ON CONFLICT (thread_id) DO UPDATE SET org_id = EXCLUDED.org_id, funding = EXCLUDED.funding,
		    account_id = EXCLUDED.account_id, updated_at = EXCLUDED.updated_at
		WHERE thread_funding.account_id <> EXCLUDED.account_id OR thread_funding.funding <> EXCLUDED.funding
		   OR thread_funding.org_id <> EXCLUDED.org_id`,
		threadID, orgID, funding, accountID, now.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record who pays for the thread: %w", err)
	}
	return nil
}

// ThreadFundingExtra carries fields on ThreadFunding that are NOT columns — computed
// values, transient state, anything derived rather than stored. ThreadFunding is
// embedded on ThreadFundingExtra (see thread_funding_orm.go), so a field added here is a field on
// every ThreadFunding value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ThreadFundingExtra struct {
}
