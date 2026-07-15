// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
)

// fakeClaudeStore is an in-memory claudeTokenStore with hooks to simulate
// another process mutating the row between reads.
type fakeClaudeStore struct {
	mu       sync.Mutex
	tokens   *db.ClaudeAuthTokens
	getCalls int
	casCalls int
	// onGet, if set, runs (with the 1-based read count) before each read so
	// tests can simulate a concurrent process rotating the stored tokens.
	onGet func(callNum int, s *fakeClaudeStore)
}

func (s *fakeClaudeStore) GetClaudeAuthTokens(_ context.Context, _ string) (*db.ClaudeAuthTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.onGet != nil {
		s.onGet(s.getCalls, s)
	}
	if s.tokens == nil {
		return nil, nil
	}
	copied := *s.tokens
	return &copied, nil
}

func (s *fakeClaudeStore) CompareAndSwapClaudeAuthTokens(_ context.Context, _ string, expectedRefreshToken string, tokens db.ClaudeAuthTokens) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.casCalls++
	if s.tokens == nil || s.tokens.RefreshToken != expectedRefreshToken {
		return false, nil
	}
	copied := tokens
	s.tokens = &copied
	return true, nil
}

// setTokens replaces the stored tokens (thread-safe), simulating another
// process persisting a rotation.
func (s *fakeClaudeStore) setTokens(tokens db.ClaudeAuthTokens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := tokens
	s.tokens = &copied
}

func expiredClaudeTokens(access, refresh string) db.ClaudeAuthTokens {
	return db.ClaudeAuthTokens{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    time.Now().Add(-time.Hour),
	}
}

func liveClaudeTokens(access, refresh string) db.ClaudeAuthTokens {
	return db.ClaudeAuthTokens{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    time.Now().Add(8 * time.Hour),
	}
}

func heldFrom(tokens db.ClaudeAuthTokens) llm.OAuthTokens {
	return llm.OAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
	}
}

// TestClaudeRefresh_SingleFlightCollapsesConcurrentRefreshes reproduces the
// production failure: N concurrent requests all see an expired access token
// and try to refresh with the same single-use refresh token. With
// coordination, exactly one upstream refresh happens and every caller adopts
// the winner's tokens.
func TestClaudeRefresh_SingleFlightCollapsesConcurrentRefreshes(t *testing.T) {
	userID := "user-" + t.Name()
	old := expiredClaudeTokens("old-access", "rt-1")
	store := &fakeClaudeStore{}
	store.setTokens(old)

	var refreshCalls atomic.Int32
	refresh := func(refreshToken string) (*claude.ClaudeTokens, error) {
		refreshCalls.Add(1)
		if refreshToken != "rt-1" {
			return nil, fmt.Errorf("claude session expired: please reconnect Claude")
		}
		time.Sleep(50 * time.Millisecond) // widen the concurrency window
		return &claude.ClaudeTokens{
			AccessToken:  "new-access",
			RefreshToken: "rt-2",
			ExpiresAt:    time.Now().Add(8 * time.Hour),
		}, nil
	}

	const goroutines = 8
	results := make([]llm.OAuthTokens, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = coordinatedClaudeRefresh(context.Background(), store, userID, heldFrom(old), refresh)
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d", i)
		assert.Equal(t, "new-access", results[i].AccessToken, "goroutine %d must adopt the winner's token", i)
	}
	assert.Equal(t, int32(1), refreshCalls.Load(), "exactly one upstream refresh must happen")

	stored, err := store.GetClaudeAuthTokens(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "new-access", stored.AccessToken)
	assert.Equal(t, "rt-2", stored.RefreshToken, "rotation must be persisted")
}

// TestClaudeRefresh_AdoptsTokensRotatedByAnotherProcess simulates the losing
// process: by the time it decides to refresh, the store already holds a newer
// live rotation (persisted by the other process). It must adopt those tokens
// without consuming the refresh token at all.
func TestClaudeRefresh_AdoptsTokensRotatedByAnotherProcess(t *testing.T) {
	userID := "user-" + t.Name()
	held := heldFrom(expiredClaudeTokens("old-access", "rt-1"))
	store := &fakeClaudeStore{}
	store.setTokens(liveClaudeTokens("rotated-access", "rt-2"))

	refresh := func(string) (*claude.ClaudeTokens, error) {
		t.Error("refresh must not be called when the store already has live rotated tokens")
		return nil, fmt.Errorf("unexpected refresh")
	}

	got, err := coordinatedClaudeRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err)
	assert.Equal(t, "rotated-access", got.AccessToken)
	assert.Equal(t, "rt-2", got.RefreshToken)
	assert.Equal(t, 0, store.casCalls, "no persistence needed when adopting")
}

// TestClaudeRefresh_RefreshFailureAdoptsConcurrentRotation covers the exact
// production race: our refresh fails with "session expired" because another
// process consumed the refresh token in the window between our store re-read
// and the refresh call. The failure re-reads the store, finds the winner's
// tokens, and adopts them instead of surfacing the terminal error.
func TestClaudeRefresh_RefreshFailureAdoptsConcurrentRotation(t *testing.T) {
	userID := "user-" + t.Name()
	old := expiredClaudeTokens("old-access", "rt-1")
	held := heldFrom(old)
	store := &fakeClaudeStore{}
	store.setTokens(old)
	rotated := liveClaudeTokens("winner-access", "rt-2")
	store.onGet = func(callNum int, s *fakeClaudeStore) {
		if callNum >= 2 {
			// The other process's rotation lands after our first read.
			copied := rotated
			s.tokens = &copied
		}
	}

	var refreshCalls atomic.Int32
	refresh := func(string) (*claude.ClaudeTokens, error) {
		refreshCalls.Add(1)
		return nil, fmt.Errorf("claude session expired: please reconnect Claude")
	}

	got, err := coordinatedClaudeRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err, "must adopt the concurrent rotation, not surface the refresh failure")
	assert.Equal(t, "winner-access", got.AccessToken)
	assert.Equal(t, "rt-2", got.RefreshToken)
	assert.Equal(t, int32(1), refreshCalls.Load())
}

// TestClaudeRefresh_CASLoserAdoptsPersistedTokens: our refresh succeeds, but a
// concurrent rotation was persisted first, so the compare-and-swap loses. The
// persisted lineage (whose refresh token is the one that will work next time)
// must win over our in-memory rotation.
func TestClaudeRefresh_CASLoserAdoptsPersistedTokens(t *testing.T) {
	userID := "user-" + t.Name()
	old := expiredClaudeTokens("old-access", "rt-1")
	held := heldFrom(old)
	store := &fakeClaudeStore{}
	store.setTokens(old)

	refresh := func(string) (*claude.ClaudeTokens, error) {
		// The concurrent rotation lands while our refresh is in flight, i.e.
		// between our pre-refresh re-read and the CAS persist.
		store.setTokens(liveClaudeTokens("their-access", "rt-theirs"))
		return &claude.ClaudeTokens{
			AccessToken:  "my-access",
			RefreshToken: "rt-mine",
			ExpiresAt:    time.Now().Add(8 * time.Hour),
		}, nil
	}

	got, err := coordinatedClaudeRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err)
	assert.Equal(t, "their-access", got.AccessToken, "the persisted lineage must win")
	assert.Equal(t, "rt-theirs", got.RefreshToken)

	stored, err := store.GetClaudeAuthTokens(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "rt-theirs", stored.RefreshToken, "CAS must not clobber the concurrent rotation")
}

// TestClaudeRefresh_GenuinelyDeadSessionErrors: refresh fails and the store
// has nothing newer — the "please reconnect Claude" failure must still
// surface.
func TestClaudeRefresh_GenuinelyDeadSessionErrors(t *testing.T) {
	userID := "user-" + t.Name()
	old := expiredClaudeTokens("old-access", "rt-1")
	store := &fakeClaudeStore{}
	store.setTokens(old)

	refresh := func(string) (*claude.ClaudeTokens, error) {
		return nil, fmt.Errorf("claude session expired: please reconnect Claude")
	}

	_, err := coordinatedClaudeRefresh(context.Background(), store, userID, heldFrom(old), refresh)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude token refresh failed")
	assert.Contains(t, err.Error(), "claude session expired: please reconnect Claude")
}

// TestClaudeRefresh_ConsumesPersistedRefreshTokenLineage: when the store holds
// a different (newer) refresh token than the caller — e.g. a rotation happened
// but its access token is already inside the expiry buffer again — the refresh
// must consume the persisted refresh token, not the caller's stale copy.
func TestClaudeRefresh_ConsumesPersistedRefreshTokenLineage(t *testing.T) {
	userID := "user-" + t.Name()
	held := heldFrom(expiredClaudeTokens("old-access", "rt-stale"))
	store := &fakeClaudeStore{}
	store.setTokens(expiredClaudeTokens("old-access", "rt-current"))

	var consumed atomic.Value
	refresh := func(refreshToken string) (*claude.ClaudeTokens, error) {
		consumed.Store(refreshToken)
		return &claude.ClaudeTokens{
			AccessToken:  "new-access",
			RefreshToken: "rt-next",
			ExpiresAt:    time.Now().Add(8 * time.Hour),
		}, nil
	}

	got, err := coordinatedClaudeRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err)
	assert.Equal(t, "rt-current", consumed.Load(), "must consume the persisted refresh token lineage")
	assert.Equal(t, "new-access", got.AccessToken)

	stored, err := store.GetClaudeAuthTokens(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "rt-next", stored.RefreshToken, "CAS keyed on the consumed token must persist")
}
