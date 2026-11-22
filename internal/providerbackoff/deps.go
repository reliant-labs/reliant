// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package providerbackoff

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ─── Dependency interfaces ──────────────────────────────────────────
//
// Declare here the *behaviour* this package needs from each thing it
// calls — one interface per dep, named for the role it plays, NOT for
// the concrete type that will satisfy it. Each one gets a matching
// field in Deps (service.go), and that field must be the interface,
// not the concrete type: `Store Store`, never `Store *db.Postgres`.
// That is what makes New(Deps{...}) constructible from mocks alone,
// and it is lint-enforced for every package
// (forgeconv-deps-are-interfaces).
//
// Keep each one minimal — only the methods this package calls. When
// the far end is another internal package, its own Service interface
// is usually the right type to use directly, and its mock_gen.go
// already has a mock of it.
//
// `forge generate` writes a mock for EVERY interface in this file,
// alongside the Service mock, into mock_gen.go — the first line above
// is what tells it to. Never hand-roll a fake for something declared
// here. `forge fix narrow-deps internal/providerbackoff` appends the
// interfaces it narrows a wide dep to here too.
//
//	// Store is this package's persistence boundary.
//	type Store interface {
//		Get(ctx context.Context, id string) (Thing, error)
//	}

// Markers is the provider_backoff table's writes. store.ProviderBackoffExtStore
// satisfies it.
type Markers interface {
	RecordProviderBackoff(ctx context.Context, chatID, threadID string, attempt, maxAttempts, statusCode int, reason string, delay time.Duration, now time.Time) error
	ClearProviderBackoff(ctx context.Context, threadID string, now time.Time) error
	RecordUsageLimitWait(ctx context.Context, chatID, threadID, provider, reason string, resumeAt, now time.Time) error
	// ClearUsageLimitWait reports how many threads it released.
	ClearUsageLimitWait(ctx context.Context, chatID, threadID string, now time.Time) (int64, error)

	ListChatProviderBackoff(ctx context.Context, chatID string) (map[string]core.ProviderBackoff, error)
	ListChatsHeldOnUsageLimit(ctx context.Context, chatIDs []string, since time.Time) ([]string, error)
	ListCapacityWakeTargets(ctx context.Context, userID string) ([]string, error)
	ListOrgCapacityWakeTargets(ctx context.Context, orgID string) ([]string, error)
}

// TxRunner opens the transaction a marker write runs in. *db.Repo satisfies it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// ChatActivity announces a chat's activity once the transaction in ctx
// commits. *db.Repo satisfies it until the chat update emitters have their own
// package.
type ChatActivity interface {
	EmitChatActivityIfChanged(ctx context.Context, chatID string) error
}
