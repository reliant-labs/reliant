package observe

import (
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// declaredOpts returns the unit and description options of a metric the default
// registry has declared, typed for one instrument constructor. The built-in
// instruments are created from the caller's meter (tests and callers pass their
// own), so they bypass the registry's guard — forge controls their attributes —
// but take their identity from the same declaration the schema prints.
func declaredOpts[T any](name string) []T {
	s, ok := metrics.Default().Lookup(name)
	if !ok {
		return nil
	}
	opts := []metric.InstrumentOption{metric.WithUnit(s.Unit), metric.WithDescription(s.Description)}
	out := make([]T, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.(T))
	}
	return out
}
