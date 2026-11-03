package observe

import (
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// declaredOpts returns the unit, description and (for a histogram) bucket
// boundary options of a metric the default
// registry has declared, typed for one instrument constructor. The built-in
// instruments are created from the caller's meter (tests and callers pass their
// own), so they bypass the registry's guard — forge controls their attributes —
// but take their identity from the same declaration the schema prints.
func declaredOpts[T any](name string) []T {
	s, ok := metrics.Default().Lookup(name)
	if !ok {
		return nil
	}
	opts := []any{metric.WithUnit(s.Unit), metric.WithDescription(s.Description)}
	if len(s.Buckets) > 0 {
		// Only a histogram option type accepts boundaries; the type switch
		// below drops them for any other instrument.
		opts = append(opts, metric.WithExplicitBucketBoundaries(s.Buckets...))
	}
	out := make([]T, 0, len(opts))
	for _, o := range opts {
		if t, ok := o.(T); ok {
			out = append(out, t)
		}
	}
	return out
}
