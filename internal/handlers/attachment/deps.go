// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. store.AttachmentExtStore
// satisfies AttachmentStore.
package attachment

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// AttachmentStore stores new attachments. store.AttachmentExtStore satisfies
// it. Get and Delete run on Deps.DB through the generated ops.
type AttachmentStore interface {
	InsertAttachment(ctx context.Context, a *core.Attachment) error
}

// ContentReader reads an attachment for AttachmentContentHandler. It is the
// one method of AttachmentStore that route needs.
type ContentReader interface {
	GetAttachment(ctx context.Context, id string) (*core.Attachment, error)
}

// OrgResolver verifies the org an upload names. *orgs.Resolver is one.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
}
