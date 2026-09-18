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
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
)

// Antigravity's access token is an opaque Google `ya29.` token, so unlike the
// Codex tests there is no JWT to mint: expiry is a stored timestamp.

// fakeAntigravityStore is an in-memory antigravityTokenStore.
type fakeAntigravityStore struct {
	mu       sync.Mutex
	tokens   *db.AntigravityAuthTokens
	casCalls int
}

func (s *fakeAntigravityStore) GetAntigravityAuthTokens(_ context.Context, _ string) (*db.AntigravityAuthTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		return nil, nil
	}
	copied := *s.tokens
	return &copied, nil
}

func (s *fakeAntigravityStore) CompareAndSwapAntigravityAuthTokens(_ context.Context, _ string, expectedRefreshToken string, tokens db.AntigravityAuthTokens) (bool, error) {
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

func (s *fakeAntigravityStore) setTokens(tokens db.AntigravityAuthTokens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := tokens
	s.tokens = &copied
}

func antigravityHeldFrom(tokens db.AntigravityAuthTokens) llm.OAuthTokens {
	return llm.OAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
	}
}

// TestAntigravityRefresh_SingleFlightCollapsesConcurrentRefreshes reproduces
// the production shape: N concurrent requests all see an expired access token.
// Exactly one upstream refresh must happen and every caller adopts its result.
func TestAntigravityRefresh_SingleFlightCollapsesConcurrentRefreshes(t *testing.T) {
	userID := "user-" + t.Name()
	old := db.AntigravityAuthTokens{
		AccessToken:  "ya29.old",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	store := &fakeAntigravityStore{}
	store.setTokens(old)

	var refreshCalls atomic.Int32
	refresh := func(refreshToken string) (*antigravity.AntigravityTokens, error) {
		refreshCalls.Add(1)
		if refreshToken != "rt-1" {
			return nil, fmt.Errorf("invalid_grant")
		}
		time.Sleep(50 * time.Millisecond) // widen the concurrency window
		return &antigravity.AntigravityTokens{
			AccessToken: "ya29.new",
			// Google does NOT return a refresh_token on refresh; the
			// normalize step carries the consumed one forward.
			RefreshToken: "rt-1",
			ExpiresAt:    time.Now().Add(time.Hour),
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
			results[i], errs[i] = coordinatedAntigravityRefresh(context.Background(), store, userID, antigravityHeldFrom(old), refresh)
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d", i)
		assert.Equal(t, "ya29.new", results[i].AccessToken, "goroutine %d must adopt the winner's token", i)
	}
	assert.Equal(t, int32(1), refreshCalls.Load(), "exactly one upstream refresh must happen")

	stored, err := store.GetAntigravityAuthTokens(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "ya29.new", stored.AccessToken)
	assert.Equal(t, "rt-1", stored.RefreshToken, "Google's non-rotating grant must survive the refresh")
}

// TestAntigravityRefresh_UsesStoredExpiry pins the difference from Codex: the
// returned expiry is the one the token endpoint reported (via expires_in),
// carried on the token struct — NOT parsed out of the access token, which is
// opaque and has no exp claim to read.
func TestAntigravityRefresh_UsesStoredExpiry(t *testing.T) {
	userID := "user-" + t.Name()
	old := db.AntigravityAuthTokens{
		AccessToken:  "ya29.old",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}
	wantExpiry := time.Now().Add(3 * time.Hour).Truncate(time.Second)

	store := &fakeAntigravityStore{}
	store.setTokens(old)

	refresh := func(string) (*antigravity.AntigravityTokens, error) {
		return &antigravity.AntigravityTokens{
			AccessToken:  "ya29.new",
			RefreshToken: "rt-1",
			ExpiresAt:    wantExpiry,
		}, nil
	}

	got, err := coordinatedAntigravityRefresh(context.Background(), store, userID, antigravityHeldFrom(old), refresh)
	require.NoError(t, err)
	assert.Equal(t, wantExpiry, got.ExpiresAt)
	assert.False(t, antigravity.IsTokenExpired(got.ExpiresAt), "a freshly refreshed token must not read as expired")
}

// TestAntigravityRefresh_AdoptsTokensRefreshedElsewhere: another process
// already refreshed, so this one must adopt the live persisted token rather
// than spend a second token call.
func TestAntigravityRefresh_AdoptsTokensRefreshedElsewhere(t *testing.T) {
	userID := "user-" + t.Name()
	held := llm.OAuthTokens{
		AccessToken:  "ya29.stale",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	store := &fakeAntigravityStore{}
	store.setTokens(db.AntigravityAuthTokens{
		AccessToken:  "ya29.fresh-from-other-process",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	})

	var refreshCalls atomic.Int32
	refresh := func(string) (*antigravity.AntigravityTokens, error) {
		refreshCalls.Add(1)
		return nil, fmt.Errorf("should not have been called")
	}

	got, err := coordinatedAntigravityRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err)
	assert.Equal(t, "ya29.fresh-from-other-process", got.AccessToken)
	assert.Equal(t, int32(0), refreshCalls.Load(), "a live persisted token must be adopted without an upstream call")
}

// TestAntigravityRefresh_DoesNotResurrectDisconnectedCredential is why the
// persistence is compare-and-swap rather than an upsert. If the user
// disconnects Antigravity while a refresh is in flight, the refreshed token
// must NOT be written back — that would silently re-create a credential the
// user just revoked.
func TestAntigravityRefresh_DoesNotResurrectDisconnectedCredential(t *testing.T) {
	userID := "user-" + t.Name()
	held := llm.OAuthTokens{
		AccessToken:  "ya29.old",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	// Empty store == the row was deleted by a disconnect.
	store := &fakeAntigravityStore{}

	refresh := func(string) (*antigravity.AntigravityTokens, error) {
		return &antigravity.AntigravityTokens{
			AccessToken:  "ya29.new",
			RefreshToken: "rt-1",
			ExpiresAt:    time.Now().Add(time.Hour),
		}, nil
	}

	got, err := coordinatedAntigravityRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err, "the in-flight request should still complete")
	assert.Equal(t, "ya29.new", got.AccessToken, "the caller uses the fresh token in memory")

	stored, err := store.GetAntigravityAuthTokens(context.Background(), userID)
	require.NoError(t, err)
	assert.Nil(t, stored, "a disconnected credential must not be written back to the store")
	assert.Equal(t, 1, store.casCalls, "persistence must go through compare-and-swap")
}

// TestAntigravityRefresh_NoRefreshTokenIsATerminalError: without a refresh
// token there is nothing to do, and the error must say so rather than the
// caller looping on a dead credential.
func TestAntigravityRefresh_NoRefreshTokenIsATerminalError(t *testing.T) {
	store := &fakeAntigravityStore{}
	_, err := coordinatedAntigravityRefresh(context.Background(), store, "user-none",
		llm.OAuthTokens{AccessToken: "ya29.old"}, func(string) (*antigravity.AntigravityTokens, error) {
			return nil, fmt.Errorf("should not be called")
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token available")
}
