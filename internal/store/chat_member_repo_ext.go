// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatMember. The generated chat_member_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatMemberRepo (a *crud.Repo[ChatMember]) and the
//     package-level GetChatMemberByPK / ListChatMember / CreateChatMember / UpsertChatMember, keyed by
//     the whole composite key (chat_id, user_id), plus UpdateChatMember / DeleteChatMemberByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ChatMember struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of ChatMemberExtStore (chat_member_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindChatMemberBy(ctx context.Context, db orm.Context, /* args */) ([]*ChatMember, error) {
//		var rows []*ChatMember
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("role = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChatMembers(ctx context.Context, db orm.Context) ([]*ChatMember, error) {
//		return ListChatMember(ctx, db, orm.WhereEq("role", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateChatMemberMasked per row (N round trips: it times out at scale). The
// generated UpdateChatMemberWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveChatMembers(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*ChatMember)(nil)).
//			Set("role = ?", to).
//			Where("role = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Per-person grants on a chat (chat_members; control-plane
// docs/design/chat-collaboration.md §3). The rows are read and written only
// here and decided on only in internal/authz (its guard_test holds both).

// ChatMemberRole is the level userID holds on chatID: "" when they hold none.
// It is the authz.Grants reader, and reads uncached (on the pool, or in the
// transaction ctx carries), so a revoke bites at the next decision. A role
// the row holds is returned as stored; deciding whether this build knows it
// is authz's.
func ChatMemberRole(ctx context.Context, db orm.Context, chatID, userID string) (core.ChatMemberRole, error) {
	if chatID == "" || userID == "" {
		return "", nil
	}
	var role string
	err := db.Bun().NewSelect().TableExpr(ChatMemberTableName).Column(ChatMemberFieldRole).
		Where("chat_id = ?", chatID).
		Where("user_id = ?", userID).
		Scan(ctx, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading a chat grant: %w", err)
	}
	return core.ChatMemberRole(role), nil
}

// ListChatMembers lists chatID's members, oldest first. It is a read of the
// rows, not a decision: who may see which of them is the caller's to decide
// (authz.ReadChatAccess, then the shaping of chat-collaboration.md §2.2).
func ListChatMembers(ctx context.Context, db orm.Context, chatID string) ([]*core.ChatMember, error) {
	rows, err := ListChatMember(ctx, db,
		orm.WhereEq(ChatMemberFieldChatID, chatID),
		orm.WithOrderBy(ChatMemberFieldCreatedAt, orm.Asc),
		orm.WithOrderBy(ChatMemberFieldUserID, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("listing chat members: %w", err)
	}
	out := make([]*core.ChatMember, len(rows))
	for i, r := range rows {
		out[i] = chatMemberToCore(r)
	}
	return out, nil
}

// GetChatMember is targetID's grant on chatID, or nil when they hold none.
func GetChatMember(ctx context.Context, db orm.Context, chatID, userID string) (*core.ChatMember, error) {
	row, err := GetChatMemberByPK(ctx, db, chatID, userID)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading a chat member: %w", err)
	}
	return chatMemberToCore(row), nil
}

func chatMemberToCore(r *ChatMember) *core.ChatMember {
	return &core.ChatMember{
		ChatID: r.ChatID, UserID: r.UserID, Role: core.ChatMemberRole(r.Role),
		GrantedBy: r.GrantedBy, CreatedAt: r.CreatedAt,
	}
}

// LockChatMembers locks the chat before the caller reads and authorizes a
// membership change. It must run in the same transaction as the decision and
// ApplyChatMemberChange, so a revocation cannot pass an in-flight decision.
func LockChatMembers(ctx context.Context, db orm.Context, chatID string) error {
	var locked string
	err := db.Bun().NewRaw(`SELECT id FROM chats WHERE id = ? FOR UPDATE`, chatID).Scan(ctx, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // The subsequent authorized read returns NotFound.
	}
	return err
}

// ApplyChatMemberChange applies one change of one person's grant on a chat
// and records it in share_events, as one unit: run it inside a transaction
// (db.RunTx) so the two land together. It returns the share action recorded
// (core.ShareActionGrant, ShareActionRoleChange or ShareActionRevoke), or ""
// when nothing changed.
//
// It first locks the chat's row, and only while the chat still has
// c.OwnerUserID as its owner and c.OrgID as its org, so a change decided on a
// stale read writes nothing, and concurrent changes of one chat's members
// serialize there: the cap of core.MaxChatMembers is counted under that lock
// (ErrChatMembersFull), so it cannot be raced past. A grant of the level
// already held, and removing a grant nobody holds, write nothing and record
// nothing. A role change keeps when the person first became a member and
// records the actor as who granted the level they now hold.
func ApplyChatMemberChange(ctx context.Context, db orm.Context, c core.ChatMemberChange) (core.ShareAction, error) {
	if c.ChatID == "" || c.OrgID == "" || c.OwnerUserID == "" || c.ActorUserID == "" || c.TargetUserID == "" {
		return "", fmt.Errorf("chat member: chat, org, owner, actor and target are required")
	}
	if c.Role != "" && !c.Role.Valid() {
		return "", fmt.Errorf("chat member: unknown role %q", c.Role)
	}
	if c.TargetUserID == c.OwnerUserID {
		return "", fmt.Errorf("chat member: the owner is never a member")
	}
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()

	var locked string
	err := db.Bun().NewRaw(`SELECT id FROM chats WHERE id = ? AND user_id = ? AND org_id = ? FOR UPDATE`,
		c.ChatID, c.OwnerUserID, c.OrgID).Scan(ctx, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("chat member: locking the chat: %w", err)
	}
	current, err := GetChatMember(ctx, db, c.ChatID, c.TargetUserID)
	if err != nil {
		return "", err
	}

	var action core.ShareAction
	switch {
	case c.Role == "" && current == nil:
		return "", nil
	case c.Role == "":
		if err := DeleteChatMemberByPK(ctx, db, c.ChatID, c.TargetUserID); err != nil {
			return "", fmt.Errorf("chat member: removing: %w", err)
		}
		action = core.ShareActionRevoke
	case current != nil && current.Role == c.Role:
		return "", nil
	case current != nil:
		if _, err := db.Bun().NewUpdate().TableExpr(ChatMemberTableName).
			Set("role = ?", string(c.Role)).
			Set("granted_by = ?", c.ActorUserID).
			Where("chat_id = ?", c.ChatID).
			Where("user_id = ?", c.TargetUserID).
			Exec(ctx); err != nil {
			return "", fmt.Errorf("chat member: changing the level: %w", err)
		}
		action = core.ShareActionRoleChange
	default:
		n, err := CountChatMember(ctx, db, orm.WhereEq(ChatMemberFieldChatID, c.ChatID))
		if err != nil {
			return "", fmt.Errorf("chat member: counting: %w", err)
		}
		if n >= core.MaxChatMembers {
			return "", core.ErrChatMembersFull
		}
		if err := CreateChatMember(ctx, db, &ChatMember{
			ChatID: c.ChatID, UserID: c.TargetUserID, Role: string(c.Role), GrantedBy: c.ActorUserID, CreatedAt: at,
		}); err != nil {
			return "", fmt.Errorf("chat member: granting: %w", err)
		}
		action = core.ShareActionGrant
	}

	ev := &ShareEvent{
		ID:           uuid.NewString(),
		OrgID:        c.OrgID,
		ResourceKind: string(core.ShareKindChat),
		ResourceID:   c.ChatID,
		OwnerUserID:  c.OwnerUserID,
		ActorUserID:  c.ActorUserID,
		Action:       string(action),
		TargetUserID: &c.TargetUserID,
		CreatedAt:    at,
	}
	if c.Role != "" {
		role := string(c.Role)
		ev.Role = &role
	}
	if err := CreateShareEvent(ctx, db, ev); err != nil {
		return "", fmt.Errorf("chat member: recording the change: %w", err)
	}
	return action, nil
}

// ChatMemberExtra carries fields on ChatMember that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatMember is
// embedded on ChatMemberExtra (see chat_member_orm.go), so a field added here is a field on
// every ChatMember value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatMemberExtra struct {
}
