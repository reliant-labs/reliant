// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
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

func (r *Repo) CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error) {
	return r.accounts.CompareAndSwapAccountTokens(ctx, userID, accountID, expectedRefresh, next)
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

// LatestServingAccount returns the account that served the thread's newest assistant message
// that names one, or "" when none does. Not part of core.ProviderAccountStore: only the
// account-management handler reads it.
func (r *Repo) LatestServingAccount(ctx context.Context, threadID string) (string, error) {
	return r.accounts.LatestServingAccount(ctx, threadID)
}

// ListThreadAssignments returns the thread's sticky account per provider, most recent first. Not
// part of core.ProviderAccountStore: only the account-management handler reads it.
func (r *Repo) ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error) {
	return r.accounts.ListThreadAssignments(ctx, threadID)
}
