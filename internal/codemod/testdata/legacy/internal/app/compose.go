// forge-scaffold (USER-OWNED, RECONCILED): forge writes this file ONCE
// (write-if-absent). Your construction order, sub-builders, carved cross-edges
// and the EXPRESSION filling each Deps field stay yours. What forge keeps in
// sync is the projection — the two things Go fixes for you, not you:
//   - the component set: a service/worker/operator discovered on disk with no
//     `c.<Field> =` here gets its import, its Components field and its
//     New(Deps{…}) call injected;
//   - the Deps literal KEY set: a component that GAINED a Deps field gets that
//     assignment filled, and a key naming a field its Deps struct no longer HAS
//     is dropped. That key cannot compile against any providers.go, so keeping
//     it would only mean `forge generate` could never heal a struct you
//     refactored. The value beside a key that still exists is never touched.
//
// When nothing here is yours, forge converges the whole file to the render a
// fresh generate would write. `forge project disown internal/app/compose.go`
// stops all of it.
// Source: each component's Deps struct + internal/app/providers.go (Infra).
//
// compose.go is the EXPLICIT per-binary component construction site. There is
// NO by-type DI injector and NO *Services god-struct: Components is a plain
// typed bag, and NewComponents constructs every handler / worker / operator as
// a LOCAL value with its Deps wired INLINE off the owned *Infra (providers.go).
// Adding a component re-runs `forge generate`, which reconciles this file
// against the discovered component set — explicit, reviewable, no string filter
// and no hidden construction order.
//
// To change WHAT fills a Deps field, edit internal/app/providers.go (the owned
// Infra + OpenInfra). To customize the composition by hand (a carved cross-edge,
// a late-bound setter), `forge project disown internal/app/compose.go` and
// own the bytes.
package app

import (
	"fmt"
	"log/slog"

	tasks "example.com/app/internal/handlers/tasks"
	ticker "example.com/app/internal/workers/ticker"
)

// keep slog imported for the Infra.Log field type even when no component
// names it directly in a constructor literal.
var _ = slog.LevelInfo

// Components holds every constructed component instance for one binary, each
// typed as its concrete handler/worker/operator type. It is a plain bag the
// cmd layer mounts/supervises from — there is no by-type resolution and no
// hidden construction order; NewComponents below is the single, explicit site.
type Components struct {
	Tasks  *tasks.Service
	Ticker *ticker.Worker
}

// NewComponents constructs the full per-binary component closure from the
// owned *Infra. Every component is constructed exactly once (per-binary
// singleton) with its Deps wired INLINE — explicit and reviewable.
// Construction order is type-topological: a component is built after every
// collaborator it depends on by type.
func NewComponents(infra *Infra) (*Components, error) {
	c := &Components{}
	tasksInst, err := tasks.New(tasks.Deps{
		DB:     infra.ORM,
		Logger: infra.Log,
		Config: infra.Cfg,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Tasks: %w", err)
	}
	c.Tasks = tasksInst
	c.Ticker = ticker.New(ticker.Deps{
		Logger: infra.Log,
		Config: infra.Cfg,
	})
	return c, nil
}
