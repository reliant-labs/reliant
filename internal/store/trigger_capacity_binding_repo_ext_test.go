package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

// Exercise the Bun statement, not a rewritten copy: each neighboring scope
// differs in just one key, so dropping any scope predicate leaks an execution.
func TestListTriggerCapacityExecutionsScopeIsolation(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	scopes := []store.TriggerCapacityScope{
		scope,
		{UserID: "another-user", ProjectID: scope.ProjectID, WorkflowRef: scope.WorkflowRef},
		{UserID: scope.UserID, ProjectID: "another-project", WorkflowRef: scope.WorkflowRef},
		{UserID: scope.UserID, ProjectID: scope.ProjectID, WorkflowRef: "project://other"},
	}
	ids := []string{"occupied", "other-user", "other-project", "other-workflow"}
	for i, candidate := range scopes {
		ev := capacityEvent(ids[i])
		ev.UserID = candidate.UserID
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, candidate))
		claimed, err := claimCapacity(ctx, env, ev.ID, candidate, 10)
		require.NoError(t, err)
		require.True(t, claimed)
		require.NoError(t, store.BindTriggerCapacityExecution(ctx, env.ORM, ev.ID, "execution-"+ev.ID, "run-"+ev.ID))
	}

	// Released and pending events share all three scope keys but hold no slot.
	for _, id := range []string{"released", "unbound", "pending"} {
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, capacityEvent(id), scope))
		if id != "pending" {
			claimed, err := claimCapacity(ctx, env, id, scope, 10)
			require.NoError(t, err)
			require.True(t, claimed)
		}
	}
	_, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET capacity_released=true WHERE id=$1`, "released")
	require.NoError(t, err)

	for i, candidate := range scopes {
		rows, err := store.ListTriggerCapacityExecutions(ctx, env.ORM, candidate)
		require.NoError(t, err)
		wantIDs := []string{ids[i]}
		if i == 0 {
			wantIDs = append(wantIDs, "unbound")
		}
		var gotIDs []string
		for _, row := range rows {
			gotIDs = append(gotIDs, row.EventID)
		}
		require.Equal(t, wantIDs, gotIDs, "scope %+v must return only its occupied slots in enrollment order", candidate)
		require.Equal(t, "execution-"+ids[i], rows[0].WorkflowID)
		require.Equal(t, "run-"+ids[i], rows[0].FirstRunID)
		require.NotNil(t, rows[0].BoundAt)
		if i == 0 {
			require.Empty(t, rows[1].WorkflowID)
			require.Empty(t, rows[1].FirstRunID)
			require.Nil(t, rows[1].BoundAt)
		}
	}
}
