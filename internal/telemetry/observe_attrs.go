// Copyright (c) 2026 Reliant Labs
package telemetry

// The names a workflow node's observe: block emits under
// (internal/workflow/observe; dev-docs/design/workflow-observe.md, section 6).
// An author names the keys, events and metrics; these are the platform's
// prefixes and the instruments that report on the feature itself. Like
// ai_attrs.go's, they are a public contract once released: never rename one in
// place, add the new name beside it.

// AttrObservePrefix prefixes every attribute an observe block sets on its
// node's workflow.node span: `attributes: {trigger: ...}` is
// reliant.observe.trigger. The prefix is what keeps an author's key from
// colliding with gen_ai.*, forge.ai.*, org.id or the rest of reliant.*.
const AttrObservePrefix = "reliant.observe."

// The node span's own record of how its observe block evaluated, set only
// when there is something to say.
const (
	// AttrNodeObserveErrors is how many of the block's expressions failed
	// (each one dropped its key, event or metric). The messages are in the
	// node's stored observation, never on the span.
	AttrNodeObserveErrors = "reliant.node.observe.errors"
	// AttrNodeObserveTruncated reports a block cut by a cap: a count, the
	// evaluation budget, or the stored row's size.
	AttrNodeObserveTruncated = "reliant.node.observe.truncated"
)

// MetricObservedPrefix prefixes every metric an observe block declares:
// `metrics: [{name: rc_train.ticks}]` is reliant.observed.rc_train.ticks.
const MetricObservedPrefix = "reliant.observed."

// ObserveMeterName is the instrumentation scope of every observe metric, the
// authors' and the feature's own.
const ObserveMeterName = "github.com/reliant-labs/reliant/internal/workflow/observe"

// The feature's own instruments.
const (
	// MetricObserveErrors counts expressions that failed, by
	// AttrObserveErrorKind.
	MetricObserveErrors = "reliant.observe.errors"
	// MetricObserveOverflow counts data points whose attribute set was new
	// after the metric (or the process) had reached its series cap, and was
	// recorded as AttrObserveOverflow=true instead; by AttrObserveMetric.
	MetricObserveOverflow = "reliant.observe.overflow"
	// MetricObserveDropped counts observations that were not stored, by
	// AttrObserveDropReason. Their spans and metrics were still emitted.
	MetricObserveDropped = "reliant.observe.dropped"
)

// The attributes of the feature's own instruments, and the one an overflowed
// data point carries.
const (
	AttrObserveErrorKind  = "kind"
	AttrObserveMetric     = "metric"
	AttrObserveDropReason = "reason"
	AttrObserveOverflow   = "overflow"
)

// Values of AttrObserveErrorKind.
const (
	// ObserveErrorEval: an expression failed to compile or evaluate, or
	// produced a value of the wrong type (a when: that is not a bool, an
	// each: that is not a list, a metric value that is not a number).
	ObserveErrorEval = "eval"
	// ObserveErrorBudget: the block ran out of its evaluation budget and the
	// rest of it was skipped.
	ObserveErrorBudget = "budget"
	// ObserveErrorPanic: evaluation panicked and was recovered.
	ObserveErrorPanic = "panic"
	// ObserveErrorKindConflict: a metric name was declared with a kind other
	// than the one this process first created it with.
	ObserveErrorKindConflict = "kind_conflict"
	// ObserveErrorWrite: the observation could not be stored.
	ObserveErrorWrite = "write"
)

// Values of AttrObserveDropReason.
const (
	// ObserveDropRowCap: the run already held its cap of stored observations.
	ObserveDropRowCap = "row_cap"
	// ObserveDropWriteFailed: the store refused or did not answer.
	ObserveDropWriteFailed = "write_failed"
)

// ObserveWithheldFormat is the span or event value that stands in for a
// string an author's expression produced that is not code-shaped: the length
// in bytes, never the text. The text is in the stored observation.
const ObserveWithheldFormat = "[withheld:%db]"

// ObserveMetricOther stands in, on a metric, for an attribute value that is
// not a bool, an integer or a code-shaped string, so a bad value cannot split
// a series.
const ObserveMetricOther = "other"
