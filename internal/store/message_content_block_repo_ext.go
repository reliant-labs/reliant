// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for MessageContentBlock: the content block
// reads and writes of internal/db's Repository, ported off sqlc (P5b lane
// L1a). Every function takes and returns internal/db/core types; the
// generated MessageContentBlock row stays inside this package.
//
// Streaming is the hot path here: AppendToContentBlock runs once per provider
// delta, and ListContentBlocksForMessages loads every page of a transcript.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertContentBlock writes block as a new row. Its free text is made valid
// UTF-8 IN PLACE first (NormalizeContentBlock), so the caller holds what was
// stored.
func InsertContentBlock(ctx context.Context, db orm.Context, block *core.MessageContentBlock) error {
	NormalizeContentBlock(block)
	return CreateMessageContentBlock(ctx, db, contentBlockRow(block))
}

// InsertContentBlockOnce writes block unless a row already holds its id (an
// activity retry re-sending the block), normalizing it in place first. The
// row is written exactly as given — created_at and updated_at included.
func InsertContentBlockOnce(ctx context.Context, db orm.Context, block *core.MessageContentBlock) error {
	NormalizeContentBlock(block)
	_, err := db.Bun().NewInsert().Model(contentBlockRow(block)).On("CONFLICT (id) DO NOTHING").Exec(ctx)
	return err
}

// FindContentBlock reads one block by id, or nil when no row holds it.
func FindContentBlock(ctx context.Context, db orm.Context, id string) (*core.MessageContentBlock, error) {
	row, err := GetMessageContentBlockByID(ctx, db, id)
	if err != nil {
		if isMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	return contentBlockFromRow(row), nil
}

// FindToolBlock reads the block of blockType (a TOOL_CALL or a TOOL_RESULT)
// that carries toolCallID, or nil.
func FindToolBlock(ctx context.Context, db orm.Context, toolCallID string, blockType reliantv1.ContentBlockType) (*core.MessageContentBlock, error) {
	rows, err := ListMessageContentBlock(ctx, db,
		orm.WhereEq(MessageContentBlockFieldToolCallID, toolCallID),
		orm.WhereEq(MessageContentBlockFieldBlockType, blockType),
		orm.WithLimit(1))
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return contentBlockFromRow(rows[0]), nil
}

// ListContentBlocks is one message's blocks in position order.
func ListContentBlocks(ctx context.Context, db orm.Context, messageID string) ([]*core.MessageContentBlock, error) {
	rows, err := ListMessageContentBlock(ctx, db,
		orm.WhereEq(MessageContentBlockFieldMessageID, messageID),
		orm.WithOrderBy(MessageContentBlockFieldPosition, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list content blocks: %w", err)
	}
	return contentBlocksFromRows(rows), nil
}

// ListContentBlocksForMessages is the read path's batch block load: every
// block of every message named, ordered by message then position. Loading a
// page of messages must not issue one query per message.
func ListContentBlocksForMessages(ctx context.Context, db orm.Context, messageIDs []string) ([]*core.MessageContentBlock, error) {
	if len(messageIDs) == 0 {
		return []*core.MessageContentBlock{}, nil
	}
	rows, err := ListMessageContentBlock(ctx, db,
		orm.WhereIn(MessageContentBlockFieldMessageID, messageIDs),
		orm.WithOrderBy(MessageContentBlockFieldMessageID, orm.Asc),
		orm.WithOrderBy(MessageContentBlockFieldPosition, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list content blocks for messages: %w", err)
	}
	return contentBlocksFromRows(rows), nil
}

// UpdateContentBlockBody rewrites a block's content, tool_input and version
// (nil writes NULL), normalizing block in place first. A block that does not
// exist is not an error.
func UpdateContentBlockBody(ctx context.Context, db orm.Context, block *core.MessageContentBlock) error {
	NormalizeContentBlock(block)
	row := &MessageContentBlock{ID: block.ID, Content: block.Content, ToolInput: block.ToolInput, Version: intToInt64Ptr(block.Version)}
	err := messageContentBlockRepo.UpdateMasked(ctx, db, row,
		[]string{MessageContentBlockFieldContent, MessageContentBlockFieldToolInput, MessageContentBlockFieldVersion},
		crud.NoReadBack())
	if errors.Is(err, orm.ErrNoRows) {
		return nil
	}
	return err
}

// AppendToContentBlock appends one streamed delta to a block's content and
// counts it in version — the hottest write on the streaming path, one per
// provider delta. The delta's bytes travel inside the statement text (bun
// formats every argument client-side); quotes, backslashes and `?` survive.
func AppendToContentBlock(ctx context.Context, db orm.Context, blockID, delta string) error {
	return appendToBlock(ctx, db, blockID, MessageContentBlockFieldContent, delta, true)
}

// AppendContentDelta appends a text delta to a block's content without
// counting a version.
func AppendContentDelta(ctx context.Context, db orm.Context, blockID, delta string) error {
	return appendToBlock(ctx, db, blockID, MessageContentBlockFieldContent, delta, false)
}

// AppendToolInputDelta appends a JSON delta to a tool call block's
// tool_input.
func AppendToolInputDelta(ctx context.Context, db orm.Context, blockID, delta string) error {
	return appendToBlock(ctx, db, blockID, MessageContentBlockFieldToolInput, delta, false)
}

// appendToBlock is `col = COALESCE(col, ”) || delta`. An empty delta (after
// UTF-8 repair) writes nothing.
func appendToBlock(ctx context.Context, db orm.Context, blockID, col, delta string, bumpVersion bool) error {
	delta = validUTF8(delta)
	if delta == "" {
		return nil
	}
	q := db.Bun().NewUpdate().Model((*MessageContentBlock)(nil)).
		Set("? = COALESCE(?, '') || ?", bun.Ident(col), bun.Ident(col), delta)
	if bumpVersion {
		q = q.Set("version = version + 1")
	}
	_, err := q.Set("updated_at = NOW()").Where("id = ?", blockID).Exec(ctx)
	return err
}

// TouchMessageBlocks stamps updated_at on every block of a message — the
// "streaming finished" mark — and reports how many it touched.
func TouchMessageBlocks(ctx context.Context, db orm.Context, messageID string) (int64, error) {
	res, err := db.Bun().NewUpdate().Model((*MessageContentBlock)(nil)).
		Set("updated_at = NOW()").
		Where("message_id = ?", messageID).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// NormalizeContentBlock makes every free-text field of block valid UTF-8, in
// place, keeping a valid field's pointer as it is (see validUTF8).
func NormalizeContentBlock(block *core.MessageContentBlock) {
	if block == nil {
		return
	}
	block.Content = validUTF8Ptr(block.Content)
	block.ToolName = validUTF8Ptr(block.ToolName)
	block.ToolInput = validUTF8Ptr(block.ToolInput)
	block.ToolCallID = validUTF8Ptr(block.ToolCallID)
	block.ThoughtSignature = validUTF8Ptr(block.ThoughtSignature)
	block.Phase = validUTF8Ptr(block.Phase)
	block.ActivityID = validUTF8Ptr(block.ActivityID)
	block.WorkflowRunID = validUTF8Ptr(block.WorkflowRunID)
}

// contentBlockRow is block as a message_content_blocks row. The node fields
// are plain strings on the core type, so "" is NULL; attempt_number is
// always written (0 when unset).
func contentBlockRow(block *core.MessageContentBlock) *MessageContentBlock {
	attempt := int64(block.AttemptNumber)
	return &MessageContentBlock{
		ID:               block.ID,
		MessageID:        block.MessageID,
		Position:         int64(block.Position),
		BlockType:        block.BlockType,
		Content:          block.Content,
		ToolName:         block.ToolName,
		ToolInput:        block.ToolInput,
		ToolCallID:       block.ToolCallID,
		IsError:          block.IsError,
		Version:          intToInt64Ptr(block.Version),
		NodeID:           emptyNil(block.NodeID),
		NodePath:         emptyNil(block.NodePath),
		ActivityID:       block.ActivityID,
		WorkflowRunID:    block.WorkflowRunID,
		AttemptNumber:    &attempt,
		ThoughtSignature: block.ThoughtSignature,
		Phase:            block.Phase,
		CreatedAt:        block.CreatedAt,
		UpdatedAt:        block.UpdatedAt,
	}
}

func contentBlockFromRow(row *MessageContentBlock) *core.MessageContentBlock {
	block := &core.MessageContentBlock{
		ID:               row.ID,
		MessageID:        row.MessageID,
		Position:         int(row.Position),
		BlockType:        row.BlockType,
		Content:          row.Content,
		ToolName:         row.ToolName,
		ToolInput:        row.ToolInput,
		ToolCallID:       row.ToolCallID,
		ThoughtSignature: row.ThoughtSignature,
		Phase:            row.Phase,
		IsError:          row.IsError,
		Version:          int64ToIntPtr(row.Version),
		ActivityID:       row.ActivityID,
		WorkflowRunID:    row.WorkflowRunID,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	if row.NodeID != nil {
		block.NodeID = *row.NodeID
	}
	if row.NodePath != nil {
		block.NodePath = *row.NodePath
	}
	if row.AttemptNumber != nil {
		block.AttemptNumber = int(*row.AttemptNumber)
	}
	return block
}

func contentBlocksFromRows(rows []*MessageContentBlock) []*core.MessageContentBlock {
	out := make([]*core.MessageContentBlock, len(rows))
	for i, row := range rows {
		out[i] = contentBlockFromRow(row)
	}
	return out
}

// emptyNil is s as a nullable column: "" is NULL.
func emptyNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MessageContentBlockExtra carries fields on MessageContentBlock that are NOT columns — computed
// values, transient state, anything derived rather than stored. MessageContentBlock is
// embedded on MessageContentBlockExtra (see message_content_block_orm.go), so a field added here is a field on
// every MessageContentBlock value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type MessageContentBlockExtra struct {
}
