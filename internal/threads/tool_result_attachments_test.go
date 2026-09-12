// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"errors"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A generated image is persisted as an attachment row and handed to the model
// as BinaryParts, but until the tool result also produced an IMAGE content
// block the USER saw nothing but an id in a wall of tool-result text. These
// tests pin the block that closes that gap.
//
// They drive createToolContentBlocks through a fake repository rather than the
// package's Postgres harness. That harness t.Skip()s when DATABASE_URL is
// unset, and a skipped test that reports "ok" is indistinguishable from a
// passing one — every assertion below would silently vanish on a machine with
// no database, which is exactly the machine this was written on.

// fakeBlockRepo records content blocks in memory and serves attachment
// metadata from a fixed map. It embeds Repository so the many methods this
// path never calls do not have to be spelled out; calling one panics loudly
// rather than passing silently.
type fakeBlockRepo struct {
	Repository
	blocks      []*db.MessageContentBlock
	attachments map[string]*db.Attachment
	getErr      error
	createErr   error
}

func newFakeBlockRepo() *fakeBlockRepo {
	return &fakeBlockRepo{attachments: map[string]*db.Attachment{}}
}

func (r *fakeBlockRepo) CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error {
	if r.createErr != nil {
		return r.createErr
	}
	copied := *block
	r.blocks = append(r.blocks, &copied)
	return nil
}

func (r *fakeBlockRepo) GetAttachment(ctx context.Context, id string) (*db.Attachment, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.attachments[id], nil
}

func (r *fakeBlockRepo) withImage(id string) *fakeBlockRepo {
	r.attachments[id] = &db.Attachment{
		ID:             id,
		Filename:       "generated-abc123.png",
		MimeType:       "image/png",
		AttachmentType: string(attachment.TypeImage),
		Content:        []byte{0x89, 0x50, 0x4E, 0x47},
	}
	return r
}

// TestToolResultAttachments_MaterializeImageBlocks is the core contract: an
// attachment id on a tool result becomes an IMAGE block whose Content is the
// id — byte-for-byte the representation the user-upload path produces, so the
// existing renderer and /api/attachments/{id} serving work unchanged.
func TestToolResultAttachments_MaterializeImageBlocks(t *testing.T) {
	repo := newFakeBlockRepo().withImage("att-image-1")
	svc := NewService(repo)
	timestamp := time.Now().UTC()

	err := svc.createToolContentBlocks(context.Background(), "msg-1", SaveMessageOpts{
		ToolResults: []ToolResult{{
			ToolCallID:    "toolu_1",
			Name:          "generate_image",
			Content:       "Generated generated-abc123.png.\nAttachment id: att-image-1",
			AttachmentIDs: []string{"att-image-1"},
		}},
	}, timestamp)
	require.NoError(t, err)

	require.Len(t, repo.blocks, 2, "expected the tool_result block plus a sibling image block")

	resultBlock := repo.blocks[0]
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, resultBlock.BlockType)
	assert.Equal(t, 0, resultBlock.Position)
	require.NotNil(t, resultBlock.ToolCallID)
	assert.Equal(t, "toolu_1", *resultBlock.ToolCallID)

	imageBlock := repo.blocks[1]
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, imageBlock.BlockType,
		"a generated image must render through the same block type an uploaded one does")
	require.NotNil(t, imageBlock.Content)
	assert.Equal(t, "att-image-1", *imageBlock.Content,
		"the image block's content must be the attachment ID, matching the user-upload path")
	assert.Equal(t, 1, imageBlock.Position, "the image must follow the result it belongs to")
	assert.Equal(t, "msg-1", imageBlock.MessageID)
	assert.Nil(t, imageBlock.ToolCallID, "an image block is not a tool result and must not claim a tool call id")
}

// Positions must stay dense and ordered across several results, because the
// renderer sorts blocks by index. An image that sorts before the result it
// came from, or two blocks sharing an index, reorders the transcript.
func TestToolResultAttachments_PositionsInterleaveWithResults(t *testing.T) {
	repo := newFakeBlockRepo().withImage("att-a").withImage("att-b")
	svc := NewService(repo)

	err := svc.createToolContentBlocks(context.Background(), "msg-2", SaveMessageOpts{
		ToolResults: []ToolResult{
			{ToolCallID: "toolu_1", Name: "generate_image", Content: "one", AttachmentIDs: []string{"att-a"}},
			{ToolCallID: "toolu_2", Name: "bash", Content: "no attachments here"},
			{ToolCallID: "toolu_3", Name: "generate_image", Content: "two", AttachmentIDs: []string{"att-b"}},
		},
	}, time.Now().UTC())
	require.NoError(t, err)

	require.Len(t, repo.blocks, 5)

	type blockShape struct {
		blockType reliantv1.ContentBlockType
		position  int
	}
	actual := make([]blockShape, 0, len(repo.blocks))
	for _, block := range repo.blocks {
		actual = append(actual, blockShape{block.BlockType, block.Position})
	}

	result := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT
	image := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE
	assert.Equal(t, []blockShape{
		{result, 0}, // toolu_1
		{image, 1},  // att-a
		{result, 2}, // toolu_2, no attachment
		{result, 3}, // toolu_3
		{image, 4},  // att-b
	}, actual, "positions must be dense and each image must directly follow its own result")
}

// A tool result with no attachments must produce exactly the blocks it always
// did. This is the regression guard for every existing tool.
func TestToolResultAttachments_NoAttachmentsIsUnchanged(t *testing.T) {
	repo := newFakeBlockRepo()
	svc := NewService(repo)

	err := svc.createToolContentBlocks(context.Background(), "msg-3", SaveMessageOpts{
		ToolResults: []ToolResult{{ToolCallID: "toolu_1", Name: "bash", Content: "hello"}},
	}, time.Now().UTC())
	require.NoError(t, err)

	require.Len(t, repo.blocks, 1)
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, repo.blocks[0].BlockType)
}

// A missing, unreadable or non-image attachment must not fail the save. The
// message still carries the tool's text and the model's own view of the image,
// and the turn may have cost real money — discarding it over a thumbnail is
// the worse outcome. The tool_result block must still land.
func TestToolResultAttachments_UnusableAttachmentIsSkippedNotFatal(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeBlockRepo)
	}{
		{
			name:  "attachment row is missing",
			setup: func(r *fakeBlockRepo) {},
		},
		{
			name:  "attachment lookup fails",
			setup: func(r *fakeBlockRepo) { r.getErr = errors.New("connection reset") },
		},
		{
			name: "attachment is not an image",
			setup: func(r *fakeBlockRepo) {
				r.attachments["att-x"] = &db.Attachment{
					ID:             "att-x",
					AttachmentType: string(attachment.TypeFileReference),
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeBlockRepo()
			tc.setup(repo)
			svc := NewService(repo)

			err := svc.createToolContentBlocks(context.Background(), "msg-4", SaveMessageOpts{
				ToolResults: []ToolResult{{
					ToolCallID:    "toolu_1",
					Name:          "generate_image",
					Content:       "generated",
					AttachmentIDs: []string{"att-x"},
				}},
			}, time.Now().UTC())
			require.NoError(t, err, "an unusable attachment must not discard a completed turn")

			require.Len(t, repo.blocks, 1, "only the tool_result block should be written")
			assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, repo.blocks[0].BlockType)
		})
	}
}
