// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package drivers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AccountLister lists the user's provider accounts with their secrets open:
// what HasStaleReliantKey reads.
type AccountLister interface {
	ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error)
}

// AvailabilityStore is what BuildAvailableDrivers reads: the user's accounts
// and their limit state.
type AvailabilityStore interface {
	AccountLister
	ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error)
}

// AccountTokenStore is the slice of the account store the refresh
// coordinator (CoordinatedAccountRefresh) needs.
type AccountTokenStore interface {
	GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error)
	CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error)
	PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error)
	UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error)
}

// APIKeyStore is what the global API key provider (InitializeAPIKeyProvider)
// serves: driver availability, account token refresh, and the user's
// per-tag model preferences (modelprefs.LoadAll).
type APIKeyStore interface {
	AvailabilityStore
	AccountTokenStore
	// ListSettingsByKey is modelprefs.SettingsReader: the store is handed to
	// modelprefs.LoadAll.
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*db.Setting, error)
}
