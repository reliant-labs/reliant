// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. store.AttachmentExtStore
// satisfies AttachmentStore.
package attachment

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// AttachmentStore stores attachments and their bytes.
type AttachmentStore interface {
	InsertAttachment(ctx context.Context, a *core.Attachment) error
	GetAttachment(ctx context.Context, id string) (*core.Attachment, error)
	RemoveAttachment(ctx context.Context, id string) error
	// AttachedInChatOwnedBy reports whether a chat member attached the
	// attachment to their own message in a chat ownerID owns
	// (chat-collaboration.md §6.4).
	AttachedInChatOwnedBy(ctx context.Context, id, ownerID string) (bool, error)
}

// ContentReader reads an attachment for AttachmentContentHandler: the two
// methods of AttachmentStore that route needs.
type ContentReader interface {
	GetAttachment(ctx context.Context, id string) (*core.Attachment, error)
	AttachedInChatOwnedBy(ctx context.Context, id, ownerID string) (bool, error)
}
