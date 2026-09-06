// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
)

// Codex access tokens are JWTs whose exp claim IS the expiry — there is no
// expires_at column — so these tests mint real (unsigned) JWTs rather than
// setting a timestamp field. Nothing in the refresh path verifies the
// signature; it only decodes the payload.
func mintCodexJWT(t *testing.T, accountID string, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"exp": expiry.Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
	require.NoError(t, err)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func expiredCodexJWT(t *testing.T, accountID string) string {
	t.Helper()
	return mintCodexJWT(t, accountID, time.Now().Add(-time.Hour))
}

func liveCodexJWT(t *testing.T, accountID string) string {
	t.Helper()
	return mintCodexJWT(t, accountID, time.Now().Add(8*time.Hour))
}

// fakeCodexStore is an in-memory codexTokenStore with hooks to simulate
// another process mutating the row between reads.
type fakeCodexStore struct {
	mu       sync.Mutex
	tokens   *db.CodexAuthTokens
	getCalls int
	casCalls int
	// onGet, if set, runs (with the 1-based read count) before each read so
	// tests can simulate a concurrent process rotating the stored tokens.
	onGet func(callNum int, s *fakeCodexStore)
}

func (s *fakeCodexStore) GetCodexAuthTokens(_ context.Context, _ string) (*db.CodexAuthTokens, error) {
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

func (s *fakeCodexStore) CompareAndSwapCodexAuthTokens(_ context.Context, _ string, expectedRefreshToken string, tokens db.CodexAuthTokens) (bool, error) {
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

func (s *fakeCodexStore) setTokens(tokens db.CodexAuthTokens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := tokens
	s.tokens = &copied
}

func codexHeldFrom(tokens db.CodexAuthTokens) llm.OAuthTokens {
	return llm.OAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    codexTokenExpiry(tokens.AccessToken),
	}
}

// TestCodexRefresh_SingleFlightCollapsesConcurrentRefreshes reproduces the
// production shape: N concurrent requests all see an expired access token and
// try to refresh with the same single-use refresh token. With coordination,
// exactly one upstream refresh happens and every caller adopts the winner's
// tokens.
func TestCodexRefresh_SingleFlightCollapsesConcurrentRefreshes(t *testing.T) {
	userID := "user-" + t.Name()
	oldAccess := expiredCodexJWT(t, "acct-1")
	newAccess := liveCodexJWT(t, "acct-1")
	old := db.CodexAuthTokens{AccessToken: oldAccess, RefreshToken: "rt-1", AccountID: "acct-1"}

	store := &fakeCodexStore{}
	store.setTokens(old)

	var refreshCalls atomic.Int32
	refresh := func(refreshToken string) (*codex.CodexTokens, error) {
		refreshCalls.Add(1)
		if refreshToken != "rt-1" {
			return nil, fmt.Errorf("codex session expired: please reconnect Codex")
		}
		time.Sleep(50 * time.Millisecond) // widen the concurrency window
		return &codex.CodexTokens{
			AccessToken:  newAccess,
			RefreshToken: "rt-2",
			AccountID:    "acct-1",
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
			results[i], errs[i] = coordinatedCodexRefresh(context.Background(), store, userID, codexHeldFrom(old), refresh)
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d", i)
		assert.Equal(t, newAccess, results[i].AccessToken, "goroutine %d must adopt the winner's token", i)
	}
	assert.Equal(t, int32(1), refreshCalls.Load(), "exactly one upstream refresh must happen")

	stored, err := store.GetCodexAuthTokens(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, newAccess, stored.AccessToken)
	assert.Equal(t, "rt-2", stored.RefreshToken, "rotation must be persisted")
}

// TestCodexRefresh_DerivesExpiryFromJWT pins the Codex-specific difference
// from Claude: the returned expiry comes from the access token's exp claim,
// because the store has no expires_at column to read it from.
func TestCodexRefresh_DerivesExpiryFromJWT(t *testing.T) {
	userID := "user-" + t.Name()
	oldAccess := expiredCodexJWT(t, "acct-1")
	wantExpiry := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	newAccess := mintCodexJWT(t, "acct-1", wantExpiry)
	old := db.CodexAuthTokens{AccessToken: oldAccess, RefreshToken: "rt-1", AccountID: "acct-1"}

	store := &fakeCodexStore{}
	store.setTokens(old)

	refresh := func(string) (*codex.CodexTokens, error) {
		return &codex.CodexTokens{AccessToken: newAccess, RefreshToken: "rt-2", AccountID: "acct-1"}, nil
	}

	got, err := coordinatedCodexRefresh(context.Background(), store, userID, codexHeldFrom(old), refresh)
	require.NoError(t, err)
	assert.Equal(t, wantExpiry.Unix(), got.ExpiresAt.Unix(),
		"expiry must be derived from the new access token's exp claim")
	assert.False(t, codex.IsTokenExpired(got.AccessToken), "refreshed token must not read as expired")
}

// TestCodexRefresh_AdoptsTokensRotatedByAnotherProcess simulates the losing
// process: by the time it decides to refresh, the store already holds a newer
// live rotation. It must adopt those tokens without consuming the single-use
// refresh token at all.
func TestCodexRefresh_AdoptsTokensRotatedByAnotherProcess(t *testing.T) {
	userID := "user-" + t.Name()
	held := codexHeldFrom(db.CodexAuthTokens{AccessToken: expiredCodexJWT(t, "acct-1"), RefreshToken: "rt-1"})

	rotatedAccess := liveCodexJWT(t, "acct-1")
	store := &fakeCodexStore{}
	store.setTokens(db.CodexAuthTokens{AccessToken: rotatedAccess, RefreshToken: "rt-2", AccountID: "acct-1"})

	refresh := func(string) (*codex.CodexTokens, error) {
		t.Error("refresh must not be called when the store already has live rotated tokens")
		return nil, fmt.Errorf("unexpected refresh")
	}

	got, err := coordinatedCodexRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err)
	assert.Equal(t, rotatedAccess, got.AccessToken)
	assert.Equal(t, "rt-2", got.RefreshToken)
	assert.Equal(t, 0, store.casCalls, "no persistence needed when adopting")
}

// TestCodexRefresh_RefreshFailureAdoptsConcurrentRotation covers the race the
// CAS exists for: our refresh fails with "session expired" because another
// process consumed the refresh token between our store re-read and the
// exchange. The failure re-reads the store, finds the winner's tokens, and
// adopts them instead of surfacing a terminal reconnect error.
func TestCodexRefresh_RefreshFailureAdoptsConcurrentRotation(t *testing.T) {
	userID := "user-" + t.Name()
	old := db.CodexAuthTokens{AccessToken: expiredCodexJWT(t, "acct-1"), RefreshToken: "rt-1"}
	held := codexHeldFrom(old)

	store := &fakeCodexStore{}
	store.setTokens(old)
	rotated := db.CodexAuthTokens{AccessToken: liveCodexJWT(t, "acct-1"), RefreshToken: "rt-2", AccountID: "acct-1"}
	store.onGet = func(callNum int, s *fakeCodexStore) {
		if callNum >= 2 {
			// The other process's rotation lands after our first read.
			copied := rotated
			s.tokens = &copied
		}
	}

	var refreshCalls atomic.Int32
	refresh := func(string) (*codex.CodexTokens, error) {
		refreshCalls.Add(1)
		return nil, fmt.Errorf("codex session expired: please reconnect Codex")
	}

	got, err := coordinatedCodexRefresh(context.Background(), store, userID, held, refresh)
	require.NoError(t, err, "must adopt the concurrent rotation, not surface the refresh failure")
	assert.Equal(t, rotated.AccessToken, got.AccessToken)
	assert.Equal(t, "rt-2", got.RefreshToken)
	assert.Equal(t, int32(1), refreshCalls.Load())
}

// TestCodexRefresh_DeadSessionSurfacesReconnectError is the other side: when
// the refresh genuinely fails and the store has nothing newer, the error must
// reach the caller so the UI can tell the user to reconnect Codex.
func TestCodexRefresh_DeadSessionSurfacesReconnectError(t *testing.T) {
	userID := "user-" + t.Name()
	old := db.CodexAuthTokens{AccessToken: expiredCodexJWT(t, "acct-1"), RefreshToken: "rt-1"}

	store := &fakeCodexStore{}
	store.setTokens(old)

	refresh := func(string) (*codex.CodexTokens, error) {
		return nil, fmt.Errorf("codex session expired: please reconnect Codex")
	}

	_, err := coordinatedCodexRefresh(context.Background(), store, userID, codexHeldFrom(old), refresh)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconnect Codex",
		"a dead session must surface a reconnect-shaped error for the summary extractor")
}
