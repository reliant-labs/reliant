// The OWNED observability extension seam for this package. Canonical pattern
// in `forge agent skill load observability`.
package ghrest

import (
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// newObserveChain assembles the in-process middleware chain the generated
// decorator (middleware_gen.go) routes every Service method through: panic
// recovery, one span per call, metrics, and a log line. A missing ref or PR,
// an already-merged PR and an existing ref are answers the queue acts on, not
// failures, so they log as expected outcomes.
func newObserveChain() *observe.ComponentChain {
	scope := "github.com/reliant-labs/reliant/internal/mergequeue/ghrest"
	logger := slog.Default()
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "ghrest"),
		observe.LogMiddleware(logger, slog.LevelDebug, observe.WithExpectedErrorFunc(func(err error) bool {
			return errors.Is(err, ghport.ErrNotFound) || errors.Is(err, ghport.ErrAlreadyMerged) || errors.Is(err, ghport.ErrAlreadyExists)
		})),
	)
}
