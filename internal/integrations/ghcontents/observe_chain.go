// yours: scaffolded once, never touched again — forge will not overwrite this file
// The OWNED observability extension seam for this package. Canonical pattern
// in `forge agent skill load observability`.
package ghcontents

import (
	"log/slog"

	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"
)

// newObserveChain assembles the in-process middleware chain that the GENERATED
// decorator (middleware_gen.go) routes every ghcontents Service method through:
// recovery, one span, call/error/duration metrics, then the log. No arguments
// or results are captured, only method identity, duration and error status.
//
// A file the ref does not have, and one over the size bound, are answers the
// projector records as the repository's config error, not failures: they log
// like a success, with expected=true.
func newObserveChain() *observe.ComponentChain {
	scope := "github.com/reliant-labs/reliant/internal/integrations/ghcontents"
	logger := slog.Default()
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "ghcontents"),
		observe.LogMiddleware(logger, slog.LevelDebug, observe.WithExpectedErrors(ErrNotFound, ErrTooLarge)),
	)
}
