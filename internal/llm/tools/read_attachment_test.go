// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReadAttachmentCtx() *rctx.ToolContext {
	return rctx.NewToolContext(context.Background(), "test-chat", "0", nil, nil)
}

func TestReadAttachmentReadPDF(t *testing.T) {
	t.Parallel()
	tool := &ReadAttachmentTool{}

	t.Run("small PDF returns whole document block", func(t *testing.T) {
		pdf := buildTestPDF(3)
		att := &db.Attachment{Filename: "small.pdf", Content: pdf}
		resp, err := tool.readPDF(att, "")
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, ToolResponseTypeImage, resp.Type)
		assert.Contains(t, resp.Content, "3 pages")
		require.Len(t, resp.BinaryParts, 1)
		assert.Equal(t, "application/pdf", resp.BinaryParts[0].MIMEType)
		assert.Equal(t, pdf, resp.BinaryParts[0].Data)
	})

	t.Run("large PDF without pages prompts for a range", func(t *testing.T) {
		att := &db.Attachment{Filename: "large.pdf", Content: buildTestPDF(PDFAutoInlinePageLimit + 5)}
		resp, err := tool.readPDF(att, "")
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, ToolResponseTypeText, resp.Type)
		assert.Empty(t, resp.BinaryParts)
		assert.Contains(t, resp.Content, "pages")
		assert.Contains(t, resp.Content, "read_attachment")
	})

	t.Run("large PDF with pages returns that range", func(t *testing.T) {
		att := &db.Attachment{Filename: "large.pdf", Content: buildTestPDF(PDFAutoInlinePageLimit + 5)}
		resp, err := tool.readPDF(att, "2-4")
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, ToolResponseTypeImage, resp.Type)
		require.Len(t, resp.BinaryParts, 1)
		assert.Equal(t, "application/pdf", resp.BinaryParts[0].MIMEType)
		assert.Contains(t, resp.Content, "pages 2-4")
	})

	t.Run("invalid page range surfaces an error response", func(t *testing.T) {
		att := &db.Attachment{Filename: "small.pdf", Content: buildTestPDF(2)}
		resp, err := tool.readPDF(att, "50-60")
		require.NoError(t, err)
		assert.True(t, resp.IsError)
	})
}

func TestReadAttachmentGuards(t *testing.T) {
	t.Parallel()
	tool := &ReadAttachmentTool{} // nil repo

	resp, err := tool.Execute(newReadAttachmentCtx(), ReadAttachmentParams{AttachmentID: "x"})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "nil repo should produce an error response")
}
