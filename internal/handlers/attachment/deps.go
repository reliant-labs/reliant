// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package attachment

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the attachment handlers use.
type Repository interface {
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
	DeleteAttachment(ctx context.Context, id string) error
}
