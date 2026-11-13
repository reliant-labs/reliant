// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatPersonalFunding. The generated chat_personal_funding_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatPersonalFundingRepo (a *crud.Repo[ChatPersonalFunding]) and the
//     package-level GetChatPersonalFundingByID / ListChatPersonalFunding / CreateChatPersonalFunding / UpdateChatPersonalFunding / DeleteChatPersonalFunding
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ChatPersonalFunding struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindChatPersonalFundingBy(ctx context.Context, db orm.Context, /* args */) ([]*ChatPersonalFunding, error) {
//		var rows []*ChatPersonalFunding
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChatPersonalFundings(ctx context.Context, db orm.Context) ([]*ChatPersonalFunding, error) {
//		return ListChatPersonalFunding(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ChatPersonalFundingExtra carries fields on ChatPersonalFunding that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatPersonalFunding is
// embedded on ChatPersonalFundingExtra (see chat_personal_funding_orm.go), so a field added here is a field on
// every ChatPersonalFunding value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatPersonalFundingExtra struct {
}

func personalFundingToCore(r *ChatPersonalFunding) *core.PersonalFunding {
	if r == nil {
		return nil
	}
	return &core.PersonalFunding{
		ChatID: r.ChatID, UserID: r.UserID, OrgID: r.OrgID, State: r.State, Reason: r.Reason, Model: r.Model,
		PersonalProvider: r.PersonalProvider, PersonalAccountLabel: r.PersonalAccountLabel,
		OrgResetAt: acctUTC(r.OrgResetAt), LeaseUntil: acctUTC(r.LeaseUntil),
		AskedAt: r.AskedAt.UTC(), AnsweredAt: acctUTC(r.AnsweredAt), Announced: r.Announced, UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// GetChatPersonalFunding returns the chat's question, or (nil, nil) when it has none.
func GetChatPersonalFunding(ctx context.Context, db orm.Context, chatID string) (*core.PersonalFunding, error) {
	var row ChatPersonalFunding
	err := db.Bun().NewSelect().Model(&row).Where("chat_id = ?", chatID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the chat's personal funding question: %w", err)
	}
	return personalFundingToCore(&row), nil
}

// LockChatPersonalFunding is GetChatPersonalFunding holding the row's lock until the caller's
// transaction ends.
func LockChatPersonalFunding(ctx context.Context, db orm.Context, chatID string) (*core.PersonalFunding, error) {
	var row ChatPersonalFunding
	err := db.Bun().NewSelect().Model(&row).Where("chat_id = ?", chatID).For("UPDATE").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock the chat's personal funding question: %w", err)
	}
	return personalFundingToCore(&row), nil
}

// LockOwnChatPersonalFunding is LockChatPersonalFunding for the question userID owns: another
// person's question reads as none.
func LockOwnChatPersonalFunding(ctx context.Context, db orm.Context, chatID, userID string) (*core.PersonalFunding, error) {
	var row ChatPersonalFunding
	err := db.Bun().NewSelect().Model(&row).Where("chat_id = ? AND user_id = ?", chatID, userID).For("UPDATE").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock the chat's personal funding question: %w", err)
	}
	return personalFundingToCore(&row), nil
}

// PutChatPersonalFunding writes the chat's row whole, inserting or replacing it.
func PutChatPersonalFunding(ctx context.Context, db orm.Context, q core.PersonalFunding) error {
	_, err := db.Bun().NewRaw(`INSERT INTO chat_personal_funding (chat_id, user_id, org_id, state, reason, model,
		personal_provider, personal_account_label, org_reset_at, lease_until, asked_at, answered_at, updated_at, announced)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13)
		ON CONFLICT (chat_id) DO UPDATE SET user_id = EXCLUDED.user_id, org_id = EXCLUDED.org_id, state = EXCLUDED.state,
		    reason = EXCLUDED.reason, model = EXCLUDED.model, personal_provider = EXCLUDED.personal_provider,
		    personal_account_label = EXCLUDED.personal_account_label, org_reset_at = EXCLUDED.org_reset_at,
		    lease_until = EXCLUDED.lease_until, asked_at = EXCLUDED.asked_at, answered_at = EXCLUDED.answered_at,
		    updated_at = EXCLUDED.updated_at, announced = EXCLUDED.announced`,
		q.ChatID, q.UserID, q.OrgID, q.State, q.Reason, q.Model, q.PersonalProvider, q.PersonalAccountLabel,
		acctUTC(q.OrgResetAt), acctUTC(q.LeaseUntil), q.AskedAt.UTC(), acctUTC(q.AnsweredAt), q.UpdatedAt.UTC(), q.Announced).Exec(ctx)
	if err != nil {
		return fmt.Errorf("write the chat's personal funding question: %w", err)
	}
	return nil
}

// InsertChatPersonalFunding writes q unless the chat already has a row; reports whether it wrote.
func InsertChatPersonalFunding(ctx context.Context, db orm.Context, q core.PersonalFunding) (bool, error) {
	res, err := db.Bun().NewRaw(`INSERT INTO chat_personal_funding (chat_id, user_id, org_id, state, reason, model,
		personal_provider, personal_account_label, org_reset_at, lease_until, asked_at, answered_at, updated_at, announced)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13)
		ON CONFLICT (chat_id) DO NOTHING`,
		q.ChatID, q.UserID, q.OrgID, q.State, q.Reason, q.Model, q.PersonalProvider, q.PersonalAccountLabel,
		acctUTC(q.OrgResetAt), acctUTC(q.LeaseUntil), q.AskedAt.UTC(), acctUTC(q.AnsweredAt), q.UpdatedAt.UTC(), q.Announced).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("ask the chat's personal funding question: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkChatPersonalFundingAnnounced records that the chat's transcript said who serves under its
// use_personal answer; reports whether this call was the one that marked it.
func MarkChatPersonalFundingAnnounced(ctx context.Context, db orm.Context, chatID string) (bool, error) {
	res, err := db.Bun().NewRaw(`UPDATE chat_personal_funding SET announced = true WHERE chat_id = ? AND NOT announced
		AND state = 'use_personal'`, chatID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("mark the chat's personal funding announced: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteChatPersonalFundingRow removes the chat's row; reports whether there was one.
func DeleteChatPersonalFundingRow(ctx context.Context, db orm.Context, chatID string) (bool, error) {
	res, err := db.Bun().NewRaw(`DELETE FROM chat_personal_funding WHERE chat_id = ?`, chatID).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("clear the chat's personal funding question: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ProviderAccountOrgs is which org owns accountID ("" for a person's own) and which org it is
// attributed to ("" for none): metadata only, no secret. found is false for an account that no
// longer exists.
func ProviderAccountOrgs(ctx context.Context, db orm.Context, accountID string) (ownerOrg, attributedOrg string, found bool, err error) {
	var row struct {
		OrgID           *string `bun:"org_id"`
		AttributedOrgID *string `bun:"attributed_org_id"`
	}
	err = db.Bun().NewRaw(`SELECT org_id, attributed_org_id FROM provider_accounts WHERE id = ?`, accountID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("read a provider account's orgs: %w", err)
	}
	return acctText(row.OrgID), acctText(row.AttributedOrgID), true, nil
}
