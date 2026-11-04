// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ThreadProviderAccount. The generated thread_provider_account_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: threadProviderAccountRepo (a *crud.Repo[ThreadProviderAccount]) and the
//     package-level GetThreadProviderAccountByPK / ListThreadProviderAccount / CreateThreadProviderAccount / UpsertThreadProviderAccount, keyed by
//     the whole composite key (thread_id, provider), plus UpdateThreadProviderAccount / DeleteThreadProviderAccountByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ThreadProviderAccount struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindThreadProviderAccountBy(ctx context.Context, db orm.Context, /* args */) ([]*ThreadProviderAccount, error) {
//		var rows []*ThreadProviderAccount
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("account_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveThreadProviderAccounts(ctx context.Context, db orm.Context) ([]*ThreadProviderAccount, error) {
//		return ListThreadProviderAccount(ctx, db, orm.WhereEq("account_id", true))
//	}
package store

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Thread assignments (P5b lane L6): a thread's sticky provider account, one per provider.

func assignmentToCore(r *ThreadProviderAccount) core.Assignment {
	return core.Assignment{ThreadID: r.ThreadID, Provider: r.Provider, AccountID: r.AccountID, AssignedAt: r.AssignedAt, Reason: r.Reason}
}

// ClearOtherThreadAssignments removes pins belonging to other providers for
// a user's thread, before assignment on the newly selected chat provider.
// Account limits are not touched; the old account may still serve other chats.
func ClearOtherThreadAssignments(ctx context.Context, db orm.Context, userID, threadID, provider string) error {
	_, err := db.Bun().NewRaw(`DELETE FROM thread_provider_accounts a
        USING threads t, chats c
        WHERE a.thread_id = ?0 AND a.provider <> ?1
          AND t.id = a.thread_id AND c.id = t.chat_id AND c.user_id = ?2`, threadID, provider, userID).Exec(ctx)
	return err
}

// GetThreadAssignment returns the thread's account for provider, or (nil, nil) when it has none.
func GetThreadAssignment(ctx context.Context, db orm.Context, threadID, provider string) (*core.Assignment, error) {
	row, err := GetThreadProviderAccountByPK(ctx, db, threadID, provider)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a := assignmentToCore(row)
	return &a, nil
}

// ListThreadAssignments returns the thread's account for every provider it has used, most
// recently assigned first.
func ListThreadAssignments(ctx context.Context, db orm.Context, threadID string) ([]core.Assignment, error) {
	rows, err := ListThreadProviderAccount(ctx, db,
		orm.WhereEq(ThreadProviderAccountFieldThreadID, threadID),
		orm.WithOrderBy(ThreadProviderAccountFieldAssignedAt, orm.Desc),
		orm.WithOrderBy(ThreadProviderAccountFieldProvider, orm.Asc))
	if err != nil {
		return nil, err
	}
	out := make([]core.Assignment, 0, len(rows))
	for _, r := range rows {
		out = append(out, assignmentToCore(r))
	}
	return out, nil
}

// InsertThreadAssignment assigns a's account to the thread for a's provider only if the thread
// has none for it yet, and reports whether it did.
func InsertThreadAssignment(ctx context.Context, db orm.Context, a core.Assignment) (bool, error) {
	res, err := db.Bun().NewInsert().Model(&ThreadProviderAccount{
		ThreadID: a.ThreadID, Provider: a.Provider, AccountID: a.AccountID, AssignedAt: a.AssignedAt.UTC(), Reason: a.Reason,
	}).On("CONFLICT (thread_id, provider) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReassignThread moves the thread's assignment for a's provider to a's account only while it is
// still expectedAccountID, and reports whether it did.
func ReassignThread(ctx context.Context, db orm.Context, a core.Assignment, expectedAccountID string) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*ThreadProviderAccount)(nil)).
		Set("account_id = ?", a.AccountID).Set("assigned_at = ?", a.AssignedAt.UTC()).Set("reason = ?", a.Reason).
		Where("thread_id = ?", a.ThreadID).Where("provider = ?", a.Provider).Where("account_id = ?", expectedAccountID).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecentAssignmentCounts counts the threads assigned to each of accountIDs since since. An
// account with none is absent from the map.
func RecentAssignmentCounts(ctx context.Context, db orm.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	out := map[string]int{}
	if len(accountIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		AccountID string `bun:"account_id"`
		N         int    `bun:"n"`
	}
	err := db.Bun().NewSelect().Model((*ThreadProviderAccount)(nil)).
		Column(ThreadProviderAccountFieldAccountID).ColumnExpr("count(*) AS n").
		Where("account_id = ANY(?::text[])", pgdialect.Array(accountIDs)).Where("assigned_at >= ?", since.UTC()).
		Group(ThreadProviderAccountFieldAccountID).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.AccountID] = r.N
	}
	return out, nil
}

// ThreadProviderAccountExtra carries fields on ThreadProviderAccount that are NOT columns — computed
// values, transient state, anything derived rather than stored. ThreadProviderAccount is
// embedded on ThreadProviderAccountExtra (see thread_provider_account_orm.go), so a field added here is a field on
// every ThreadProviderAccount value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ThreadProviderAccountExtra struct {
}
