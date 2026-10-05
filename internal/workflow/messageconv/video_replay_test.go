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

type attachmentRepo struct {
	db.Repository
	atts map[string]*db.Attachment
}

func (r *attachmentRepo) GetAttachment(_ context.Context, id string) (*db.Attachment, error) {
	return r.atts[id], nil
}

// A generated video lives in an IMAGE-typed block but must never be replayed to
// a model: no routed chat model accepts it and the bytes would inflate every
// later request.
func TestContentBlockToPart_VideoIsNotReplayedToTheModel(t *testing.T) {
	repo := &attachmentRepo{atts: map[string]*db.Attachment{
		"vid": {ID: "vid", Filename: "g.mp4", MimeType: "video/mp4", Content: []byte("mp4")},
		"img": {ID: "img", Filename: "g.png", MimeType: "image/png", Content: []byte("png")},
	}}
	block := func(id string) *db.MessageContentBlock {
		return &db.MessageContentBlock{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: &id}
	}

	assert.Nil(t, ContentBlockToPart(context.Background(), "chat", block("vid"), repo))

	part := ContentBlockToPart(context.Background(), "chat", block("img"), repo)
	bin, ok := part.(message.BinaryContent)
	require.True(t, ok, "images still replay")
	assert.Equal(t, "image/png", bin.MIMEType)
}
