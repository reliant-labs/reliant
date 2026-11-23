// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package privacy

import (
	"context"

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
// here. `forge fix narrow-deps internal/privacy` appends the
// interfaces it narrows a wide dep to here too.
//
//	// Store is this package's persistence boundary.
//	type Store interface {
//		Get(ctx context.Context, id string) (Thing, error)
//	}

// Settings is the slice of the settings store the privacy choices use.
// store.SettingExtStore satisfies it.
type Settings interface {
	// GetSetting is an error satisfying svcerr.IsNotFound when the user has not
	// set key.
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*core.Setting, error)
	UpsertSetting(ctx context.Context, s *core.Setting) error
}
