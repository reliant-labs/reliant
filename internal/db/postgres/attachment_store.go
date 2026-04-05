package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type attachmentStore struct {
	q  pgdb.Querier
	db pgdb.DBTX
}

// NewAttachmentStore creates the Postgres attachment store implementation.
func NewAttachmentStore(q pgdb.Querier, db pgdb.DBTX) core.AttachmentStore {
	return &attachmentStore{q: q, db: db}
}

func (s *attachmentStore) CreateAttachment(ctx context.Context, attachment *core.Attachment) error {
	return s.q.CreateAttachment(ctx, pgdb.CreateAttachmentParams{
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
	return attachmentFromPG(row), nil
}

func (s *attachmentStore) GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*core.Attachment, error) {
	if len(ids) == 0 {
		return []*core.Attachment{}, nil
	}

	// Build the IN clause with Postgres positional parameters ($1, $2, $3, ...)
	// The sqlc-generated code doesn't properly handle sqlc.slice() for Postgres
	// with database/sql - it generates IN ($1) which only matches the first ID.
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT id, user_id, filename, size, mime_type, file_hash, file_path, created_at, updated_at, attachment_type, content
		FROM attachments
		WHERE id IN (%s)`,
		strings.Join(placeholders, ", "),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get attachments by IDs: %w", err)
	}
	defer rows.Close()

	var attachments []*core.Attachment
	for rows.Next() {
		var a pgdb.Attachment
		if err := rows.Scan(
			&a.ID, &a.UserID, &a.Filename, &a.Size, &a.MimeType,
			&a.FileHash, &a.FilePath, &a.CreatedAt, &a.UpdatedAt,
			&a.AttachmentType, &a.Content,
		); err != nil {
			return nil, fmt.Errorf("failed to scan attachment: %w", err)
		}
		attachments = append(attachments, attachmentFromPG(a))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate attachments: %w", err)
	}

	return attachments, nil
}

func (s *attachmentStore) DeleteAttachment(ctx context.Context, id string) error {
	return s.q.DeleteAttachment(ctx, id)
}

func attachmentFromPG(sa pgdb.Attachment) *core.Attachment {
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
