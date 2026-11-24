// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Attachment. The generated attachment_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: attachmentRepo (a *crud.Repo[Attachment]) and the
//     package-level GetAttachmentByID / ListAttachment / CreateAttachment / UpdateAttachment / DeleteAttachment
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Attachment struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindAttachmentBy(ctx context.Context, db orm.Context, /* args */) ([]*Attachment, error) {
//		var rows []*Attachment
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveAttachments(ctx context.Context, db orm.Context) ([]*Attachment, error) {
//		return ListAttachment(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertAttachment stores a new attachment. Its created_at and updated_at are
// the time of the write, whatever a carries; both are written back onto a.
func InsertAttachment(ctx context.Context, db orm.Context, a *core.Attachment) error {
	row := &Attachment{
		ID:             a.ID,
		UserID:         a.UserID,
		Filename:       a.Filename,
		Size:           a.Size,
		MimeType:       a.MimeType,
		FileHash:       a.FileHash,
		FilePath:       a.FilePath,
		AttachmentType: a.AttachmentType,
		Content:        a.Content,
	}
	if err := CreateAttachment(ctx, db, row); err != nil {
		return err
	}
	a.CreatedAt, a.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

// GetAttachment returns the attachment, or (nil, nil) when there is none.
func GetAttachment(ctx context.Context, db orm.Context, id string) (*core.Attachment, error) {
	row, err := GetAttachmentByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return attachmentToCore(row), nil
}

// GetAttachmentsByIDs returns the attachments among ids that exist, in no
// particular order.
func GetAttachmentsByIDs(ctx context.Context, db orm.Context, ids []string) ([]*core.Attachment, error) {
	if len(ids) == 0 {
		return []*core.Attachment{}, nil
	}
	rows, err := ListAttachment(ctx, db, orm.WhereIn(AttachmentFieldID, ids))
	if err != nil {
		return nil, err
	}
	out := make([]*core.Attachment, len(rows))
	for i, row := range rows {
		out[i] = attachmentToCore(row)
	}
	return out, nil
}

// RemoveAttachment deletes the attachment. A missing one is not an error.
func RemoveAttachment(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteAttachment(ctx, db, id); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

func attachmentToCore(row *Attachment) *core.Attachment {
	return &core.Attachment{
		ID:             row.ID,
		UserID:         row.UserID,
		Filename:       row.Filename,
		Size:           row.Size,
		MimeType:       row.MimeType,
		FileHash:       row.FileHash,
		FilePath:       row.FilePath,
		AttachmentType: row.AttachmentType,
		Content:        row.Content,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// AttachmentExtra carries fields on Attachment that are NOT columns — computed
// values, transient state, anything derived rather than stored. Attachment is
// embedded on AttachmentExtra (see attachment_orm.go), so a field added here is a field on
// every Attachment value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type AttachmentExtra struct {
}

// AttachmentUploaders maps each of ids that exists to its uploader
// (attachments.user_id), without reading the bytes: what a send asks to
// check that every attachment it names is the sender's own.
func AttachmentUploaders(ctx context.Context, db orm.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT id, user_id FROM attachments WHERE id = ANY($1::text[])`, orm.StringArray(ids))
	if err != nil {
		return nil, fmt.Errorf("read attachment uploaders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, userID string
		if err := rows.Scan(&id, &userID); err != nil {
			return nil, fmt.Errorf("scan attachment uploader: %w", err)
		}
		out[id] = userID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read attachment uploaders: %w", err)
	}
	return out, nil
}

// AttachedInChatOwnedBy reports whether attachment id is one a chat member
// attached to their own message (an image, file reference or document
// content block of a message whose author_user_id is the attachment's
// uploader) in a chat ownerID owns: besides its uploader, the one person who
// may read it (control-plane docs/design/chat-collaboration.md §6.4). The
// owner is chats.user_id, never another member, so the rule cannot widen to
// the chat's other members; and the message must be the uploader's own, so an
// owner cannot reach someone else's file by naming its id in a send of their
// own. The block types are literals so idx_content_blocks_attachment's
// predicate matches.
func AttachedInChatOwnedBy(ctx context.Context, db orm.Context, id, ownerID string) (bool, error) {
	if id == "" || ownerID == "" {
		return false, nil
	}
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1 FROM message_content_blocks b
    JOIN messages m ON m.id = b.message_id
    JOIN chats c ON c.id = m.chat_id
    JOIN attachments a ON a.id = b.content
    WHERE b.content = $1 AND b.block_type IN (4, 6, 7)
      AND c.user_id = $2 AND m.author_user_id = a.user_id)`, id, ownerID).Scan(&ok)
	return ok, err
}
