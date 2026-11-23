// Copyright (c) 2025 Reliant Labs
package messageconv

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
)

// An image a tool presented to the user (present_image) rides in an IMAGE
// block like a viewed screenshot does, but it was shown to a person: the
// replay must not hand its bytes to the model, which would cap what can be
// shown at a model's limits and charge its tokens on every later turn.
func TestDbMessagesToMessages_PresentedImageIsNotReplayedToTheModel(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G'}
	atts := map[string]*db.Attachment{
		"shown":  {ID: "shown", Filename: "chart.png", MimeType: "image/png", Content: png, AttachmentType: string(attachment.TypeDisplay)},
		"viewed": {ID: "viewed", Filename: "shot.png", MimeType: "image/png", Content: png, AttachmentType: string(attachment.TypeImage)},
	}
	ptr := func(s string) *string { return &s }
	f := false
	blocks := []*db.MessageContentBlock{
		{MessageID: "m", Position: 0, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr("call_present"), Content: ptr("Showed 1 image to the user"), IsError: &f},
		{MessageID: "m", Position: 1, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: ptr("shown")},
		{MessageID: "m", Position: 2, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr("call_view"), Content: ptr("Image file: shot.png"), IsError: &f},
		{MessageID: "m", Position: 3, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: ptr("viewed")},
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
	require.Len(t, msgs[0].Parts, 2, "the presented image's block is consumed, not left as a bare part")

	results := msgs[0].ToolResults()
	require.Len(t, results, 2)
	assert.Empty(t, results[0].BinaryParts, "a presented image never reaches the model")
	assert.Equal(t, "Showed 1 image to the user", results[0].Content, "and leaves no missing-media note")
	require.Len(t, results[1].BinaryParts, 1, "a viewed image still replays")
}

func TestContentBlockToPart_PresentedImageIsNotReplayedToTheModel(t *testing.T) {
	t.Parallel()

	repo := &MockAttachmentReader{GetAttachmentFunc: func(_ context.Context, id string) (*db.Attachment, error) {
		return &db.Attachment{ID: id, Filename: "chart.png", MimeType: "image/png", Content: []byte("png"),
			AttachmentType: string(attachment.TypeDisplay)}, nil
	}}
	id := "shown"
	block := &db.MessageContentBlock{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: &id}
	assert.Nil(t, ContentBlockToPart(context.Background(), "chat", block, repo))
}
