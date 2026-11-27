// Copyright (c) 2025 Reliant Labs
package dblock_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/dblock"
)

func newLocker(t *testing.T, sessions dblock.Sessions) dblock.Service {
	t.Helper()
	l, err := dblock.New(dblock.Deps{Sessions: sessions})
	require.NoError(t, err)
	return l
}

// Exactly one holder at a time: a second locker over the same database does
// not get the lock while the first holds it, and gets it the moment the first
// releases.
func TestLockService_OneHolderAtATime(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	// A key no production job uses, so parallel packages cannot collide with it.
	key := dblock.Key(0x7465_7374_0000_0001)

	first := newLocker(t, repo.SessionPool())
	second := newLocker(t, repo.SessionPool())

	release, ok, err := first.TryLock(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "an unheld lock is taken")

	_, ok, err = second.TryLock(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "a held lock is not taken by a second replica")

	release()

	release2, ok, err := second.TryLock(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "a released lock is taken again")
	release2()
}

// A named lock is one per name: a second holder of the same name is refused,
// another name is its own lock.
func TestLockService_NamedLocks(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	first := newLocker(t, repo.SessionPool())
	second := newLocker(t, repo.SessionPool())

	release, ok, err := first.TryLockNamed(ctx, "test:one")
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = second.TryLockNamed(ctx, "test:one")
	require.NoError(t, err)
	assert.False(t, ok, "a held name is not taken twice")
	other, ok, err := second.TryLockNamed(ctx, "test:two")
	require.NoError(t, err)
	assert.True(t, ok, "another name is another lock")
	other()
	release()
	again, ok, err := second.TryLockNamed(ctx, "test:one")
	require.NoError(t, err)
	require.True(t, ok, "a released name is taken again")
	again()
}

// A lock that was not taken returns a release that is safe to call, so a
// caller can defer it unconditionally.
func TestLockService_ReleaseOfAnUntakenLockIsANoOp(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	key := dblock.Key(0x7465_7374_0000_0002)
	l := newLocker(t, repo.SessionPool())

	holder, ok, err := l.TryLock(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	defer holder()

	release, ok, err := l.TryLock(ctx, key)
	require.NoError(t, err)
	require.False(t, ok)
	require.NotNil(t, release)
	release()

	// The holder still has it.
	_, ok, err = l.TryLock(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "releasing an untaken lock did not free the holder's")
}

// The keys are production's: an overlapping rolling deploy elects one leader
// across old and new replicas only if they never change.
func TestKeys_AreTheOnesProductionAlreadyHolds(t *testing.T) {
	t.Parallel()

	assert.EqualValues(t, int64(0x7265_6c69_616e_7401), dblock.WorktreeSweep)
	assert.EqualValues(t, int64(0x7265_6c69_616e_7402), dblock.AlertReceivers)
	assert.EqualValues(t, int64(0x7265_6c69_616e_7403), dblock.ChatUpdateHeads)
	assert.NotEqual(t, dblock.WorktreeSweep, dblock.AlertReceivers)
	assert.NotEqual(t, dblock.AlertReceivers, dblock.ChatUpdateHeads)
}

type failingSessions struct{ err error }

func (f failingSessions) Conn(context.Context) (*sql.Conn, error) { return nil, f.err }

func TestLockService_ReportsAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	boom := errors.New("no connection")
	_, ok, err := newLocker(t, failingSessions{err: boom}).TryLock(context.Background(), dblock.AlertReceivers)
	require.ErrorIs(t, err, boom)
	assert.False(t, ok)
}

func TestNew_RefusesToBuildWithoutASessionPool(t *testing.T) {
	t.Parallel()

	_, err := dblock.New(dblock.Deps{})
	require.Error(t, err, "a locker with no pool would take no lock at all, silently")
}
