// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ClearOtherThreadAssignments clears incompatible provider pins before the
// next assignment after a chat-wide provider switch, scoped to the thread's
// owner. Repeated calls are harmless.
func (r *Repo) ClearOtherThreadAssignments(ctx context.Context, userID, threadID, provider string) error {
	return store.ClearOtherThreadAssignments(ctx, r.ORM(), userID, threadID, provider)
}

// Provider account persistence (design §1.5). The store owns its SQL and its own transactions.

// ListProviderAccounts returns metadata only, ordered by (provider, priority).
func (r *Repo) ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error) {
	return r.accounts.ListProviderAccounts(ctx, userID)
}

// ListProviderAccountsWithSecrets opens every secret. An account whose secret does not open
// is skipped and logged, never fatal: one broken credential must not hide the others.
func (r *Repo) ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error) {
	return r.accounts.ListProviderAccountsWithSecrets(ctx, userID)
}

func (r *Repo) GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error) {
	return r.accounts.GetProviderAccountWithSecrets(ctx, userID, accountID)
}

// UpsertProviderAccount inserts at priority max+1, or updates the row with the same identity
// (fresh secrets, needs_reauth cleared; label, priority and pause are kept).
func (r *Repo) UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
	return r.accounts.UpsertProviderAccount(ctx, u)
}

// CompareAndSwapAccountTokens writes next only if the stored refresh token still equals expectedRefresh.
func (r *Repo) CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error) {
	return r.accounts.CompareAndSwapAccountTokens(ctx, userID, accountID, expectedRefresh, next)
}

func (r *Repo) PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	return r.accounts.PatchProviderAccount(ctx, userID, accountID, p)
}

// ReorderProviderAccounts sets priorities 0..n-1 in orderedIDs order, atomically. orderedIDs
// must be exactly the user's accounts for the provider (ErrInvalidAccountOrder otherwise).
func (r *Repo) ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error {
	return r.accounts.ReorderProviderAccounts(ctx, userID, provider, orderedIDs)
}

// DeleteProviderAccount removes the account and re-densifies the provider's priorities.
// Thread assignments and limit state cascade. ErrProviderAccountNotFound if absent.
func (r *Repo) DeleteProviderAccount(ctx context.Context, userID, accountID string) error {
	return r.accounts.DeleteProviderAccount(ctx, userID, accountID)
}

// ListAccountLimits returns limit state by account id for the user's accounts that have any.
func (r *Repo) ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error) {
	return r.accounts.ListAccountLimits(ctx, userID)
}

// UpsertAccountLimitSnapshot stores the windows only if s.ObservedAt is newer than the stored
// one (monotonic across processes). It never disturbs the bench or the poll schedule, except
// that an "allowed" snapshot observed after the bench was set lifts it and clears the probe count.
func (r *Repo) UpsertAccountLimitSnapshot(ctx context.Context, s core.LimitState) error {
	return r.accounts.UpsertAccountLimitSnapshot(ctx, s)
}

// BenchAccount refuses the account until `until` (GREATEST with an existing bench). probe marks
// an unknown-reset bench: it increments limited_probes; otherwise the count resets to 0.
func (r *Repo) BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error {
	return r.accounts.BenchAccount(ctx, userID, accountID, until, window, probe)
}

// LeaseUsagePolls returns ids whose usage_api_next_at <= now and pushes each to now+lease, so
// concurrent leasers get disjoint sets.
func (r *Repo) LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	return r.accounts.LeaseUsagePolls(ctx, now, limit, lease)
}

// ScheduleUsagePoll sets the next poll time (zero = unschedule), failure count and last error.
func (r *Repo) ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error {
	return r.accounts.ScheduleUsagePoll(ctx, accountID, next, failures, errText)
}

// GetThreadAssignment returns (nil, nil) when the thread has no account for the provider.
func (r *Repo) GetThreadAssignment(ctx context.Context, threadID, provider string) (*core.Assignment, error) {
	return r.accounts.GetThreadAssignment(ctx, threadID, provider)
}

// SetThreadAssignment is a compare-and-set: it applies only if the thread's current account
// for the provider equals expectedAccountID, where "" means "no assignment yet". applied=false
// means another writer got there first; the caller reads the row and adopts it.
func (r *Repo) SetThreadAssignment(ctx context.Context, a core.Assignment, expectedAccountID string) (bool, error) {
	return r.accounts.SetThreadAssignment(ctx, a, expectedAccountID)
}

// RecentAssignmentCounts counts threads assigned to each account since the given time.
func (r *Repo) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	return r.accounts.RecentAssignmentCounts(ctx, accountIDs, since)
}

// IsProviderAccountNotFound reports whether err is core.ErrProviderAccountNotFound.
func IsProviderAccountNotFound(err error) bool {
	return errors.Is(err, core.ErrProviderAccountNotFound)
}

// GetAccountLimits returns one account's limit state, or (nil, nil) when it has none. Only the
// usage poller reads limits by bare account id.
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

// LatestServingAccount returns the account that served the thread's newest assistant message
// that names one, or "" when none does. Only the account-management handler reads it.
func (r *Repo) LatestServingAccount(ctx context.Context, threadID string) (string, error) {
	return r.accounts.LatestServingAccount(ctx, threadID)
}

// ListThreadAssignments returns the thread's sticky account per provider, most recent first. Only
// the account-management handler reads it.
func (r *Repo) ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error) {
	return r.accounts.ListThreadAssignments(ctx, threadID)
}
