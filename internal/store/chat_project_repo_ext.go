// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ChatProject. The generated chat_project_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: chatProjectRepo (a *crud.Repo[ChatProject]) and the
//     package-level GetChatProjectByPK / ListChatProject / CreateChatProject / UpsertChatProject, keyed by
//     the whole composite key (chat_id, project_id), plus UpdateChatProject / DeleteChatProjectByPK
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ChatProject struct (or your own row type).
//     Placeholders follow the path: bun's `?` off db.Bun() (builders,
//     NewRaw); postgres's $1, $2, … in db.Exec / db.Query / db.QueryRow,
//     which send the SQL as written (`?` there is the jsonb operator).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Each exported func(ctx context.Context, db orm.Context, …) in this file is
// also a method of ChatProjectExtStore (chat_project_ext_store_gen.go, regenerated
// from this file every forge generate), the handle bound: name that in a
// service's Deps — forge wires and mocks it — instead of hand-writing an
// adapter over these functions.
//
// Example (uncomment and adapt):
//
//	func FindChatProjectBy(ctx context.Context, db orm.Context, /* args */) ([]*ChatProject, error) {
//		var rows []*ChatProject
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("daemon_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveChatProjects(ctx context.Context, db orm.Context) ([]*ChatProject, error) {
//		return ListChatProject(ctx, db, orm.WhereEq("daemon_id", true))
//	}
//
// A write over many rows is ONE set-based statement, never List then an
// UpdateChatProjectMasked per row (N round trips: it times out at scale). The
// generated UpdateChatProjectWhere(ctx, db, values, fields, filters...) is that
// statement over List's own filters. For one it cannot express, go through
// db.Bun() like any raw query (there is no orm.Ident — name columns in the
// SQL, values as `?` arguments):
//
//	func MoveChatProjects(ctx context.Context, db orm.Context, from, to any) (int64, error) {
//		res, err := db.Bun().NewUpdate().Model((*ChatProject)(nil)).
//			Set("daemon_id = ?", to).
//			Where("daemon_id = ?", from).
//			Exec(ctx)
//		if err != nil {
//			return 0, err
//		}
//		return res.RowsAffected()
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListChatProjectsForChat returns the projects attached to the chat, oldest
// attachment first. The chat's primary project is never among them.
func ListChatProjectsForChat(ctx context.Context, db orm.Context, chatID string) ([]*core.ChatProject, error) {
	rows, err := ListChatProject(ctx, db,
		orm.WhereEq(ChatProjectFieldChatID, chatID),
		orm.WithOrderBy(ChatProjectFieldCreatedAt+", "+ChatProjectFieldProjectID, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list the chat's attached projects: %w", err)
	}
	out := make([]*core.ChatProject, len(rows))
	for i, row := range rows {
		out[i] = chatProjectToCore(row)
	}
	return out, nil
}

// LockChatProjects takes the transaction-scoped advisory lock on a chat's
// attached projects, so a count-then-insert (the attachment cap) is not
// raced by another attach. ctx must carry a transaction.
func LockChatProjects(ctx context.Context, db orm.Context, chatID string) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "chat-projects:"+chatID); err != nil {
		return fmt.Errorf("failed to lock the chat's attached projects: %w", err)
	}
	return nil
}

// InsertChatProject records an attachment. One that exists already is left
// as it is (its machine and time included), and is not an error.
func InsertChatProject(ctx context.Context, db orm.Context, cp *core.ChatProject) error {
	row := &ChatProject{ChatID: cp.ChatID, ProjectID: cp.ProjectID, DaemonID: cp.DaemonID}
	_, err := db.Bun().NewInsert().Model(row).
		Column(ChatProjectFieldChatID, ChatProjectFieldProjectID, ChatProjectFieldDaemonID).
		On("CONFLICT (chat_id, project_id) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to attach the project to the chat: %w", err)
	}
	return nil
}

// DeleteChatProject removes an attachment; none is not an error.
func DeleteChatProject(ctx context.Context, db orm.Context, chatID, projectID string) error {
	if err := DeleteChatProjectByPK(ctx, db, chatID, projectID); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return fmt.Errorf("failed to detach the project from the chat: %w", err)
	}
	return nil
}

// CopyChatProjects gives toChatID every attachment fromChatID has, with the
// same machine (a branch of a chat works in the same projects). Attachments
// toChatID has already are kept.
func CopyChatProjects(ctx context.Context, db orm.Context, fromChatID, toChatID string) error {
	_, err := db.Bun().NewRaw(`INSERT INTO chat_projects (chat_id, project_id, daemon_id)
SELECT ?1, project_id, daemon_id FROM chat_projects WHERE chat_id = ?0
ON CONFLICT (chat_id, project_id) DO NOTHING`, fromChatID, toChatID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to copy the chat's attached projects: %w", err)
	}
	return nil
}

func chatProjectToCore(row *ChatProject) *core.ChatProject {
	return &core.ChatProject{
		ChatID:    row.ChatID,
		ProjectID: row.ProjectID,
		DaemonID:  row.DaemonID,
		CreatedAt: row.CreatedAt,
	}
}

// ChatProjectExtra carries fields on ChatProject that are NOT columns — computed
// values, transient state, anything derived rather than stored. ChatProject is
// embedded on ChatProjectExtra (see chat_project_orm.go), so a field added here is a field on
// every ChatProject value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ChatProjectExtra struct {
}
