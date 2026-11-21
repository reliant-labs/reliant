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

func TestUserImageIncludesReferenceAndVisionBytes(t *testing.T) {
	t.Parallel()
	id := "stored-photo"
	photo := []byte{0xff, 0xd8, 0xff}
	repo := &MockAttachmentReader{GetAttachmentFunc: func(_ context.Context, got string) (*db.Attachment, error) {
		assert.Equal(t, id, got)
		return &db.Attachment{ID: id, Filename: "portrait.jpg", MimeType: "image/jpeg", Content: photo}, nil
	}}
	block := &db.MessageContentBlock{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: &id}
	result := buildMessage(context.Background(), &db.Message{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER}, []*db.MessageContentBlock{block}, nil, repo)
	require.Len(t, result.Parts, 2)
	ref, ok := result.Parts[0].(message.TextContent)
	require.True(t, ok)
	assert.Contains(t, ref.Text, "attachment_id="+id)
	assert.Contains(t, ref.Text, "save_attachment")
	assert.Equal(t, message.BinaryContent{MIMEType: "image/jpeg", Data: photo}, result.Parts[1])
}
