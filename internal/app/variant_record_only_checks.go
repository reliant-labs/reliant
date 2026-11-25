// yours: scaffolded once by `forge scaffold variant`, never touched again
package app

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

// RecordOnlyChecks is the record-only configuration of workflowcheck.Service:
// no GitHub, so it only records what the fire and run activities report
// (Open, Started, Ended). The worker's Temporal worker registers those
// activities over it; SyncingChecks writes GitHub. The composition graph
// identifies a node by its Go type, so each configuration is a type of its
// own (`forge skill load architecture/graph-checks`).
type RecordOnlyChecks struct{ workflowcheck.Service }

// RecordOnlyChecksDeps is where the checks are recorded.
type RecordOnlyChecksDeps struct {
	DB *db.Repo
}

// NewRecordOnlyChecks builds the record-only workflowcheck.Service.
//
// Root-only: no other root may reach it, and it never fills an interface
// field it merely satisfies.
//
//forge:root-only worker
func NewRecordOnlyChecks(d RecordOnlyChecksDeps) (*RecordOnlyChecks, error) {
	return &RecordOnlyChecks{Service: workflowcheck.NewInstrumented(workflowcheck.Deps{
		Store: store.NewWorkflowCheckRunExtStore(d.DB.ORM()),
	})}, nil
}
