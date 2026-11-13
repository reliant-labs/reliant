// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatPersonPick. The generated chat_person_pick_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatPersonPickRepo (a *crud.Repo[ChatPersonPick]) and the
//     package-level GetChatPersonPickByID / ListChatPersonPick / CreateChatPersonPick / UpdateChatPersonPick / DeleteChatPersonPick
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ChatPersonPick struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindChatPersonPickBy(ctx context.Context, db orm.Context, /* args */) ([]*ChatPersonPick, error) {
//		var rows []*ChatPersonPick
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChatPersonPicks(ctx context.Context, db orm.Context) ([]*ChatPersonPick, error) {
//		return ListChatPersonPick(ctx, db, orm.WhereEq("user_id", true))
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

// PutChatPersonPick records that userID, a person, picked modelID (and the
// provider switch chatProvider) as chatID's root model in orgID at now,
// replacing the chat's earlier pick.
func PutChatPersonPick(ctx context.Context, db orm.Context, chatID, userID, orgID, modelID, chatProvider string, now time.Time) error {
	_, err := db.Bun().NewRaw(`INSERT INTO chat_person_picks (chat_id, user_id, org_id, model_id, chat_provider, at)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5)
		ON CONFLICT (chat_id) DO UPDATE SET user_id = EXCLUDED.user_id, org_id = EXCLUDED.org_id,
		    model_id = EXCLUDED.model_id, chat_provider = EXCLUDED.chat_provider, at = EXCLUDED.at`,
		chatID, userID, orgID, modelID, chatProvider, now.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record the person's pick for the chat: %w", err)
	}
	return nil
}

// GetChatPersonPick returns chatID's person pick, nil when it has none.
func GetChatPersonPick(ctx context.Context, db orm.Context, chatID string) (*ChatPersonPick, error) {
	row := new(ChatPersonPick)
	err := db.Bun().NewSelect().Model(row).Where("chat_id = ?", chatID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the person's pick for the chat: %w", err)
	}
	return row, nil
}

// ChatPersonPickExtra carries fields on ChatPersonPick that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatPersonPick is
// embedded on ChatPersonPickExtra (see chat_person_pick_orm.go), so a field added here is a field on
// every ChatPersonPick value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatPersonPickExtra struct {
}
