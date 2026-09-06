// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Codex OAuth refresh tokens rotate on every exchange, so the same
// coordination Claude needs applies here: two uncoordinated refreshes with the
// same refresh token race, the first wins, and the second gets invalid_grant
// ("codex session expired") even though a perfectly good token was just
// persisted. Coordination happens at two levels:
//
//  1. In-process: codexRefreshGroup collapses concurrent refresh attempts per
//     user, so losers wait for the winner and share its result.
//  2. Cross-process (api and worker share the DB): before consuming the
//     refresh token we re-read the store and adopt any newer rotation;
//     persistence uses compare-and-swap on the consumed refresh token; and a
//     failed refresh re-checks the store once before surfacing, adopting a
//     concurrent rotation instead of erroring.
//
// Unlike Claude, codex_auth_tokens has no expires_at column: a Codex access
// token is a JWT carrying its own exp claim. Expiry is therefore DERIVED from
// the access token via codexTokenExpiry rather than stored alongside it.

// codexTokenStore is the narrow persistence surface the coordinated Codex
// token refresher needs. db.Repository satisfies it.
type codexTokenStore interface {
	GetCodexAuthTokens(ctx context.Context, userID string) (*db.CodexAuthTokens, error)
	CompareAndSwapCodexAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens db.CodexAuthTokens) (bool, error)
}

// codexRefreshFunc performs the actual OAuth refresh-token exchange.
type codexRefreshFunc func(refreshToken string) (*codex.CodexTokens, error)

// codexRefreshGroup collapses concurrent Codex token refreshes per user within
// this process.
var codexRefreshGroup singleflight.Group

// codexTokenExpiry returns the expiry embedded in a Codex JWT access token. An
// unparseable token yields the zero time, which every caller treats as
// "expired" — the same disposition codex.IsTokenExpired takes.
func codexTokenExpiry(accessToken string) time.Time {
	exp, err := codex.GetTokenExpiry(accessToken)
	if err != nil {
		return time.Time{}
	}
	return exp
}

// codexTokenRepo returns the repository backing the global API key provider.
func codexTokenRepo() (db.Repository, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if globalAPIKeyProvider == nil || globalAPIKeyProvider.repo == nil {
		return nil, fmt.Errorf("API key provider not initialized")
	}
	return globalAPIKeyProvider.repo, nil
}

// BuildCodexTokenRefresher returns a closure that refreshes Codex OAuth tokens
// with in-process single-flight and cross-process store coordination,
// persisting rotations to the database. The closure captures the global API
// key provider's repo and the userID so the transport interceptor can call it
// without DB knowledge.
func BuildCodexTokenRefresher(ctx context.Context, userID string) func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
	return func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
		repo, err := codexTokenRepo()
		if err != nil {
			return llm.OAuthTokens{}, fmt.Errorf("codex token refresh failed: %w", err)
		}
		return coordinatedCodexRefresh(ctx, repo, userID, held, codex.RefreshCodexTokens)
	}
}

// BuildCodexTokenReloader returns a closure that re-reads the persisted Codex
// OAuth tokens. Drivers use it to recover from a 401 caused by another process
// rotating the tokens after this driver loaded its credentials.
func BuildCodexTokenReloader(ctx context.Context, userID string) func() (*llm.OAuthTokens, error) {
	return func() (*llm.OAuthTokens, error) {
		repo, err := codexTokenRepo()
		if err != nil {
			return nil, err
		}
		stored, err := repo.GetCodexAuthTokens(context.WithoutCancel(ctx), userID)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, nil
		}
		return &llm.OAuthTokens{
			AccessToken:  stored.AccessToken,
			RefreshToken: stored.RefreshToken,
			ExpiresAt:    codexTokenExpiry(stored.AccessToken),
		}, nil
	}
}

// coordinatedCodexRefresh single-flights refreshAndPersistCodexTokens per user
// so concurrent callers in this process trigger exactly one upstream refresh
// and share its result.
func coordinatedCodexRefresh(ctx context.Context, store codexTokenStore, userID string, held llm.OAuthTokens, refresh codexRefreshFunc) (llm.OAuthTokens, error) {
	// Detach from the caller's context: the refresh consumes a single-use
	// refresh token, so once started it must run to completion and persist —
	// a cancelled workflow activity must not orphan the rotation (which would
	// permanently invalidate the session). The result is also shared with
	// concurrent callers via singleflight, so it must not be tied to any one
	// caller's lifetime.
	ctx = context.WithoutCancel(ctx)

	v, err, _ := codexRefreshGroup.Do(userID, func() (any, error) {
		return refreshAndPersistCodexTokens(ctx, store, userID, held, refresh)
	})
	if err != nil {
		return llm.OAuthTokens{}, err
	}
	return v.(llm.OAuthTokens), nil
}

// refreshAndPersistCodexTokens refreshes the Codex OAuth tokens for userID,
// coordinating with other processes through the persisted store. It returns
// the token state the caller should use. It only errors when the session is
// genuinely unrecoverable (refresh failed AND the store has no newer tokens).
func refreshAndPersistCodexTokens(ctx context.Context, store codexTokenStore, userID string, held llm.OAuthTokens, refresh codexRefreshFunc) (llm.OAuthTokens, error) {
	// Cross-process check: another process may already have rotated the
	// tokens. Re-read the store before consuming the single-use refresh token
	// and adopt a newer live access token if there is one.
	if stored := loadStoredCodexTokens(ctx, store, userID); stored != nil {
		if stored.AccessToken != "" && stored.AccessToken != held.AccessToken && !codex.IsTokenExpired(stored.AccessToken) {
			logging.Info("Codex tokens already rotated elsewhere; adopting persisted tokens instead of refreshing",
				"user_id", userID, "expires_at", stored.ExpiresAt)
			return *stored, nil
		}
		// The store owns the refresh-token lineage: if a different refresh
		// token is persisted (e.g. rotated but already near expiry again),
		// consume that one rather than our stale copy.
		if stored.RefreshToken != "" {
			held.RefreshToken = stored.RefreshToken
		}
	}
	if held.RefreshToken == "" {
		return llm.OAuthTokens{}, fmt.Errorf("codex token refresh failed: no refresh token available")
	}

	tokens, refreshErr := refresh(held.RefreshToken)
	if refreshErr != nil {
		// The refresh token may have been consumed by another process in the
		// window since our re-read. If the store now holds a newer live
		// rotation, adopt it instead of surfacing a terminal "reconnect"
		// error for a session that is actually fine.
		if stored := loadStoredCodexTokens(ctx, store, userID); stored != nil &&
			stored.AccessToken != "" && stored.AccessToken != held.AccessToken && !codex.IsTokenExpired(stored.AccessToken) {
			logging.Warn("Codex token refresh failed but another process rotated tokens; adopting persisted tokens",
				"user_id", userID, "error", refreshErr)
			return *stored, nil
		}
		return llm.OAuthTokens{}, fmt.Errorf("codex token refresh failed: %w", refreshErr)
	}

	newState := llm.OAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    codexTokenExpiry(tokens.AccessToken),
	}

	// Persist with compare-and-swap: only overwrite the row if the stored
	// refresh token is still the one we consumed. If a concurrent rotation
	// won the row, its refresh token is the live lineage and must not be
	// clobbered.
	dbTokens := db.CodexAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		AccountID:    tokens.AccountID,
	}
	swapped, casErr := store.CompareAndSwapCodexAuthTokens(ctx, userID, held.RefreshToken, dbTokens)
	switch {
	case casErr != nil:
		// Still return the fresh tokens: a persistence failure must not fail
		// the request that triggered the refresh.
		logging.Warn("Failed to persist refreshed Codex tokens; continuing with in-memory tokens",
			"user_id", userID, "error", casErr)
	case !swapped:
		// A concurrent rotation was persisted first (or the row was deleted,
		// e.g. the user disconnected Codex). Prefer the persisted lineage
		// when it is live; otherwise use our fresh tokens in memory only.
		if stored := loadStoredCodexTokens(ctx, store, userID); stored != nil &&
			stored.AccessToken != "" && !codex.IsTokenExpired(stored.AccessToken) {
			logging.Warn("Codex token refresh raced a concurrent rotation; adopting persisted tokens",
				"user_id", userID)
			return *stored, nil
		}
		logging.Warn("Codex token rotation not persisted (stored refresh token changed or row missing); continuing with in-memory tokens",
			"user_id", userID)
	default:
		logging.Info("Codex OAuth tokens refreshed and persisted", "user_id", userID, "expires_at", newState.ExpiresAt)
	}
	return newState, nil
}

// loadStoredCodexTokens reads the persisted Codex tokens, returning nil on
// error or when none are stored.
func loadStoredCodexTokens(ctx context.Context, store codexTokenStore, userID string) *llm.OAuthTokens {
	stored, err := store.GetCodexAuthTokens(ctx, userID)
	if err != nil {
		logging.Warn("Failed to read persisted Codex tokens", "user_id", userID, "error", err)
		return nil
	}
	if stored == nil {
		return nil
	}
	return &llm.OAuthTokens{
		AccessToken:  stored.AccessToken,
		RefreshToken: stored.RefreshToken,
		ExpiresAt:    codexTokenExpiry(stored.AccessToken),
	}
}
