// Copyright (c) 2025 Reliant Labs
package triggertest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
)

// Create and Update write through the org-checked store: the insert records a
// "create" change, the replace an "update", and a nil trigger is refused.
func TestCreateAndUpdateRecordTheirChange(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	require.Error(t, triggertest.Create(ctx, env.Repo, nil))

	tr := &core.Trigger{ID: "tt-1", UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, Name: "tt", Kind: core.TriggerKindSchedule, Workflow: "nightly-audit", Message: "m"}
	require.NoError(t, triggertest.Create(ctx, env.Repo, tr))
	tr.Message = "edited"
	require.NoError(t, triggertest.Update(ctx, env.Repo, tr))

	got, err := store.GetTrigger(ctx, env.Repo.ORM(), "tt-1")
	require.NoError(t, err)
	require.Equal(t, "edited", got.Message)
	changes, err := store.TriggerChanges(ctx, env.Repo.ORM(), storetest.SeedUserID, "tt-1")
	require.NoError(t, err)
	ops := map[string]bool{}
	for _, c := range changes {
		ops[c.Operation] = true
	}
	require.True(t, ops["create"] && ops["update"], "changes: %+v", changes)
}
