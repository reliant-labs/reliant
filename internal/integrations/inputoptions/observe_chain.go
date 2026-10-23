// yours: scaffolded once, never touched again — forge will not overwrite this file
// The OWNED observability extension seam for this package. Canonical pattern
// in `forge skill load observability`.
package inputoptions

import (
	"log/slog"

	"go.opentelemetry.io/otel"

	"github.com/reliant-labs/forge/pkg/observe"
)

// newObserveChain assembles the in-process middleware chain that the GENERATED
// decorator (middleware_gen.go) routes every inputoptions Service method
// through: recovery, one span per call, call metrics, and the log.
//
// A request this package refuses — a scope that is missing or not a valid
// value (ErrBadScope), an unknown type or connection — is the caller's error
// (svcerr InvalidArgument / NotFound, so error_class=user) and goes back to the
// client with its message. It logs at WARN rather than forge's default INFO: a
// form that keeps sending a bad scope is a UI bug worth seeing, but not an
// ERROR or a Sentry event — "invalid scope: repository is not a valid
// repository" was 8 ERRORs on 2026-10-21, before these errors were typed.
func newObserveChain() *observe.ComponentChain {
	scope := "github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	logger := slog.Default()
	return observe.NewComponentChain(
		observe.RecoverMiddleware(logger),
		observe.TraceMiddleware(otel.Tracer(scope)),
		observe.MetricsMiddleware(otel.Meter(scope), "inputoptions"),
		observe.LogMiddleware(logger, slog.LevelDebug, observe.WithUserErrorLevel(slog.LevelWarn)),
	)
}
