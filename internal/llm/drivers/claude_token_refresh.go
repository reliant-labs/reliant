// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"

	"golang.org/x/sync/singleflight"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Claude OAuth refresh tokens are SINGLE-USE: every refresh-token exchange
// rotates the refresh token, invalidating the previous one. Two uncoordinated
// refreshes with the same refresh token therefore race: the first wins and the
// second gets invalid_grant ("claude session expired"), which used to surface
// as a terminal workflow error even though a perfectly good token had just
// been persisted. Coordination happens at two levels:
//
//  1. In-process: claudeRefreshGroup collapses concurrent refresh attempts per
//     user, so losers wait for the winner and share its result.
//  2. Cross-process (api and worker share the DB): before consuming the
//     refresh token we re-read the store and adopt any newer rotation;
//     persistence uses compare-and-swap on the consumed refresh token; and a
//     failed refresh re-checks the store once before surfacing, adopting a
//     concurrent rotation instead of erroring.

// claudeTokenStore is the narrow persistence surface the coordinated Claude
// token refresher needs. db.Repository satisfies it.
type claudeTokenStore interface {
	GetClaudeAuthTokens(ctx context.Context, userID string) (*db.ClaudeAuthTokens, error)
	CompareAndSwapClaudeAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens db.ClaudeAuthTokens) (bool, error)
}

// claudeRefreshFunc performs the actual OAuth refresh-token exchange.
type claudeRefreshFunc func(refreshToken string) (*claude.ClaudeTokens, error)

// claudeRefreshGroup collapses concurrent Claude token refreshes per user
// within this process.
var claudeRefreshGroup singleflight.Group

// claudeTokenRepo returns the repository backing the global API key provider.
func claudeTokenRepo() (db.Repository, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if globalAPIKeyProvider == nil || globalAPIKeyProvider.repo == nil {
		return nil, fmt.Errorf("API key provider not initialized")
	}
	return globalAPIKeyProvider.repo, nil
}

// BuildClaudeTokenRefresher returns a closure that refreshes Claude OAuth
// tokens with in-process single-flight and cross-process store coordination,
// persisting rotations to the database. The closure captures the global API
// key provider's repo and the userID so the transport interceptor can call it
// without DB knowledge.
func BuildClaudeTokenRefresher(ctx context.Context, userID string) func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
	return func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
		repo, err := claudeTokenRepo()
		if err != nil {
			return llm.OAuthTokens{}, fmt.Errorf("claude token refresh failed: %w", err)
		}
		return coordinatedClaudeRefresh(ctx, repo, userID, held, claude.RefreshClaudeTokens)
	}
}

// BuildClaudeTokenReloader returns a closure that re-reads the persisted
// Claude OAuth tokens. Drivers use it to recover from a 401 caused by another
// process rotating the tokens after this driver loaded its credentials.
func BuildClaudeTokenReloader(ctx context.Context, userID string) func() (*llm.OAuthTokens, error) {
	return func() (*llm.OAuthTokens, error) {
		repo, err := claudeTokenRepo()
		if err != nil {
			return nil, err
		}
		stored, err := repo.GetClaudeAuthTokens(context.WithoutCancel(ctx), userID)
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

// coordinatedClaudeRefresh single-flights refreshAndPersistClaudeTokens per
// user so concurrent callers in this process trigger exactly one upstream
// refresh and share its result.
func coordinatedClaudeRefresh(ctx context.Context, store claudeTokenStore, userID string, held llm.OAuthTokens, refresh claudeRefreshFunc) (llm.OAuthTokens, error) {
	// Detach from the caller's context: the refresh consumes a single-use
	// refresh token, so once started it must run to completion and persist —
	// a cancelled workflow activity must not orphan the rotation (which would
	// permanently invalidate the session). The result is also shared with
	// concurrent callers via singleflight, so it must not be tied to any one
	// caller's lifetime.
	ctx = context.WithoutCancel(ctx)

	v, err, _ := claudeRefreshGroup.Do(userID, func() (any, error) {
		return refreshAndPersistClaudeTokens(ctx, store, userID, held, refresh)
	})
	if err != nil {
		return llm.OAuthTokens{}, err
	}
	return v.(llm.OAuthTokens), nil
}

// refreshAndPersistClaudeTokens refreshes the Claude OAuth tokens for userID,
// coordinating with other processes through the persisted store. It returns
// the token state the caller should use. It only errors when the session is
// genuinely unrecoverable (refresh failed AND the store has no newer tokens).
func refreshAndPersistClaudeTokens(ctx context.Context, store claudeTokenStore, userID string, held llm.OAuthTokens, refresh claudeRefreshFunc) (llm.OAuthTokens, error) {
	// Cross-process check: another process may already have rotated the
	// tokens. Re-read the store before consuming the single-use refresh token
	// and adopt a newer live access token if there is one.
	if stored := loadStoredClaudeTokens(ctx, store, userID); stored != nil {
		if stored.AccessToken != "" && stored.AccessToken != held.AccessToken && !claude.IsTokenExpired(stored.ExpiresAt) {
			logging.Info("Claude tokens already rotated elsewhere; adopting persisted tokens instead of refreshing",
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
		return llm.OAuthTokens{}, fmt.Errorf("claude token refresh failed: no refresh token available")
	}

	tokens, refreshErr := refresh(held.RefreshToken)
	if refreshErr != nil {
		// The refresh token may have been consumed by another process in the
		// window since our re-read. If the store now holds a newer live
		// rotation, adopt it instead of surfacing a terminal "reconnect"
		// error for a session that is actually fine.
		if stored := loadStoredClaudeTokens(ctx, store, userID); stored != nil &&
			stored.AccessToken != "" && stored.AccessToken != held.AccessToken && !claude.IsTokenExpired(stored.ExpiresAt) {
			logging.Warn("Claude token refresh failed but another process rotated tokens; adopting persisted tokens",
				"user_id", userID, "error", refreshErr)
			return *stored, nil
		}
		return llm.OAuthTokens{}, fmt.Errorf("claude token refresh failed: %w", refreshErr)
	}

	newState := llm.OAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
	}

	// Persist with compare-and-swap: only overwrite the row if the stored
	// refresh token is still the one we consumed. If a concurrent rotation
	// won the row, its refresh token is the live lineage and must not be
	// clobbered.
	dbTokens := db.ClaudeAuthTokens{
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		ExpiresAt:        tokens.ExpiresAt,
		AccountUUID:      tokens.AccountUUID,
		AccountEmail:     tokens.AccountEmail,
		OrganizationUUID: tokens.OrganizationUUID,
		OrganizationName: tokens.OrganizationName,
		Scope:            tokens.Scope,
	}
	swapped, casErr := store.CompareAndSwapClaudeAuthTokens(ctx, userID, held.RefreshToken, dbTokens)
	switch {
	case casErr != nil:
		// Still return the fresh tokens: a persistence failure must not fail
		// the request that triggered the refresh.
		logging.Warn("Failed to persist refreshed Claude tokens; continuing with in-memory tokens",
			"user_id", userID, "error", casErr)
	case !swapped:
		// A concurrent rotation was persisted first (or the row was deleted,
		// e.g. the user disconnected Claude). Prefer the persisted lineage
		// when it is live; otherwise use our fresh tokens in memory only.
		if stored := loadStoredClaudeTokens(ctx, store, userID); stored != nil &&
			stored.AccessToken != "" && !claude.IsTokenExpired(stored.ExpiresAt) {
			logging.Warn("Claude token refresh raced a concurrent rotation; adopting persisted tokens",
				"user_id", userID)
			return *stored, nil
		}
		logging.Warn("Claude token rotation not persisted (stored refresh token changed or row missing); continuing with in-memory tokens",
			"user_id", userID)
	default:
		logging.Info("Claude OAuth tokens refreshed and persisted", "user_id", userID, "expires_at", tokens.ExpiresAt)
	}
	return newState, nil
}

// loadStoredClaudeTokens reads the persisted Claude tokens, returning nil on
// error or when none are stored.
func loadStoredClaudeTokens(ctx context.Context, store claudeTokenStore, userID string) *llm.OAuthTokens {
	stored, err := store.GetClaudeAuthTokens(ctx, userID)
	if err != nil {
		logging.Warn("Failed to read persisted Claude tokens", "user_id", userID, "error", err)
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
