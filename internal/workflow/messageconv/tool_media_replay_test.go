// Copyright (c) 2025 Reliant Labs
package messageconv

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A tool message's IMAGE and DOCUMENT blocks come back as the BinaryParts of
// the tool result they follow, because that is the only place any driver
// reads tool media from. They used to come back as bare BinaryContent parts
// on the tool message, which every driver ignores, so the model saw the
// view tool's caption and never the screenshot.
func TestDbMessagesToMessages_FoldsToolMediaIntoItsResult(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G'}
	pdf := []byte("%PDF-1.4")
	atts := map[string]*db.Attachment{
		"img": {ID: "img", Filename: "dashboard-end.png", MimeType: "image/png", Content: png},
		"pdf": {ID: "pdf", Filename: "report.pdf", MimeType: "application/pdf", Content: pdf},
		"vid": {ID: "vid", Filename: "clip.mp4", MimeType: "video/mp4", Content: []byte("mp4")},
	}
	ptr := func(s string) *string { return &s }
	f := false
	blocks := []*db.MessageContentBlock{
		{MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr("call_view"), Content: ptr("Image file: dashboard-end.png (image/png, 241.0KB)"), IsError: &f},
		{MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: ptr("img")},
		{MessageID: "m", Position: 2, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr("call_bash"), Content: ptr("ok"), IsError: &f},
		{MessageID: "m", Position: 3, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr("call_pdf"), Content: ptr("PDF file: report.pdf"), IsError: &f},
		{MessageID: "m", Position: 4, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT, Content: ptr("pdf")},
		{MessageID: "m", Position: 5, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: ptr("vid")},
		{MessageID: "m", Position: 6, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: ptr("missing")},
	}
	repo := &MockStore{
		ListContentBlocksForMessagesFunc: func(context.Context, []string) ([]*db.MessageContentBlock, error) { return blocks, nil },
		GetContextWindowFunc:             func(context.Context, string) (*db.ContextWindow, error) { return &db.ContextWindow{}, nil },
		GetAttachmentFunc: func(_ context.Context, id string) (*db.Attachment, error) {
			return atts[id], nil
		},
	}

	msgs, err := DbMessagesToMessages(context.Background(), []*db.Message{
		{ID: "m", ChatID: "chat", Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL, ContextWindowID: "cw"},
	}, repo)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	for _, part := range msgs[0].Parts {
		_, bare := part.(message.BinaryContent)
		assert.False(t, bare, "tool media must not be left as a bare part no driver reads")
	}
	results := msgs[0].ToolResults()
	require.Len(t, results, 3)

	require.Len(t, results[0].BinaryParts, 1, "the screenshot rides on the view result")
	assert.Equal(t, "image/png", results[0].BinaryParts[0].MIMEType)
	assert.Equal(t, png, results[0].BinaryParts[0].Data)
	assert.Equal(t, "dashboard-end.png", results[0].BinaryParts[0].Path)

	assert.Empty(t, results[1].BinaryParts, "a result with no media gains none")

	require.Len(t, results[2].BinaryParts, 1, "a PDF replays its bytes, not a read_attachment pointer; video and a missing row are dropped")
	assert.Equal(t, "application/pdf", results[2].BinaryParts[0].MIMEType)
	assert.Equal(t, pdf, results[2].BinaryParts[0].Data)
	assert.Contains(t, results[2].Content, "Attachment missing of this result could not be loaded",
		"a missing row leaves a note on its result instead of vanishing")
	assert.NotContains(t, results[2].Content, "vid", "generated video is dropped without a note")
}

// A media block with no tool result before it is not the fold's to consume:
// it stays a part, as on any other message.
func TestDbMessagesToMessages_OrphanToolMediaIsKept(t *testing.T) {
	t.Parallel()

	id := "img"
	repo := &MockStore{
		ListContentBlocksForMessagesFunc: func(context.Context, []string) ([]*db.MessageContentBlock, error) {
			return []*db.MessageContentBlock{{MessageID: "t", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: &id}}, nil
		},
		GetContextWindowFunc: func(context.Context, string) (*db.ContextWindow, error) { return &db.ContextWindow{}, nil },
		GetAttachmentFunc: func(context.Context, string) (*db.Attachment, error) {
			return &db.Attachment{MimeType: "image/png", Content: []byte("png")}, nil
		},
	}
	msgs, err := DbMessagesToMessages(context.Background(), []*db.Message{
		{ID: "t", ChatID: "chat", Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL, ContextWindowID: "cw"},
	}, repo)
	require.NoError(t, err)
	require.Len(t, msgs[0].BinaryContent(), 1)
}

// On a user message an IMAGE block is still its own part: only tool messages
// fold.
func TestDbMessagesToMessages_UserImageStaysAPart(t *testing.T) {
	t.Parallel()

	id := "img"
	repo := &MockStore{
		ListContentBlocksForMessagesFunc: func(context.Context, []string) ([]*db.MessageContentBlock, error) {
			return []*db.MessageContentBlock{{MessageID: "u", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: &id}}, nil
		},
		GetContextWindowFunc: func(context.Context, string) (*db.ContextWindow, error) { return &db.ContextWindow{}, nil },
		GetAttachmentFunc: func(context.Context, string) (*db.Attachment, error) {
			return &db.Attachment{MimeType: "image/png", Content: []byte("png")}, nil
		},
	}
	msgs, err := DbMessagesToMessages(context.Background(), []*db.Message{
		{ID: "u", ChatID: "chat", Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, ContextWindowID: "cw"},
	}, repo)
	require.NoError(t, err)
	require.Len(t, msgs[0].BinaryContent(), 1)
}
