// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatSelection. The generated chat_selection_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatSelectionRepo (a *crud.Repo[ChatSelection]) and the
//     package-level GetChatSelectionByID / ListChatSelection / CreateChatSelection / UpdateChatSelection / DeleteChatSelection
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ChatSelection struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindChatSelectionBy(ctx context.Context, db orm.Context, /* args */) ([]*ChatSelection, error) {
//		var rows []*ChatSelection
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChatSelections(ctx context.Context, db orm.Context) ([]*ChatSelection, error) {
//		return ListChatSelection(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/chatselection"
)

// The chat_selections queries (P5b L1b): one row per chat recording the
// workflow, params and provider policy the composer has selected. The
// statements return internal/chatselection's own type, the caller's.

// GetChatSelection returns the chat's selection, nil with no error when it
// has none. forUpdate locks the row for the rest of the caller's transaction.
func GetChatSelection(ctx context.Context, db orm.Context, chatID string, forUpdate bool) (*chatselection.Selection, error) {
	row := new(ChatSelection)
	q := db.Bun().NewSelect().Model(row).Where("chat_id = ?", chatID)
	if forUpdate {
		q = q.For("UPDATE")
	}
	err := q.Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read chat selection: %w", err)
	}
	return chatSelectionFromRow(row)
}

// LockChatSelection takes the transaction-scoped advisory lock that makes one
// writer of a chat's selection at a time. It covers the first write too, when
// there is no row to lock. ctx must carry a transaction.
func LockChatSelection(ctx context.Context, db orm.Context, chatID string) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "chat-selection:"+chatID); err != nil {
		return fmt.Errorf("failed to lock chat selection: %w", err)
	}
	return nil
}

// upsertChatSelectionSQL writes the selection, advancing version on every
// write so a reader can tell edits apart.
const upsertChatSelectionSQL = `
INSERT INTO chat_selections (chat_id, user_id, workflow_name, params, provider_policy, version, updated_at)
VALUES (?0, ?1, ?2, ?3::jsonb, ?4::jsonb, 1, ?5)
ON CONFLICT (chat_id) DO UPDATE SET
    workflow_name = EXCLUDED.workflow_name,
    params = EXCLUDED.params,
    provider_policy = EXCLUDED.provider_policy,
    version = chat_selections.version + 1,
    updated_at = EXCLUDED.updated_at
RETURNING version`

// UpsertChatSelection writes sel as the chat's selection at now and returns
// the stored record (with its new version). The owner is fixed by the first
// write: a later one keeps user_id.
func UpsertChatSelection(ctx context.Context, db orm.Context, chatID string, sel *chatselection.Selection, now time.Time) (*chatselection.Selection, error) {
	params, err := json.Marshal(sel.Params)
	if err != nil {
		return nil, fmt.Errorf("failed to encode chat selection params: %w", err)
	}
	policy, err := json.Marshal(storedProviderPolicy{ProviderPolicy: sel.Policy, ChatProvider: sel.ChatProvider})
	if err != nil {
		return nil, fmt.Errorf("failed to encode chat selection policy: %w", err)
	}
	var version int64
	err = db.Bun().NewRaw(upsertChatSelectionSQL,
		chatID, sel.UserID, sel.WorkflowName, string(params), string(policy), now).Scan(ctx, &version)
	if err != nil {
		return nil, fmt.Errorf("failed to write chat selection: %w", err)
	}
	stored := *sel
	stored.ChatID = chatID
	stored.Version = version
	stored.UpdatedAt = now
	return &stored, nil
}

// copyChatSelectionSQL starts toChatID's selection as a copy of
// fromChatID's, unless it already has one.
const copyChatSelectionSQL = `
INSERT INTO chat_selections (chat_id, user_id, workflow_name, params, provider_policy, version, updated_at)
SELECT ?0, ?1, workflow_name, params, provider_policy, 1, ?2
FROM chat_selections WHERE chat_id = ?3
ON CONFLICT (chat_id) DO NOTHING`

// CopyChatSelection copies fromChatID's selection onto toChatID for userID. A
// source with no selection, or a target that already has one, is a no-op.
func CopyChatSelection(ctx context.Context, db orm.Context, fromChatID, toChatID, userID string, now time.Time) error {
	if _, err := db.Bun().NewRaw(copyChatSelectionSQL, toChatID, userID, now, fromChatID).Exec(ctx); err != nil {
		return fmt.Errorf("failed to copy chat selection: %w", err)
	}
	return nil
}

func chatSelectionFromRow(row *ChatSelection) (*chatselection.Selection, error) {
	sel := &chatselection.Selection{
		ChatID:       row.ChatID,
		UserID:       row.UserID,
		WorkflowName: row.WorkflowName,
		UpdatedAt:    row.UpdatedAt,
	}
	if row.Version != nil {
		sel.Version = *row.Version
	}
	if err := json.Unmarshal(row.Params, &sel.Params); err != nil {
		return nil, fmt.Errorf("failed to decode chat selection params: %w", err)
	}
	if sel.Params == nil {
		sel.Params = map[string]any{}
	}
	var policy storedProviderPolicy
	if err := json.Unmarshal(row.ProviderPolicy, &policy); err != nil {
		return nil, fmt.Errorf("failed to decode chat selection policy: %w", err)
	}
	sel.Policy, sel.ChatProvider = policy.ProviderPolicy, policy.ChatProvider
	return sel, nil
}

// storedProviderPolicy is the provider_policy column: the chat's on-limit
// policy and, beside it, the provider an explicit switch bound the chat to
// (chatselection.Selection.ChatProvider). Both say how the chat uses
// providers, and the binding needs no column of its own: a record written
// before it existed decodes as unbound.
type storedProviderPolicy struct {
	chatselection.ProviderPolicy
	ChatProvider string `json:"chat_provider,omitempty"`
}

// ChatSelectionExtra carries fields on ChatSelection that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatSelection is
// embedded on ChatSelectionExtra (see chat_selection_orm.go), so a field added here is a field on
// every ChatSelection value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatSelectionExtra struct {
}
