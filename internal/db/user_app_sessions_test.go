package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// A user's first session is reported once, whichever of several concurrent
// callers (API replicas) gets there first; every later session is not first.
func TestRecordFirstAppSessionIsTrueExactlyOnce(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	var firsts atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first, err := repo.RecordFirstAppSession(ctx, "user-first-session")
			require.NoError(t, err)
			if first {
				firsts.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), firsts.Load())

	first, err := repo.RecordFirstAppSession(ctx, "user-first-session")
	require.NoError(t, err)
	require.False(t, first)
	first, err = repo.RecordFirstAppSession(ctx, "another-user")
	require.NoError(t, err)
	require.True(t, first)

	_, err = repo.RecordFirstAppSession(ctx, "")
	require.Error(t, err)
}
