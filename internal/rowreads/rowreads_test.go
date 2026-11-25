// Copyright (c) 2025 Reliant Labs
package rowreads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
)

// An empty id is refused before any query, as the facade read it replaced did.
func TestReadsRefuseAnEmptyID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	r := Of(repo)
	_, err := r.GetDaemon(context.Background(), "")
	require.EqualError(t, err, "daemon ID cannot be empty")
}

// A missing machine is NotFound, and a handle with no database fails every read.
func TestReadsMissingAndNoDatabase(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	_, err := Of(repo).GetDaemon(ctx, "no-such-daemon")
	require.True(t, svcerr.IsNotFound(err), "got %v", err)

	_, err = Reads{}.GetDaemon(ctx, "any")
	require.ErrorIs(t, err, ErrNoDatabase)
	_, err = WithRepo(nil).GetDaemon(ctx, "any")
	require.ErrorIs(t, err, ErrNoDatabase)
}

// The wrapped repository answers the machine read the facade no longer has.
func TestWithRepoReadsAMachine(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: "d1", UserID: "u1"}))

	got, err := WithRepo(repo).GetDaemon(ctx, "d1")
	require.NoError(t, err)
	require.Equal(t, "u1", got.UserID)
}
