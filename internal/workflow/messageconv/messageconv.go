// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
package messageconv

import (
	"context"
	"fmt"
	"os"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	attachutil "github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// ProtoMessageRoleToModelRole converts a proto MessageRole to a model MessageRole.
func ProtoMessageRoleToModelRole(role reliantv1.MessageRole) message.MessageRole {
	name := strings.TrimPrefix(role.String(), "MESSAGE_ROLE_")
	if name == "" {
		return message.User
	}
	return message.MessageRole(strings.ToLower(name))
}

// DbMessageToMessage converts a DB message to a message.Message, loading the
// message's content blocks and context window on demand.
//
// Converting a whole thread this way is one round trip per message per lookup.
// Prefer DbMessagesToMessages, which fetches both in bulk.
func DbMessageToMessage(ctx context.Context, dbMsg *db.Message, repo db.Repository) (message.Message, error) {
	blocks, err := repo.ListContentBlocks(ctx, dbMsg.ID)
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to load content blocks: %w", err)
	}

	cw, err := repo.GetContextWindow(ctx, dbMsg.ContextWindowID)
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to get context window for message %s: %w", dbMsg.ID, err)
	}

	return buildMessage(ctx, dbMsg, blocks, cw, repo), nil
}

// DbMessagesToMessages converts a batch of DB messages, fetching content blocks
// and context windows in bulk rather than per message.
//
// The per-message path issues two queries for every message, so a long thread
// becomes hundreds of sequential round trips. That is slow, and it holds the
// caller's context open across all of them — which turns any mid-flight
// cancellation into a failure of the whole history load.
func DbMessagesToMessages(ctx context.Context, dbMessages []*db.Message, repo db.Repository) ([]message.Message, error) {
	if len(dbMessages) == 0 {
		return []message.Message{}, nil
	}

	messageIDs := make([]string, 0, len(dbMessages))
	for _, dbMsg := range dbMessages {
		messageIDs = append(messageIDs, dbMsg.ID)
	}

	allBlocks, err := repo.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load content blocks: %w", err)
	}

	blocksByMessage := make(map[string][]*db.MessageContentBlock, len(dbMessages))
	for _, block := range allBlocks {
		blocksByMessage[block.MessageID] = append(blocksByMessage[block.MessageID], block)
	}

	// Context windows repeat heavily across a thread, so resolve each distinct
	// one once.
	windows := make(map[string]*db.ContextWindow)
	for _, dbMsg := range dbMessages {
		if _, seen := windows[dbMsg.ContextWindowID]; seen {
			continue
		}
		cw, err := repo.GetContextWindow(ctx, dbMsg.ContextWindowID)
		if err != nil {
			return nil, fmt.Errorf("failed to get context window for message %s: %w", dbMsg.ID, err)
		}
		windows[dbMsg.ContextWindowID] = cw
	}

	msgs := make([]message.Message, 0, len(dbMessages))
	for _, dbMsg := range dbMessages {
		msgs = append(msgs, buildMessage(ctx, dbMsg, blocksByMessage[dbMsg.ID], windows[dbMsg.ContextWindowID], repo))
	}
	return msgs, nil
}

// buildMessage assembles a message.Message from already-loaded rows.
func buildMessage(ctx context.Context, dbMsg *db.Message, blocks []*db.MessageContentBlock, cw *db.ContextWindow, repo db.Repository) message.Message {
	parts := make([]message.ContentPart, 0, len(blocks))
	for _, block := range blocks {
		part := ContentBlockToPart(ctx, dbMsg.ChatID, block, repo)
		if part != nil {
			parts = append(parts, part)
		}
	}

	// Get context_sequence from context_window for API compatibility
	var contextSequence int64
	if cw != nil {
		contextSequence = int64(cw.Sequence)
	}

	msg := message.Message{
		ID:              dbMsg.ID,
		Role:            ProtoMessageRoleToModelRole(dbMsg.Role),
		Ordinal:         dbMsg.Ordinal,
		Thread:          dbMsg.ThreadID, // Direct access
		ContextSequence: contextSequence,
		Parts:           parts,
	}

	// Copy token count for context estimation
	if dbMsg.TokenCount != nil {
		msg.TokenCount = int64(*dbMsg.TokenCount)
	}

	return msg
}

// ContentBlockToPart converts a content block to a message part.
func ContentBlockToPart(ctx context.Context, chatID string, block *db.MessageContentBlock, repo db.Repository) message.ContentPart {
	switch block.BlockType {
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
		if block.Content != nil {
			return message.TextContent{Text: *block.Content}
		}

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE:
		if block.Content != nil {
			attachment, err := LoadAttachment(ctx, chatID, *block.Content, repo)
			if err != nil {
				logging.Error("Failed to load attachment for LLM context",
					"attachment_id", *block.Content,
					"chat_id", chatID,
					"error", err)
				return nil
			}
			return message.BinaryContent{
				MIMEType: attachment.MimeType,
				Data:     attachment.Content,
			}
		}

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT:
		// Documents (PDFs) are NOT injected whole — that would flood the context
		// window. Instead we surface a reference the model can read on demand,
		// paginated, via the read_attachment tool.
		if block.Content != nil {
			attachmentID := *block.Content
			attachment, err := LoadAttachment(ctx, chatID, attachmentID, repo)
			if err != nil {
				logging.Error("Failed to load document attachment for LLM context",
					"attachment_id", attachmentID,
					"chat_id", chatID,
					"error", err)
				return nil
			}
			return message.TextContent{
				Text: fmt.Sprintf(
					"[Attached document: %s (attachment_id=%s). Use the read_attachment tool with this "+
						"attachment_id to read it. For PDFs, pass a pages range (e.g. pages=\"1-5\").]",
					attachment.FileName, attachmentID),
			}
		}

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE:
		// File reference: read file content and return as text
		if block.Content != nil {
			attachmentID := *block.Content
			fileContent, filename, err := LoadFileReference(ctx, chatID, attachmentID, repo)
			if err != nil {
				logging.Error("Failed to load file reference for LLM context",
					"attachment_id", attachmentID,
					"chat_id", chatID,
					"error", err)
				// Return error message as text so the LLM knows the file couldn't be read
				return message.TextContent{
					Text: fmt.Sprintf("[Error: Could not read file '%s': %v]", attachmentID, err),
				}
			}
			// Format as text with filename header for context
			return message.TextContent{
				Text: fmt.Sprintf("Contents of %s:\n```\n%s\n```", filename, fileContent),
			}
		}

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
		if block.ToolCallID != nil && block.ToolName != nil {
			input := ""
			if block.ToolInput != nil {
				input = *block.ToolInput
			}
			thoughtSig := ""
			if block.ThoughtSignature != nil {
				thoughtSig = *block.ThoughtSignature
			}
			return message.ToolCall{
				ID:               *block.ToolCallID,
				Name:             *block.ToolName,
				Input:            input,
				ThoughtSignature: thoughtSig,
			}
		}

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
		if block.ToolCallID != nil && block.Content != nil {
			isError := false
			if block.IsError != nil {
				isError = *block.IsError
			}
			return message.ToolResult{
				ToolCallID: *block.ToolCallID,
				Content:    *block.Content,
				IsError:    isError,
			}
		}

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING:
		if block.Content != nil {
			signature := ""
			if block.ThoughtSignature != nil {
				signature = *block.ThoughtSignature
			}
			return message.ReasoningContent{
				Thinking:  *block.Content,
				Signature: signature,
			}
		}
	}
	return nil
}

// LoadAttachment loads an attachment from the database.
func LoadAttachment(ctx context.Context, chatID string, attachmentID string, repo db.Repository) (*message.Attachment, error) {
	att, err := repo.GetAttachment(ctx, attachmentID)
	if err != nil {
		return nil, fmt.Errorf("failed to get attachment: %w", err)
	}
	if att == nil {
		return nil, fmt.Errorf("attachment not found: %s", attachmentID)
	}

	return &message.Attachment{
		FileName: att.Filename,
		MimeType: att.MimeType,
		Content:  att.Content,
	}, nil
}

// LoadFileReference loads a file reference and returns its text content.
// Content is read from the DB (captured at attach time). Falls back to
// reading from the filesystem for legacy records that pre-date content capture.
func LoadFileReference(ctx context.Context, chatID string, attachmentID string, repo db.Repository) (content string, filename string, err error) {
	// Get the attachment metadata from database
	att, err := repo.GetAttachment(ctx, attachmentID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get attachment: %w", err)
	}

	var data []byte

	if len(att.Content) > 0 {
		// Content was captured at attach time — use it directly
		data = att.Content
	} else {
		// Legacy fallback: read from filesystem
		logging.Warn("File reference has no stored content, falling back to filesystem read",
			"attachment_id", attachmentID,
			"filename", att.Filename)

		filePath := att.FilePath
		if filePath == "" {
			return "", "", fmt.Errorf("attachment has no file path and no stored content")
		}

		data, err = os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				return "", att.Filename, fmt.Errorf("file no longer exists: %s", filePath)
			}
			return "", att.Filename, fmt.Errorf("failed to read file: %w", err)
		}
	}

	extracted, err := attachutil.ExtractTextFromFile(att.Filename, data)
	if err != nil {
		return "", att.Filename, err
	}

	return extracted, att.Filename, nil
}
