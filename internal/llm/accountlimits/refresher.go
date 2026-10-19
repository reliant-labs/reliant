// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Refresher is what the provider_account handler needs to refresh one account's
// usage on demand (Settings → Providers on open, and the ↻ button).
type Refresher interface {
	// RefreshUsage reads the account's usage now, unless it was read within
	// UsageFloor (throttled), and returns the account with the limit state it
	// ends with. A usage endpoint that fails is not an error: the account keeps
	// its headers and the failure is on Limits.UsageAPIError. An account that is
	// absent or another user's is core.ErrProviderAccountNotFound.
	RefreshUsage(ctx context.Context, userID, accountID string) (RefreshResult, error)
}

// RefreshResult is the state of an account after a refresh.
type RefreshResult struct {
	Account core.ProviderAccount
	// Limits is nil when the account has no limit state at all.
	Limits *core.LimitState
	// Throttled is true when the usage API was not called because it was called
	// for this account within UsageFloor; Limits is the stored state.
	Throttled bool
}

// Refresh implements Refresher.
func (f *Fetcher) RefreshUsage(ctx context.Context, userID, accountID string) (RefreshResult, error) {
	res, err := f.Refresh(ctx, userID, accountID)
	if err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{Account: res.Account, Limits: res.Limits, Throttled: res.Outcome == OutcomeThrottled}, nil
}

var _ Refresher = (*Fetcher)(nil)
