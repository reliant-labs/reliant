// Copyright (c) 2025 Reliant Labs
package rowreads

import (
	"context"
	"reflect"
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

// Every consumer that was handed a *db.Repo and is now handed a Repo chooses
// optional roles with interface assertions (store.(Iface)). Those answer the
// same as before exactly when the wrapper's method set holds every method of
// *db.Repo with the same signature, so this is the whole proof: the wrapper
// loses nothing, and what it adds is the reads the facade gave up.
func TestRepoKeepsTheWholeFacadeMethodSet(t *testing.T) {
	facade := reflect.ValueOf(&db.Repo{})
	wrapped := reflect.ValueOf(Repo{})
	ft := facade.Type()
	for i := 0; i < ft.NumMethod(); i++ {
		name := ft.Method(i).Name
		w := wrapped.MethodByName(name)
		require.True(t, w.IsValid(), "rowreads.Repo lost the facade method %s", name)
		require.Equal(t, facade.Method(i).Type(), w.Type(), "rowreads.Repo changed the signature of %s", name)
	}
	for _, name := range []string{"GetDaemon"} {
		require.True(t, wrapped.MethodByName(name).IsValid(), "rowreads.Repo has no %s", name)
		require.False(t, facade.MethodByName(name).IsValid(), "the facade still has %s: delete it", name)
	}
}
