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

// TestUserImageReferenceNamesTypeAndDelegation pins the two facts the image's
// pixels cannot carry: its stored type (so a PNG is not saved as .jpg) and
// that a sub-agent cannot see it (so delegated work gets the id, not "the
// image the user sent").
func TestUserImageReferenceNamesTypeAndDelegation(t *testing.T) {
	t.Parallel()
	ref := userImageReference("att-9", "image/png")
	assert.Contains(t, ref, "attachment_id=att-9")
	assert.Contains(t, ref, "image/png")
	assert.Contains(t, ref, "save_attachment")
	assert.Contains(t, ref, "sub-agent")
}

func TestUserDocumentReferenceOffersSave(t *testing.T) {
	t.Parallel()
	id := "stored-doc"
	repo := &MockAttachmentReader{GetAttachmentFunc: func(_ context.Context, _ string) (*db.Attachment, error) {
		return &db.Attachment{ID: id, Filename: "spec.pdf", MimeType: "application/pdf", Content: []byte("%PDF-1.7")}, nil
	}}
	block := &db.MessageContentBlock{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT, Content: &id}
	part := ContentBlockToPart(context.Background(), "chat", block, repo)
	text, ok := part.(message.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, "read_attachment")
	assert.Contains(t, text.Text, "save_attachment")
	assert.Contains(t, text.Text, "attachment_id="+id)
}
