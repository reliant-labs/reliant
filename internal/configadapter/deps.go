// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package configadapter

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ConfigRecordReader is the stored project config record NewRepoConfigStore
// adapts to config.StoredConfigStore. NewORMRecordReader is the database's.
type ConfigRecordReader interface {
	GetProjectConfigRecord(ctx context.Context, projectID string) (*core.ProjectConfigRecord, error)
	GetProjectMachineConfigRecord(ctx context.Context, projectID, daemonID string) (*core.ProjectConfigRecord, bool, error)
}
