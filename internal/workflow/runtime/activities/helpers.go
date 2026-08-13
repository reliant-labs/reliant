// Copyright (c) 2025 Reliant Labs
package activities

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
)

// ============================================================================
// Thin wrappers around messageconv — keeps call sites inside this package
// unchanged while the real logic lives in a shared, cycle-free package.
// ============================================================================

func ProtoMessageRoleToModelRole(role reliantv1.MessageRole) message.MessageRole {
	return messageconv.ProtoMessageRoleToModelRole(role)
}

func DbMessageToMessage(ctx context.Context, dbMsg *db.Message, repo db.Repository) (message.Message, error) {
	return messageconv.DbMessageToMessage(ctx, dbMsg, repo)
}

func ContentBlockToPart(ctx context.Context, chatID string, block *db.MessageContentBlock, repo db.Repository) message.ContentPart {
	return messageconv.ContentBlockToPart(ctx, chatID, block, repo)
}

func LoadAttachment(ctx context.Context, chatID string, attachmentID string, repo db.Repository) (*message.Attachment, error) {
	return messageconv.LoadAttachment(ctx, chatID, attachmentID, repo)
}

func LoadFileReference(ctx context.Context, chatID string, attachmentID string, repo db.Repository) (content string, filename string, err error) {
	return messageconv.LoadFileReference(ctx, chatID, attachmentID, repo)
}
