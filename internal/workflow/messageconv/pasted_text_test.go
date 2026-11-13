package messageconv

import (
	"context"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// A paste is an ordinary uploaded .txt file. Its full contents must reach the
// model in the user turn, without a filesystem read or a read_attachment call.
func TestPastedTextReachesModelUnchanged(t *testing.T) {
	t.Parallel()
	text := "  Task instructions\r\n\t你好 👋\r\n```quoted code```\n" + strings.Repeat("long log line\r\n", 3000) + "  trailing whitespace  \n"
	id := "paste-attachment"
	repo := &MockStore{
		ListContentBlocksForMessagesFunc: func(context.Context, []string) ([]*db.MessageContentBlock, error) {
			return []*db.MessageContentBlock{{MessageID: "user-turn", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE, Content: &id}}, nil
		},
		GetContextWindowFunc: func(context.Context, string) (*db.ContextWindow, error) { return &db.ContextWindow{}, nil },
		GetAttachmentFunc: func(_ context.Context, attachmentID string) (*db.Attachment, error) {
			require.Equal(t, id, attachmentID)
			return &db.Attachment{ID: id, Filename: "Pasted text.txt", MimeType: "text/plain", Content: []byte(text)}, nil
		},
	}
	got, err := DbMessagesToMessages(context.Background(), []*db.Message{
		{ID: "user-turn", ChatID: "chat", Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, ContextWindowID: "cw"},
	}, repo)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[0].Parts, 1)
	part, ok := got[0].Parts[0].(message.TextContent)
	require.True(t, ok, "pasted text must be inline model context, not a tool-readable pointer")
	require.Equal(t, "Contents of Pasted text.txt:\n```\n"+text+"\n```", part.Text)
}
