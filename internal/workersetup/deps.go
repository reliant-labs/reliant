// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are what this package's helpers read from the
// database; mock_gen.go mocks them. *db.Repo satisfies every one. Config
// itself takes the concrete *db.Repo: Setup is composition, wiring every
// activity and trigger firer over one repo.
package workersetup

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ChatWorkflowReader is what ChatWorkflowLookup reads: the chat's
// authoritative workflow id.
type ChatWorkflowReader interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// AccountAssignmentStore is what accountRouterStore translates for
// accountrouter.Store: a thread's account, and the limit and assignment
// counts the router scores accounts by.
type AccountAssignmentStore interface {
	GetThreadAssignment(ctx context.Context, threadID, provider string) (*core.Assignment, error)
	SetThreadAssignment(ctx context.Context, a core.Assignment, expectedAccountID string) (applied bool, err error)
	ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error)
	RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error)
}

// AccountCredentialStore is what credentialMarker writes: the needs-sign-in
// mark on an account whose key the provider rejected.
type AccountCredentialStore interface {
	PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error)
}

// AccountRoutingStore is what accountRouting wires: the router's store, the
// credential marker, and the per-user routing mode setting.
type AccountRoutingStore interface {
	AccountAssignmentStore
	ClearOtherThreadAssignments(ctx context.Context, userID, threadID, provider string) error
	AccountCredentialStore
	GetSettingValue(ctx context.Context, userID, key string) (string, bool)
}
