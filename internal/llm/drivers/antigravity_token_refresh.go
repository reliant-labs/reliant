// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"

	"golang.org/x/sync/singleflight"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Antigravity's refresh coordination mirrors Claude's and Codex's, with one
// substantive difference: Google does NOT rotate the refresh token on an
// installed-app refresh. The response carries a new access token and no
// refresh_token, so the grant we consumed is still the live one afterwards.
//
// That removes the single-use hazard those two are built around, but not the
// need for coordination:
//
//  1. In-process: antigravityRefreshGroup collapses concurrent refreshes per
//     user, so N workers hitting an expired token make ONE token call instead
//     of N, and share the result.
//  2. Cross-process: the row can still be replaced out from under us — a fresh
//     sign-in issues a genuinely new refresh token, and a disconnect deletes
//     the row entirely. Persisting via compare-and-swap on the consumed
//     refresh token means a slow in-flight refresh cannot resurrect a
//     credential the user just revoked, or overwrite a newer grant.
//
// Expiry is STORED, not derived: the Google access token is opaque, so
// antigravity_auth_tokens carries a real expires_at column and nothing here
// ever inspects the token's contents.

// antigravityTokenStore is the narrow persistence surface the coordinated
// Antigravity token refresher needs. db.Repository satisfies it.
type antigravityTokenStore interface {
	GetAntigravityAuthTokens(ctx context.Context, userID string) (*db.AntigravityAuthTokens, error)
	CompareAndSwapAntigravityAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens db.AntigravityAuthTokens) (bool, error)
}

// antigravityRefreshFunc performs the actual OAuth refresh-token exchange.
type antigravityRefreshFunc func(refreshToken string) (*antigravity.AntigravityTokens, error)

// antigravityRefreshGroup collapses concurrent Antigravity token refreshes per
// user within this process.
var antigravityRefreshGroup singleflight.Group

// antigravityTokenRepo returns the repository backing the global API key provider.
func antigravityTokenRepo() (db.Repository, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if globalAPIKeyProvider == nil || globalAPIKeyProvider.repo == nil {
		return nil, fmt.Errorf("API key provider not initialized")
	}
	return globalAPIKeyProvider.repo, nil
}

// BuildAntigravityTokenRefresher returns a closure that refreshes Antigravity
// OAuth tokens with in-process single-flight and cross-process store
// coordination, persisting the result. The closure captures the global API key
// provider's repo and the userID so the transport interceptor can call it
// without DB knowledge.
func BuildAntigravityTokenRefresher(ctx context.Context, userID string) func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
	return func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
		repo, err := antigravityTokenRepo()
		if err != nil {
			return llm.OAuthTokens{}, fmt.Errorf("antigravity token refresh failed: %w", err)
		}
		return coordinatedAntigravityRefresh(ctx, repo, userID, held, antigravity.RefreshAntigravityTokens)
	}
}

// BuildAntigravityTokenReloader returns a closure that re-reads the persisted
// Antigravity OAuth tokens. Drivers use it to recover from a 401 caused by
// another process refreshing after this driver loaded its credentials.
func BuildAntigravityTokenReloader(ctx context.Context, userID string) func() (*llm.OAuthTokens, error) {
	return func() (*llm.OAuthTokens, error) {
		repo, err := antigravityTokenRepo()
		if err != nil {
			return nil, err
		}
		stored, err := repo.GetAntigravityAuthTokens(context.WithoutCancel(ctx), userID)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, nil
		}
		return &llm.OAuthTokens{
			AccessToken:  stored.AccessToken,
			RefreshToken: stored.RefreshToken,
			ExpiresAt:    stored.ExpiresAt,
		}, nil
	}
}

// coordinatedAntigravityRefresh single-flights refreshAndPersistAntigravityTokens
// per user so concurrent callers in this process trigger exactly one upstream
// refresh and share its result.
func coordinatedAntigravityRefresh(ctx context.Context, store antigravityTokenStore, userID string, held llm.OAuthTokens, refresh antigravityRefreshFunc) (llm.OAuthTokens, error) {
	// Detach from the caller's context: the result is shared with concurrent
	// callers via singleflight, so it must not be tied to any one caller's
	// lifetime, and a cancelled workflow activity must not orphan a refresh
	// that has already been persisted.
	ctx = context.WithoutCancel(ctx)

	v, err, _ := antigravityRefreshGroup.Do(userID, func() (any, error) {
		return refreshAndPersistAntigravityTokens(ctx, store, userID, held, refresh)
	})
	if err != nil {
		return llm.OAuthTokens{}, err
	}
	return v.(llm.OAuthTokens), nil
}

// refreshAndPersistAntigravityTokens refreshes the Antigravity OAuth tokens
// for userID, coordinating with other processes through the persisted store.
// It returns the token state the caller should use, and errors only when the
// session is genuinely unrecoverable.
func refreshAndPersistAntigravityTokens(ctx context.Context, store antigravityTokenStore, userID string, held llm.OAuthTokens, refresh antigravityRefreshFunc) (llm.OAuthTokens, error) {
	// Cross-process check: another process may already have refreshed. Adopt a
	// live persisted access token rather than spending another token call.
	if stored := loadStoredAntigravityTokens(ctx, store, userID); stored != nil {
		if stored.AccessToken != "" && stored.AccessToken != held.AccessToken && !antigravity.IsTokenExpired(stored.ExpiresAt) {
			logging.Info("Antigravity tokens already refreshed elsewhere; adopting persisted tokens",
				"user_id", userID, "expires_at", stored.ExpiresAt)
			return *stored, nil
		}
		// The store owns the grant: if a different refresh token is persisted
		// (a fresh sign-in), use that one rather than our stale copy, which
		// the new sign-in may have invalidated.
		if stored.RefreshToken != "" {
			held.RefreshToken = stored.RefreshToken
		}
	}
	if held.RefreshToken == "" {
		return llm.OAuthTokens{}, fmt.Errorf("antigravity token refresh failed: no refresh token available")
	}

	tokens, refreshErr := refresh(held.RefreshToken)
	if refreshErr != nil {
		// The grant may have been replaced since our re-read. If the store now
		// holds a newer live token, adopt it instead of surfacing a terminal
		// "reconnect" error for a session that is actually fine.
		if stored := loadStoredAntigravityTokens(ctx, store, userID); stored != nil &&
			stored.AccessToken != "" && stored.AccessToken != held.AccessToken && !antigravity.IsTokenExpired(stored.ExpiresAt) {
			logging.Warn("Antigravity token refresh failed but another process refreshed; adopting persisted tokens",
				"user_id", userID, "error", refreshErr)
			return *stored, nil
		}
		return llm.OAuthTokens{}, fmt.Errorf("antigravity token refresh failed: %w", refreshErr)
	}

	newState := llm.OAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
	}

	// Persist with compare-and-swap on the refresh token we consumed: if a
	// fresh sign-in replaced the grant, or a disconnect deleted the row, the
	// swap fails and we must not clobber that decision.
	dbTokens := db.AntigravityAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
		IDToken:      tokens.IDToken,
		Scope:        tokens.Scope,
	}
	swapped, casErr := store.CompareAndSwapAntigravityAuthTokens(ctx, userID, held.RefreshToken, dbTokens)
	switch {
	case casErr != nil:
		// Still return the fresh tokens: a persistence failure must not fail
		// the request that triggered the refresh.
		logging.Warn("Failed to persist refreshed Antigravity tokens; continuing with in-memory tokens",
			"user_id", userID, "error", casErr)
	case !swapped:
		// The row was replaced or removed. Prefer the persisted grant when it
		// is live; otherwise use our fresh tokens in memory only.
		if stored := loadStoredAntigravityTokens(ctx, store, userID); stored != nil &&
			stored.AccessToken != "" && !antigravity.IsTokenExpired(stored.ExpiresAt) {
			logging.Warn("Antigravity token refresh raced a concurrent sign-in; adopting persisted tokens",
				"user_id", userID)
			return *stored, nil
		}
		logging.Warn("Antigravity token refresh not persisted (stored refresh token changed or row missing); continuing with in-memory tokens",
			"user_id", userID)
	default:
		logging.Info("Antigravity OAuth tokens refreshed and persisted", "user_id", userID, "expires_at", newState.ExpiresAt)
	}
	return newState, nil
}

// loadStoredAntigravityTokens reads the persisted tokens, returning nil on
// error or when none are stored.
func loadStoredAntigravityTokens(ctx context.Context, store antigravityTokenStore, userID string) *llm.OAuthTokens {
	stored, err := store.GetAntigravityAuthTokens(ctx, userID)
	if err != nil {
		logging.Warn("Failed to read persisted Antigravity tokens", "user_id", userID, "error", err)
		return nil
	}
	if stored == nil {
		return nil
	}
	return &llm.OAuthTokens{
		AccessToken:  stored.AccessToken,
		RefreshToken: stored.RefreshToken,
		ExpiresAt:    stored.ExpiresAt,
	}
}
