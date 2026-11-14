package store

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// SetRestampMovedTriggerFunding replaces the project move's trigger-funding
// seam (restampMovedTriggerFunding) for one test.
func SetRestampMovedTriggerFunding(t *testing.T, f func(ctx context.Context, db orm.Context, c core.ProjectOrgMoveCommit, move *core.ProjectOrgMove) error) {
	t.Helper()
	prev := restampMovedTriggerFunding
	restampMovedTriggerFunding = f
	t.Cleanup(func() { restampMovedTriggerFunding = prev })
}
