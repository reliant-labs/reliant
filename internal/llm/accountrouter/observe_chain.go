// yours: scaffolded once, never touched again — forge will not overwrite this file
// The OWNED observability extension seam for this package. Canonical pattern
// in `forge skill load observability`.
package accountrouter

import (
	"log/slog"

	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"
)

// newObserveChain assembles the in-process middleware chain that the GENERATED
// decorator (middleware_gen.go) routes every accountrouter Service method
// through: recovery, one span per call, call metrics, and the log.
//
// The one change from forge's default chain is the level of a user error.
// Assign's *ErrAllLimited — every account of the provider is rate-limited —
// is a state of the user's subscriptions, not a fault (ErrAllLimited.ErrorClass),
// and the caller parks the run until the reset. It is still worth seeing in a
// WARN view, so user errors log at WARN here rather than the default INFO. It
// was an ERROR, and a Sentry event, on every assignment while a subscription
// was spent: 72 in two days (2026-10-21..23).
func newObserveChain() *observe.ComponentChain {
	scope := "github.com/reliant-labs/reliant/internal/llm/accountrouter"
	logger := slog.Default()
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "accountrouter"),
		observe.LogMiddleware(logger, slog.LevelDebug, observe.WithUserErrorLevel(slog.LevelWarn)),
	)
}
