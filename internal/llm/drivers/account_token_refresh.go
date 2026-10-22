// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// One refresh coordinator for every OAuth provider, keyed by ACCOUNT.
//
// Claude and Codex refresh tokens are SINGLE-USE: every exchange rotates the refresh token and
// invalidates the previous one. Two uncoordinated refreshes with the same token therefore race:
// the first wins and the second gets invalid_grant, which used to surface as a terminal
// "session expired" even though a good token had just been persisted. Coordination happens at
// two levels:
//
//  1. In-process: refreshGroup collapses concurrent refreshes of one account, so losers wait for
//     the winner and share its result. Two different accounts refresh independently.
//  2. Cross-process (api and worker share the DB): before consuming the refresh token the store
//     is re-read and a newer rotation adopted; persistence is a compare-and-swap on the consumed
//     refresh token; and a failed refresh re-checks the store once before surfacing.
//
// Antigravity (Google) does not rotate its refresh token, which removes the single-use hazard
// but not the need for the rest. Codex has no stored expiry: it is the exp claim of the JWT.
//
// A refresh the provider REJECTS (invalid_grant) while the store holds no newer lineage marks
// the account needs-sign-in. That removes it from AvailableDrivers.Accounts, so the account
// router moves the chat to another account instead of failing it forever. A refresh that merely
// fails (network, 5xx) never does: the credential is not in question.

// ProviderRefresh is one provider's token exchange: refresh token in, the new credential out.
// It carries whatever identity the provider's response states (see core.AccountTokens).
type ProviderRefresh func(refreshToken string) (core.AccountTokens, error)

var refreshGroup singleflight.Group // key: userID + "/" + accountID

// providerRefreshers is a REGISTRY with no default on purpose: a driver id with no entry gets no
// refresher, which fails visibly and immediately instead of silently calling another provider's
// token endpoint an hour after sign-in appeared to work.
var providerRefreshers = map[models.DriverID]ProviderRefresh{
	models.DriverID("claude"): func(rt string) (core.AccountTokens, error) {
		t, err := claude.RefreshClaudeTokens(rt)
		if err != nil {
			return core.AccountTokens{}, err
		}
		out := core.AccountTokens{
			Secret: t.AccessToken, Refresh: t.RefreshToken, ExpiresAt: t.ExpiresAt,
			Email: t.AccountEmail, OrganizationName: t.OrganizationName,
			ExternalAccountID: t.AccountUUID, OrganizationID: t.OrganizationUUID,
		}
		if t.AccountUUID != "" {
			out.IdentityKey = core.ClaudeIdentityKey(t.AccountUUID, t.OrganizationUUID)
		}
		return out, nil
	},
	models.DriverID("codex"): func(rt string) (core.AccountTokens, error) {
		t, err := codex.RefreshCodexTokens(rt)
		if err != nil {
			return core.AccountTokens{}, err
		}
		out := core.AccountTokens{
			Secret: t.AccessToken, Refresh: t.RefreshToken, IDToken: t.IDToken,
			ExpiresAt: codexTokenExpiry(t.AccessToken), ExternalAccountID: t.AccountID,
		}
		if id, ok := core.CodexIdentityFromJWT(t.IDToken, t.AccessToken); ok {
			out.IdentityKey, out.Email, out.PlanRaw, out.Plan = id.IdentityKey, id.Email, id.PlanRaw, id.Plan
			out.ExternalAccountID = id.ExternalAccountID
		}
		return out, nil
	},
	models.DriverID(antigravity.DriverID): func(rt string) (core.AccountTokens, error) {
		t, err := antigravity.RefreshAntigravityTokens(rt)
		if err != nil {
			return core.AccountTokens{}, err
		}
		out := core.AccountTokens{Secret: t.AccessToken, Refresh: t.RefreshToken, IDToken: t.IDToken, ExpiresAt: t.ExpiresAt}
		if id, ok := core.GoogleIdentityFromIDToken(t.IDToken); ok {
			out.IdentityKey, out.Email, out.ExternalAccountID = id.IdentityKey, id.Email, id.ExternalAccountID
		}
		return out, nil
	},
}

// codexTokenExpiry returns the expiry embedded in a Codex JWT access token. An unparseable token
// yields the zero time, which every caller treats as expired.
func codexTokenExpiry(accessToken string) time.Time {
	exp, err := codex.GetTokenExpiry(accessToken)
	if err != nil {
		return time.Time{}
	}
	return exp
}

// tokenExpired reports whether an account's access token needs refreshing before use.
func tokenExpired(provider, accessToken string, storedExpiry time.Time) bool {
	switch models.DriverID(provider) {
	case "codex":
		return codex.IsTokenExpired(accessToken)
	case models.DriverID(antigravity.DriverID):
		return antigravity.IsTokenExpired(storedExpiry)
	default:
		return claude.IsTokenExpired(storedExpiry)
	}
}

// accountExpiry is when an account's access token expires: derived for Codex, stored otherwise.
func accountExpiry(provider, accessToken string, storedExpiry time.Time) time.Time {
	if models.DriverID(provider) == "codex" {
		return codexTokenExpiry(accessToken)
	}
	return storedExpiry
}

func oauthTokensOf(a *core.AccountWithSecrets) llm.OAuthTokens {
	return llm.OAuthTokens{
		AccessToken:  a.Secrets.Secret,
		RefreshToken: a.Secrets.Refresh,
		ExpiresAt:    accountExpiry(a.Provider, a.Secrets.Secret, a.ExpiresAt),
	}
}

// CoordinatedAccountRefresh refreshes one account's OAuth tokens: single-flighted per
// (user, account), coordinated with other processes through the store, persisted with a
// compare-and-swap, and the result shared with every concurrent caller. held is the credential
// the caller used. The poller (accountlimits) and the LLM drivers both go through here, so a
// poll never consumes a refresh token a live request is about to consume.
func CoordinatedAccountRefresh(ctx context.Context, store AccountTokenStore, userID, accountID string, held llm.OAuthTokens, refresh ProviderRefresh) (llm.OAuthTokens, error) {
	// Detach from the caller's context: the exchange consumes a single-use refresh token, so once
	// started it must run to completion and persist. A cancelled workflow activity must not
	// orphan the rotation (which would permanently invalidate the session), and the result is
	// shared with concurrent callers, so it must not be tied to any one caller's lifetime.
	ctx = context.WithoutCancel(ctx)
	v, err, _ := refreshGroup.Do(userID+"/"+accountID, func() (any, error) {
		return refreshAndPersistAccount(ctx, store, userID, accountID, held, refresh)
	})
	if err != nil {
		return llm.OAuthTokens{}, err
	}
	return v.(llm.OAuthTokens), nil
}

func loadAccount(ctx context.Context, store AccountTokenStore, userID, accountID string) *core.AccountWithSecrets {
	a, err := store.GetProviderAccountWithSecrets(ctx, userID, accountID)
	if err != nil {
		if !errors.Is(err, core.ErrProviderAccountNotFound) {
			logging.WarnContext(ctx, "Failed to read persisted account tokens", "user_id", userID, "account_id", accountID, "error", err)
		}
		return nil
	}
	return a
}

// newerLive returns the stored tokens when they are a live credential other than the one held.
func newerLive(a *core.AccountWithSecrets, held llm.OAuthTokens) (llm.OAuthTokens, bool) {
	if a == nil || a.Secrets.Secret == "" || a.Secrets.Secret == held.AccessToken {
		return llm.OAuthTokens{}, false
	}
	if tokenExpired(a.Provider, a.Secrets.Secret, a.ExpiresAt) {
		return llm.OAuthTokens{}, false
	}
	return oauthTokensOf(a), true
}

func refreshAndPersistAccount(ctx context.Context, store AccountTokenStore, userID, accountID string, held llm.OAuthTokens, refresh ProviderRefresh) (llm.OAuthTokens, error) {
	provider := "account"
	stored := loadAccount(ctx, store, userID, accountID)
	if stored != nil {
		provider = stored.Provider
		if live, ok := newerLive(stored, held); ok {
			logging.InfoContext(ctx, "Account tokens already rotated elsewhere; adopting persisted tokens instead of refreshing",
				"user_id", userID, "account_id", accountID, "expires_at", live.ExpiresAt)
			return live, nil
		}
		// The store owns the refresh-token lineage: if a different refresh token is persisted
		// (rotated, but already near expiry again), consume that one rather than our stale copy.
		if stored.Secrets.Refresh != "" {
			held.RefreshToken = stored.Secrets.Refresh
		}
	}
	if held.RefreshToken == "" {
		return llm.OAuthTokens{}, fmt.Errorf("%s token refresh failed: no refresh token available", provider)
	}

	consumed := held.RefreshToken
	var (
		next    core.AccountTokens
		rerr    error
		retried bool
	)
	for {
		next, rerr = refresh(consumed)
		if rerr == nil {
			break
		}
		// The refresh token may have been consumed elsewhere in the window since our re-read
		// (the store now holds a newer rotation from another process). Adopt that lineage
		// rather than surface a terminal error for a session that is actually fine.
		adopted, retryWith := adoptNewerLineage(ctx, store, userID, accountID, held, consumed)
		if adopted != nil {
			logging.WarnContext(ctx, "Account token refresh failed but another writer rotated the tokens; adopting them",
				"user_id", userID, "account_id", accountID, "error", rerr)
			return *adopted, nil
		}
		if retryWith != "" && !retried {
			retried = true
			consumed = retryWith
			continue
		}
		if errors.Is(rerr, llm.ErrRefreshRejected) {
			markNeedsReauth(ctx, store, userID, accountID, rerr)
		}
		return llm.OAuthTokens{}, fmt.Errorf("%s token refresh failed: %w", provider, rerr)
	}

	newState := llm.OAuthTokens{AccessToken: next.Secret, RefreshToken: next.Refresh, ExpiresAt: next.ExpiresAt}
	if newState.RefreshToken == "" {
		newState.RefreshToken = consumed
	}

	// Persist with compare-and-swap: only overwrite the row if the stored refresh token is still
	// the one we consumed. If a concurrent rotation won, its lineage is live and must not be
	// clobbered.
	swapped, casErr := store.CompareAndSwapAccountTokens(ctx, userID, accountID, consumed, next)
	switch {
	case casErr != nil:
		// Still return the fresh tokens: a persistence failure must not fail the request that
		// triggered the refresh.
		logging.WarnContext(ctx, "Failed to persist refreshed account tokens; continuing with in-memory tokens",
			"user_id", userID, "account_id", accountID, "error", casErr)
	case !swapped:
		// A concurrent rotation was persisted first (or the account was removed). Prefer the
		// persisted lineage when it is live; otherwise use ours in memory only.
		if live, ok := newerLive(loadAccount(ctx, store, userID, accountID), llm.OAuthTokens{}); ok {
			logging.WarnContext(ctx, "Account token refresh raced a concurrent rotation; adopting persisted tokens",
				"user_id", userID, "account_id", accountID)
			return live, nil
		}
		logging.WarnContext(ctx, "Account token rotation not persisted (stored refresh token changed or account removed); continuing with in-memory tokens",
			"user_id", userID, "account_id", accountID)
	default:
		logging.InfoContext(ctx, "Account OAuth tokens refreshed and persisted", "user_id", userID, "account_id", accountID, "expires_at", next.ExpiresAt)
		rewriteIdentity(ctx, store, stored, next, newState)
	}
	return newState, nil
}

// adoptNewerLineage looks for a rotation someone else persisted. It returns the live tokens to
// adopt, or the refresh token to retry with when the lineage moved but its access token is not
// usable yet.
func adoptNewerLineage(ctx context.Context, store AccountTokenStore, userID, accountID string, held llm.OAuthTokens, consumed string) (*llm.OAuthTokens, string) {
	stored := loadAccount(ctx, store, userID, accountID)
	if live, ok := newerLive(stored, held); ok {
		return &live, ""
	}
	if stored != nil && stored.Secrets.Refresh != "" && stored.Secrets.Refresh != consumed {
		return nil, stored.Secrets.Refresh
	}
	return nil, ""
}

func markNeedsReauth(ctx context.Context, store AccountTokenStore, userID, accountID string, cause error) {
	reason := "the provider rejected the saved sign-in: " + cause.Error()
	if _, err := store.PatchProviderAccount(ctx, userID, accountID, core.AccountPatch{NeedsReauth: &reason}); err != nil {
		if !errors.Is(err, core.ErrProviderAccountNotFound) {
			logging.WarnContext(ctx, "Failed to mark an account as needing sign-in", "user_id", userID, "account_id", accountID, "error", err)
		}
		return
	}
	logging.WarnContext(ctx, "Account refresh token rejected and no newer lineage exists; account needs sign-in",
		"user_id", userID, "account_id", accountID)
}

// rewriteIdentity replaces a placeholder identity with the one the refresh response
// states (design §3.3). The store merges with an account that already owns that identity.
func rewriteIdentity(ctx context.Context, store AccountTokenStore, before *core.AccountWithSecrets, next core.AccountTokens, state llm.OAuthTokens) {
	if before == nil || next.IdentityKey == "" || next.IdentityKey == before.IdentityKey || !core.IsPlaceholderIdentityKey(before.IdentityKey) {
		return
	}
	first := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	_, _, err := store.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: before.UserID, Provider: before.Provider, AuthKind: before.AuthKind,
		IdentityKey:       next.IdentityKey,
		Email:             first(next.Email, before.Email),
		ExternalAccountID: first(next.ExternalAccountID, before.ExternalAccountID),
		OrganizationID:    first(next.OrganizationID, before.OrganizationID),
		OrganizationName:  first(next.OrganizationName, before.OrganizationName),
		Plan:              first(next.Plan, before.Plan),
		PlanRaw:           first(next.PlanRaw, before.PlanRaw),
		KeyHint:           before.KeyHint,
		Scope:             before.Scope,
		Secrets:           core.AccountSecrets{Secret: next.Secret, Refresh: state.RefreshToken, IDToken: next.IDToken},
		ExpiresAt:         next.ExpiresAt,
		ReplaceIdentityOf: before.IdentityKey,
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to rewrite the account's placeholder identity after refresh",
			"user_id", before.UserID, "account_id", before.ID, "error", err)
	}
}

// accountTokenRepo returns the store backing the global API key provider.
func accountTokenRepo() (AccountTokenStore, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if globalAPIKeyProvider == nil || globalAPIKeyProvider.repo == nil {
		return nil, fmt.Errorf("API key provider not initialized")
	}
	return globalAPIKeyProvider.repo, nil
}

// buildAccountRefresher returns the refresher/reloader pair for one account. The transport
// interceptor calls them without any database knowledge.
func buildAccountRefresher(ctx context.Context, driverID models.DriverID, userID, accountID string, refresh ProviderRefresh) (func(llm.OAuthTokens) (llm.OAuthTokens, error), func() (*llm.OAuthTokens, error)) {
	refresher := func(held llm.OAuthTokens) (llm.OAuthTokens, error) {
		repo, err := accountTokenRepo()
		if err != nil {
			return llm.OAuthTokens{}, fmt.Errorf("%s token refresh failed: %w", driverID, err)
		}
		return CoordinatedAccountRefresh(ctx, repo, userID, accountID, held, refresh)
	}
	// The reloader re-reads the persisted tokens so a driver can recover from a 401 caused by
	// another process rotating them after this driver loaded its credentials.
	reloader := func() (*llm.OAuthTokens, error) {
		repo, err := accountTokenRepo()
		if err != nil {
			return nil, err
		}
		stored, err := repo.GetProviderAccountWithSecrets(context.WithoutCancel(ctx), userID, accountID)
		if err != nil {
			if errors.Is(err, core.ErrProviderAccountNotFound) {
				return nil, nil
			}
			return nil, err
		}
		tokens := oauthTokensOf(stored)
		return &tokens, nil
	}
	return refresher, reloader
}

// RefreshingTokens hands out a usable access token for one account, refreshing it first through
// the coordinator when it has expired. It is the accountlimits.TokenSource the usage poller runs
// on. An account whose provider has no OAuth refresh path (an API key) just returns its secret.
type RefreshingTokens struct {
	Store AccountTokenStore
	// Exchange overrides the provider's token exchange (nil = the provider's real endpoint). It
	// exists so a caller's tests never reach a vendor.
	Exchange func(provider string) ProviderRefresh
}

// AccessToken implements accountlimits.TokenSource.
func (s RefreshingTokens) AccessToken(ctx context.Context, userID, accountID string) (string, error) {
	acct, err := s.Store.GetProviderAccountWithSecrets(ctx, userID, accountID)
	if err != nil {
		return "", err
	}
	refresh, ok := providerRefreshers[models.DriverID(acct.Provider)]
	if ok && s.Exchange != nil {
		refresh = s.Exchange(acct.Provider)
	}
	if !ok || acct.Secrets.Refresh == "" || !tokenExpired(acct.Provider, acct.Secrets.Secret, acct.ExpiresAt) {
		return acct.Secrets.Secret, nil
	}
	tokens, err := CoordinatedAccountRefresh(ctx, s.Store, userID, accountID, oauthTokensOf(acct), refresh)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}
