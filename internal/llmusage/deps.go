// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package llmusage

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
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
// here. `forge fix narrow-deps internal/llmusage` appends the
// interfaces it narrows a wide dep to here too.
//
//	// Store is this package's persistence boundary.
//	type Store interface {
//		Get(ctx context.Context, id string) (Thing, error)
//	}

// Ledger is the usage ledger's writes. store.LlmCallUsageExtStore satisfies it.
type Ledger interface {
	RecordLLMCallUsage(ctx context.Context, rec store.LLMCallUsageRecord) (bool, error)
	PruneLLMCallUsage(ctx context.Context, cutoff time.Time, batch int) (int64, error)
	PruneLLMUsagePurgeFences(ctx context.Context, now time.Time) (int64, error)
}

// TxRunner opens the transaction a ledger write runs in. *db.Repo satisfies it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}
