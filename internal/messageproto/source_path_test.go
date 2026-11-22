// Copyright (c) 2025 Reliant Labs
package messageproto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
)

// An image present_image showed carries the file it was read from to the
// client (Attachment.source_path), which opens it in the file viewer. No
// other attachment does: their FilePath is a storage name, not a path on the
// user's machine.
func TestFromDB_SourcePathOnlyForPresentedImages(t *testing.T) {
	t.Parallel()

	attachments := []*db.Attachment{
		{ID: "shown", Filename: "shot.png", MimeType: "image/png", FilePath: "/tmp/shot.png", AttachmentType: string(attachment.TypeDisplay)},
		{ID: "gen", Filename: "g.png", MimeType: "image/png", FilePath: "user/g.png", AttachmentType: string(attachment.TypeImage)},
	}
	got := FromDB(userMessage("m1", nil, nil), nil, attachments, nil).Attachments
	require.Len(t, got, 2)
	require.NotNil(t, got[0].SourcePath)
	assert.Equal(t, "/tmp/shot.png", *got[0].SourcePath)
	assert.Nil(t, got[1].SourcePath)
}
