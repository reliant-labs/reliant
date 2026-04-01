package sqlite

import (
	"context"
	"database/sql"

	"github.com/reliant-labs/reliant/internal/db/core"
	sqlitedb "github.com/reliant-labs/reliant/internal/db/sqlite/generated"
)

type attachmentStore struct{ q sqlitedb.Querier }

// NewAttachmentStore creates the SQLite attachment store implementation.
func NewAttachmentStore(q sqlitedb.Querier) core.AttachmentStore { return &attachmentStore{q: q} }

func (s *attachmentStore) CreateAttachment(ctx context.Context, attachment *core.Attachment) error {
	return s.q.CreateAttachment(ctx, sqlitedb.CreateAttachmentParams{
		ID:             attachment.ID,
		UserID:         attachment.UserID,
		Filename:       attachment.Filename,
		Size:           attachment.Size,
		MimeType:       attachment.MimeType,
		FileHash:       ptrToNullString(attachment.FileHash),
		FilePath:       attachment.FilePath,
		AttachmentType: attachment.AttachmentType,
		Content:        attachment.Content,
	})
}

func (s *attachmentStore) GetAttachment(ctx context.Context, id string) (*core.Attachment, error) {
	row, err := s.q.GetAttachment(ctx, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return attachmentFromSQLc(row), nil
}

func (s *attachmentStore) GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*core.Attachment, error) {
	rows, err := s.q.GetAttachmentsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	attachments := make([]*core.Attachment, len(rows))
	for i, row := range rows {
		attachments[i] = attachmentFromSQLc(row)
	}
	return attachments, nil
}

func (s *attachmentStore) DeleteAttachment(ctx context.Context, id string) error {
	return s.q.DeleteAttachment(ctx, id)
}

func attachmentFromSQLc(sa sqlitedb.Attachment) *core.Attachment {
	return &core.Attachment{
		ID:             sa.ID,
		UserID:         sa.UserID,
		Filename:       sa.Filename,
		Size:           sa.Size,
		MimeType:       sa.MimeType,
		FileHash:       nullStringToPtr(sa.FileHash),
		FilePath:       sa.FilePath,
		AttachmentType: sa.AttachmentType,
		Content:        sa.Content,
		CreatedAt:      sa.CreatedAt,
		UpdatedAt:      sa.UpdatedAt,
	}
}

func ptrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func nullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}
