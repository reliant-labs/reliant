// yours: scaffolded once, never touched again — forge will not overwrite this file
// The OWNED observability extension seam for this package. Canonical pattern
// in `forge agent skill load observability`.
package ghblobs

import (
	"log/slog"

	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"
)

// newObserveChain assembles the in-process middleware chain that the GENERATED
// decorator (middleware_gen.go) routes every ghblobs Service method through:
// recovery, one span, call/error/duration metrics, then the log. No arguments
// or results are captured, only method identity, duration and error status.
//
// A blob GitHub does not have (or does not show the viewer), one over the
// size bound, and a token GitHub refused are answers the coverage page shows
// ("source not available"), not failures: they log like a success, with
// expected=true.
func newObserveChain() *observe.ComponentChain {
	scope := "github.com/reliant-labs/reliant/internal/integrations/ghblobs"
	logger := slog.Default()
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "ghblobs"),
		observe.LogMiddleware(logger, slog.LevelDebug, observe.WithExpectedErrors(ErrNotFound, ErrTooLarge, ErrTokenRejected)),
	)
}
