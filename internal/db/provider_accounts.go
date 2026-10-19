// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
)

// Provider account persistence (design §1.5). The store owns its SQL and its own transactions.

var _ core.ProviderAccountStore = (*Repo)(nil)

func (r *Repo) ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error) {
	return r.accounts.ListProviderAccounts(ctx, userID)
}

func (r *Repo) ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error) {
	return r.accounts.ListProviderAccountsWithSecrets(ctx, userID)
}

func (r *Repo) GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error) {
	return r.accounts.GetProviderAccountWithSecrets(ctx, userID, accountID)
}

func (r *Repo) UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
	return r.accounts.UpsertProviderAccount(ctx, u)
}

// CompareAndSwapAccountTokens rotates an account's tokens. An account that still mirrors an
// OAuth legacy row also has the rotation written through to that row: refresh tokens are
// single-use, and the previous release (live during a rolling deploy) reads only the legacy
// table, so leaving the spent token there would sign its user out.
func (r *Repo) CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error) {
	swapped, err := r.accounts.CompareAndSwapAccountTokens(ctx, userID, accountID, expectedRefresh, next)
	if err != nil || !swapped {
		return swapped, err
	}
	if err := r.writeThroughRotation(ctx, userID, accountID, expectedRefresh, next); err != nil {
		slog.Warn("rotated account tokens were not written through to the legacy table", "account_id", accountID, "error", err)
	}
	return true, nil
}

// writeThroughRotation mirrors a rotation into the legacy row it was copied from, guarded by the
// refresh token that was consumed: if the previous release already rotated that row, or the user
// disconnected in it, the legacy row is left exactly as it is (the sync adopts or prunes it).
func (r *Repo) writeThroughRotation(ctx context.Context, userID, accountID, consumedRefresh string, next core.AccountTokens) error {
	provider, source, err := r.accounts.LegacyInfo(ctx, userID, accountID)
	if err != nil || source == "" {
		return err
	}
	acct, err := r.accounts.GetProviderAccountWithSecrets(ctx, userID, accountID)
	if err != nil {
		return err
	}
	refresh := acct.Secrets.Refresh
	if refresh == "" {
		refresh = consumedRefresh
	}
	var swapped bool
	switch source {
	case postgresstore.LegacySourceClaude:
		swapped, err = r.settings.CompareAndSwapClaudeAuthTokens(ctx, userID, consumedRefresh, core.ClaudeAuthTokens{
			AccessToken: acct.Secrets.Secret, RefreshToken: refresh, ExpiresAt: acct.ExpiresAt,
			AccountUUID: acct.ExternalAccountID, AccountEmail: acct.Email,
			OrganizationUUID: acct.OrganizationID, OrganizationName: acct.OrganizationName, Scope: acct.Scope,
		})
	case postgresstore.LegacySourceCodex:
		swapped, err = r.settings.CompareAndSwapCodexAuthTokens(ctx, userID, consumedRefresh, core.CodexAuthTokens{
			AccessToken: acct.Secrets.Secret, RefreshToken: refresh, IDToken: acct.Secrets.IDToken, AccountID: acct.ExternalAccountID,
		})
	case postgresstore.LegacySourceAntigravity:
		swapped, err = r.settings.CompareAndSwapAntigravityAuthTokens(ctx, userID, consumedRefresh, core.AntigravityAuthTokens{
			AccessToken: acct.Secrets.Secret, RefreshToken: refresh, ExpiresAt: acct.ExpiresAt, IDToken: acct.Secrets.IDToken, Scope: acct.Scope,
		})
	default:
		return nil // API keys and device tokens never rotate
	}
	if err != nil || !swapped {
		return err
	}
	// The legacy write stamped its own updated_at; level the mirror with it so the sync does
	// not read our own write-through as a newer legacy credential.
	for _, t := range legacyTables {
		if t.source != source {
			continue
		}
		rows, err := t.read(ctx, r.DB.SQLDB(), userID, provider)
		if err != nil || len(rows) == 0 {
			return err
		}
		return r.accounts.StampMirror(ctx, accountID, rows[0].updatedAt)
	}
	return nil
}

func (r *Repo) PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	return r.accounts.PatchProviderAccount(ctx, userID, accountID, p)
}

func (r *Repo) ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error {
	return r.accounts.ReorderProviderAccounts(ctx, userID, provider, orderedIDs)
}

func (r *Repo) DeleteProviderAccount(ctx context.Context, userID, accountID string) error {
	return r.accounts.DeleteProviderAccount(ctx, userID, accountID)
}

func (r *Repo) ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error) {
	return r.accounts.ListAccountLimits(ctx, userID)
}

func (r *Repo) UpsertAccountLimitSnapshot(ctx context.Context, s core.LimitState) error {
	return r.accounts.UpsertAccountLimitSnapshot(ctx, s)
}

func (r *Repo) BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error {
	return r.accounts.BenchAccount(ctx, userID, accountID, until, window, probe)
}

func (r *Repo) LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	return r.accounts.LeaseUsagePolls(ctx, now, limit, lease)
}

func (r *Repo) ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error {
	return r.accounts.ScheduleUsagePoll(ctx, accountID, next, failures, errText)
}

func (r *Repo) GetThreadAssignment(ctx context.Context, threadID, provider string) (*core.Assignment, error) {
	return r.accounts.GetThreadAssignment(ctx, threadID, provider)
}

func (r *Repo) SetThreadAssignment(ctx context.Context, a core.Assignment, expectedAccountID string) (bool, error) {
	return r.accounts.SetThreadAssignment(ctx, a, expectedAccountID)
}

func (r *Repo) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	return r.accounts.RecentAssignmentCounts(ctx, accountIDs, since)
}

// IsProviderAccountNotFound reports whether err is core.ErrProviderAccountNotFound.
func IsProviderAccountNotFound(err error) bool {
	return errors.Is(err, core.ErrProviderAccountNotFound)
}

// GetAccountLimits returns one account's limit state, or (nil, nil) when it has none. Not part of
// core.ProviderAccountStore: only the usage poller reads limits by bare account id.
func (r *Repo) GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error) {
	return r.accounts.GetAccountLimits(ctx, accountID)
}

// EnsureUsagePollScheduled schedules a usage poll only when none is scheduled and the account's
// provider is one of providers.
func (r *Repo) EnsureUsagePollScheduled(ctx context.Context, accountID string, next time.Time, providers []string) error {
	return r.accounts.EnsureUsagePollScheduled(ctx, accountID, next, providers)
}

// ClaimUsageFetch reports whether the caller may call the account's usage API now: false when
// another caller claimed it within the last floor.
func (r *Repo) ClaimUsageFetch(ctx context.Context, accountID string, now time.Time, floor time.Duration) (bool, error) {
	return r.accounts.ClaimUsageFetch(ctx, accountID, now, floor)
}

// ListThreadAssignments returns the thread's sticky account per provider, most recent first. Not
// part of core.ProviderAccountStore: only the account-management handler reads it.
func (r *Repo) ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error) {
	return r.accounts.ListThreadAssignments(ctx, threadID)
}
