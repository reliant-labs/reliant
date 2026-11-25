// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Records keeps the *db.Repo reads' empty-id guard: an empty id is refused
// before any query, so a request that names none never reads a row.
func TestRecordsRefuseAnEmptyID(t *testing.T) {
	r := NewRecords(nil)
	ctx := context.Background()
	_, err := r.GetDaemon(ctx, "")
	require.EqualError(t, err, "daemon ID cannot be empty")
	_, err = r.GetWorktree(ctx, "")
	require.EqualError(t, err, "worktree ID cannot be empty")
	_, err = r.GetProject(ctx, "")
	require.EqualError(t, err, "project ID cannot be empty")
	_, err = r.ListProjectDaemonsForProject(ctx, "")
	require.EqualError(t, err, "project ID cannot be empty")
}
