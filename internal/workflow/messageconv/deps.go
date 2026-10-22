// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies both.
package messageconv

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// AttachmentReader is what converting one content block reads: the
// attachment an image or file-reference block points at.
type AttachmentReader interface {
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
}

// Store is what converting whole messages reads: their content blocks and
// context windows, plus the attachments those blocks point at.
type Store interface {
	AttachmentReader
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*db.MessageContentBlock, error)
	GetContextWindow(ctx context.Context, id string) (*db.ContextWindow, error)
}
