// yours: scaffolded once, never touched again — forge will not overwrite this file
// The OWNED observability extension seam for this package. Canonical pattern
// in `forge skill load observability`.
package gitcredentialclient

import (
	"log/slog"

	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"
)

// newObserveChain assembles the in-process middleware chain that the GENERATED
// decorator (middleware_gen.go) routes every gitcredentialclient Service method
// through. Spans, metrics, structured logging and panic-recovery are applied
// uniformly — NO arguments or results are captured, only method identity,
// duration, and error status.
//
// THIS IS THE EXTENSION SEAM. The generated decorator is uniform and never
// needs editing; shape observability HERE:
//
//   - add a project middleware (implement observe.ComponentMiddleware and
//     append it — e.g. an in-process timeout or rate-limit layer);
//   - drop a layer you don't want (delete its line — nil is also dropped);
//   - change the success log level (the observe.LogMiddleware argument), or
//     tune success logging with trailing options: one method's level
//     (observe.WithSuccessLevel("gitcredentialclient.Method", slog.LevelInfo)), a
//     success-sampling window for this package (observe.WithSuccessSampling),
//     the slow line (observe.WithSlowThreshold);
//   - declare the errors that are an expected OUTCOME of a call, not a
//     failure — an answer the caller acts on, like "no such object" behind
//     a 404: observe.WithExpectedErrors(ErrNotFound) on LogMiddleware, or
//     observe.WithExpectedErrorFunc for a classification no sentinel
//     expresses. They log like a success, with the error and expected=true.
//     Nothing is expected until declared here.
//
// Middlewares run OUTER→INNER in the order listed: recovery wraps everything
// (a component panic becomes an error, never a crash), then one OTel span per
// call, then call/error/duration metrics, then the log: every failure at
// Error (a declared expected error is not a failure); every success at the
// level below — quiet under a production Info handler. With
// observe.WithSuccessSampling(d) appended to LogMiddleware, successes are
// sampled per method instead: the first, then at most one per window with
// suppressed=<n>. Any call over 1s is always written, with slow=true.
//
// To opt the package OUT, put `// forge:no-observe` on the package clause (or
// on a single Service method to skip just that one). The opt-out wins over
// every opt-in.
//
// Deleting this file does NOT opt out. A scaffold stamps BOTH the
// `// forge:constructor` marker on `func New` and this seam, and either one
// alone still instruments the package (see ShouldInstrumentComponent) — the
// seam exists only so projects predating the marker keep working. The marker
// is the current mechanism.
func newObserveChain() *observe.ComponentChain {
	scope := "github.com/reliant-labs/reliant/internal/gitcredentialclient"
	logger := slog.Default()
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "gitcredentialclient"),
		observe.LogMiddleware(logger, slog.LevelDebug),
	)
}
