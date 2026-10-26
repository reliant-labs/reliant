// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// launchConcurrently runs one launch per (user, project) pair at once and
// returns each launch's error.
func launchConcurrently(t *testing.T, launcher *Launcher, userIDs, projectIDs []string) []error {
	t.Helper()
	params := mockModelParams(t)
	start := make(chan struct{})
	errs := make([]error, len(userIDs))
	var wg sync.WaitGroup
	for i := range userIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = launchFor(context.Background(), launcher, params, userIDs[i], projectIDs[i])
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// Sixteen StartChats at once all succeed on their FIRST attempt — sixteen
// users, and sixteen chats of one user in one worktree. The launch used to be
// one SERIALIZABLE transaction whose reads (foreign-key checks, the root
// workflow's ON CONFLICT probe, the thread-exists check, the seed messages'
// numbering) took predicate locks on index pages every other launch inserted
// into, so launches that shared no row aborted one another with SQLSTATE
// 40001 ("failed to create workflow and thread: failed to create workflow",
// e2e story 02) until one exhausted its attempts. One user's launches also
// collided on rows they genuinely share — the worktree's activity_generation
// and the user's update sequence — which SERIALIZABLE refuses to wait on.
// Under READ COMMITTED those writes wait their turn, so no attempt is retried
// at all: counting retries, not timing them, is what keeps this exact on a
// loaded host.
func TestSixteenConcurrentStartChatsAllSucceedFirstTime(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: drives many concurrent DB-backed launches; skipped under -short")
	}
	t.Parallel()
	const launches = 16

	for _, tc := range []struct {
		name     string
		oneOwner bool
	}{
		{name: "distinct users"},
		{name: "one user, one worktree", oneOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)
			launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

			userIDs := make([]string, launches)
			projectIDs := make([]string, launches)
			for i := range userIDs {
				if tc.oneOwner && i > 0 {
					userIDs[i], projectIDs[i] = userIDs[0], projectIDs[0]
					continue
				}
				userIDs[i] = fmt.Sprintf("sixteen-launch-user-%d-%s", i, uuid.NewString()[:8])
				projectIDs[i] = seedLaunchUser(t, repo, userIDs[i])
			}

			retries := db.RecordRetryBackoffs(repo)
			errs := launchConcurrently(t, launcher, userIDs, projectIDs)
			for i, err := range errs {
				require.NoError(t, err, "launch %d of %d (user %s)", i+1, launches, userIDs[i])
			}
			assert.Zero(t, retries.Len(), "no launch transaction conflicted and had to be retried")
		})
	}
}

// A launch takes no predicate (SIREAD) locks, which is what makes the test
// above hold on any table size rather than on this one's. Postgres keeps a
// committed SERIALIZABLE transaction's SIREAD locks while a transaction that
// overlapped it is still open, so an observer opened before the launch sees
// in pg_locks every one the launch took — in its own transaction or in the
// status writes that follow it. pg_locks spans the server, so only this
// test's database counts.
func TestConcurrentStartChatsTakeNoPredicateLocks(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: DB-backed launch; skipped under -short")
	}
	t.Parallel()

	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
	ctx := context.Background()
	userID := "siread-user-" + uuid.NewString()[:8]
	projectID := seedLaunchUser(t, repo, userID)

	observer, err := raw.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = observer.Rollback() })
	_, err = observer.ExecContext(ctx, `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`)
	require.NoError(t, err)
	var observerPID int
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID))

	_, err = launchFor(ctx, launcher, mockModelParams(t), userID, projectID)
	require.NoError(t, err)

	rows, err := raw.QueryContext(ctx, `
SELECT locktype, coalesce(relation::regclass::text, ''), coalesce(page::text, '')
FROM pg_locks
WHERE mode = 'SIReadLock' AND coalesce(pid, 0) <> $1
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, observerPID)
	require.NoError(t, err)
	defer rows.Close()
	var held []string
	for rows.Next() {
		var lockType, relation, page string
		require.NoError(t, rows.Scan(&lockType, &relation, &page))
		held = append(held, fmt.Sprintf("%s %s %s", lockType, relation, page))
	}
	require.NoError(t, rows.Err())
	assert.Empty(t, held, "the launch's transaction took predicate locks another launch's inserts would conflict with")
}
