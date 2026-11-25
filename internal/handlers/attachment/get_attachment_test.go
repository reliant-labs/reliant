// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// Reading an attachment back returns the same usable url Upload and
// CreateFileReference handed out, plus its bytes. The url has no column, so
// the generated projection leaves it empty; GetAttachment's Pack fills it.
func TestGetAttachmentReturnsItsURLAndBytes(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	now := viewertest.SeededAt
	att := &db.Attachment{
		ID: "att-url-" + f.ChatID, UserID: f.OwnerID, Filename: "notes.txt", Size: 5, MimeType: "text/plain",
		AttachmentType: "file", Content: []byte("hello"), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateAttachment(f.Owner, att))

	svc := NewTestAttachment(t, WithAttachmentDeps(RealDeps(repo)))
	got, err := svc.GetAttachment(f.Owner, connect.NewRequest(&reliantv1.GetAttachmentRequest{Id: att.ID}))
	require.NoError(t, err)

	assert.Equal(t, "/api/attachments/"+att.ID, got.Msg.GetAttachment().GetUrl())
	assert.Equal(t, attachmentToWire(att).GetUrl(), got.Msg.GetAttachment().GetUrl(), "Get returns the url Upload returns")
	assert.Equal(t, att.Content, got.Msg.GetContent())
	assert.Equal(t, att.ID, got.Msg.GetAttachment().GetId())
}
