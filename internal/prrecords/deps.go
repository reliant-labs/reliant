// forge:deps — consumer-declared interfaces for this package's Deps.
package prrecords

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Store is where records are kept. *db.Repo satisfies it.
type Store interface {
	ApplyPRRecordChange(ctx context.Context, c core.PRRecordChange) (bool, error)
}
