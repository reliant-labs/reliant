---
name: observability/metrics
description: Declaring your own metrics with forge/pkg/observe/metrics — the Spec (name, type, UCUM unit, attribute keys, stability, since), the declared-attribute check, and which gauge to use. A per-entity value (per tenant, per job this replica leads) is a KeyedGauge you Set and Delete, so its series stops when the entity goes; a value cheapest to read when asked for is an observable gauge with a callback; RegisterCallback observes several at once.
---

# Declaring your own metrics

Your metrics live in `internal/metrics/metrics.go`. Each one is declared on
`metrics.Default()` with a `metrics.Spec` — name, UCUM unit, description, the
attribute keys it may carry (optionally with their closed set of values),
stability and `since` — and the registry returns the standard OpenTelemetry
instrument, so recording is `c.Add(ctx, 1, metric.WithAttributes(...))`.

An attribute the metric did not declare panics under `ENVIRONMENT=development`,
at the line that recorded it, and elsewhere is dropped (a value outside an
enumerated set becomes `_other`) and counted on
`forge.metrics.rejected_attributes`. A stray user id never becomes a series per
user. The declarations are the schema: `<bin> metrics schema --json`,
`GET /debug/metrics/schema` (the pprof listener) and
`forge obs metrics declared [--binary|--url]` print it; `forge build` writes
`bin/observability/metrics-schema.json`, which a release carries as
`observability/metrics-schema.json`; `forge obs metrics declared --dashboard`
draws the Custom metrics dashboard from it. In an older project,
`forge generate` births `cmd/<bin>/cmd/metrics.go` and
`internal/metrics/metrics.go` when they are absent, and never touches an
existing one.

## Gauges: pick the shape by whether a series can die

All three declare as `type: gauge` in the schema and check attributes like every
other instrument.

| Constructor | For | A series stops being exported |
|---|---|---|
| `Int64Gauge` / `Float64Gauge` | one process-wide value recorded when it changes | never: the SDK exports its last value forever |
| `Int64KeyedGauge` / `Float64KeyedGauge` | a value per entity that can go away (per tenant, per job this replica leads) | when you `Delete` it, or `DeleteMatching` every series carrying one attribute |
| `Int64ObservableGauge` / `Float64ObservableGauge` | a value cheapest to read when asked for (a pool's size, a cache's length) | when its callback stops observing it |

The trap is the first row with an entity attribute. A synchronous gauge
labelled `job="ns/a"` keeps exporting ns/a's last value after this replica hands
the job to another, so two replicas report the same job and dashboards sum both.

### Keyed gauge

```go
var jobGeneration = metrics.Default().Int64KeyedGauge(metrics.Spec{
	Name:        "job_generation",
	Unit:        "{generation}",
	Description: "Latest generation published, per job this replica leads.",
	Attributes:  []metrics.Attribute{{Key: "job"}},
	Since:       "v0.1.0",
})

// RecordPublished records a generation this replica published.
func RecordPublished(ctx context.Context, job string, gen int64) {
	jobGeneration.Set(ctx, gen, attribute.String("job", job))
}

// ForgetJob stops reporting job: the new leader reports it now.
func ForgetJob(job string) {
	jobGeneration.Delete(attribute.String("job", job))
}
```

`Set` replaces the series' value (it never adds). Its attributes are checked
there, at the call, not at collection time. `Delete` takes the same attributes
as `Set` and finds the same series, even ones production corrected.
`DeleteMatching(attribute.String("job", job))` removes every series of the job
whatever its other attributes; `DeleteMatching()` removes them all. `Len()`
says how many series are exported.

### Observable gauge

```go
var poolInUse = metrics.Default().Int64ObservableGauge(metrics.Spec{
	Name: "pool_in_use", Unit: "{connection}", Description: "Connections checked out.", Since: "v0.1.0",
}, func(_ context.Context, o metric.Int64Observer) error {
	o.Observe(int64(pool.Stat().AcquiredConns()))
	return nil
})
```

Each callback's observations pass the attribute check (in development an
undeclared attribute panics inside the callback). To observe several observable
gauges from one callback, under one lock, register it on the registry:

```go
gen := metrics.Default().Int64ObservableGauge(genSpec)
imb := metrics.Default().Float64ObservableGauge(imbSpec)
_, err := metrics.Default().RegisterCallback(func(_ context.Context, o metric.Observer) error {
	o.ObserveInt64(gen, g, metric.WithAttributes(attribute.String("job", job)))
	o.ObserveFloat64(imb, i, metric.WithAttributes(attribute.String("job", job)))
	return nil
}, gen, imb)
```

Use it rather than an `otel.Meter`'s `RegisterCallback`: it checks every
observation's attributes, refuses an instrument the registry did not declare,
and a meter of another scope refuses the registry's instruments anyway.

## Instruments the registry cannot create

A metric some other code creates — a library's own meter, an observable counter
— is still listed in the schema with `metrics.Default().Describe(spec)`, a
declaration without an instrument, setting `Spec.Type` yourself.
