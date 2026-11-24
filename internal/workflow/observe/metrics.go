// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"context"
	"sort"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// The third cardinality layer (design section 6): per process, how many
// distinct attribute sets one metric, and all of them together, may record.
// A new set past either cap is recorded as {overflow="true"} instead and
// counted (telemetry.MetricObserveOverflow). A restart resets the sets, so a
// backend sees at most (processes x MaxSeriesPerMetric) series of a metric: a
// bound, not a guess.
const (
	MaxSeriesPerMetric  = 64
	MaxSeriesPerProcess = 4096
)

// MetricBase is the platform's attributes on every observe metric: which
// workflow and node recorded it. Never an org, user, chat, run or thread id:
// an id on a metric is unbounded by definition (the first cardinality layer).
type MetricBase struct {
	WorkflowName string
	NodeID       string
	NodeType     string
}

func (b MetricBase) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(telemetry.AttrAIWorkflowName, b.WorkflowName),
		attribute.String(telemetry.AttrAINodeID, b.NodeID),
		attribute.String(telemetry.AttrAINodeType, b.NodeType),
	}
}

// Recorder emits observe metrics on one meter, with the cardinality caps, and
// the feature's own counters. Safe for concurrent use.
type Recorder struct {
	meter metric.Meter

	errors   metric.Int64Counter
	overflow metric.Int64Counter
	dropped  metric.Int64Counter

	mu          sync.Mutex
	instruments map[string]*instrument
	series      map[string]map[attribute.Distinct]struct{}
	total       int
	perMetric   int
	perProcess  int
}

type instrument struct {
	kind      string
	counter   metric.Float64Counter
	histogram metric.Float64Histogram
	gauge     metric.Float64Gauge
}

// NewRecorder builds a Recorder on meter.
func NewRecorder(meter metric.Meter) *Recorder {
	r := &Recorder{
		meter:       meter,
		instruments: map[string]*instrument{},
		series:      map[string]map[attribute.Distinct]struct{}{},
		perMetric:   MaxSeriesPerMetric,
		perProcess:  MaxSeriesPerProcess,
	}
	var err error
	// An instrument the SDK refuses still comes back usable (a no-op).
	r.errors, err = meter.Int64Counter(telemetry.MetricObserveErrors,
		metric.WithUnit("{expression}"),
		metric.WithDescription("Observe-block expressions that failed to evaluate, by kind."))
	handle(err)
	r.overflow, err = meter.Int64Counter(telemetry.MetricObserveOverflow,
		metric.WithUnit("{point}"),
		metric.WithDescription("Observe metric points recorded as overflow because their metric reached its series cap, by metric."))
	handle(err)
	r.dropped, err = meter.Int64Counter(telemetry.MetricObserveDropped,
		metric.WithUnit("{observation}"),
		metric.WithDescription("Node observations that were not stored, by reason; their spans and metrics were still emitted."))
	handle(err)
	return r
}

// handle reports an instrument the SDK refused (it still came back usable, a
// no-op).
func handle(err error) {
	if err != nil {
		otel.Handle(err)
	}
}

var (
	defaultRecorderOnce sync.Once
	defaultRecorder     *Recorder
)

// DefaultRecorder is the process's Recorder, on the global meter provider
// (whose instruments forward to the provider the process installs, even when
// taken before it is installed).
func DefaultRecorder() *Recorder {
	defaultRecorderOnce.Do(func() {
		defaultRecorder = NewRecorder(otel.Meter(telemetry.ObserveMeterName))
	})
	return defaultRecorder
}

// Record emits the points a node's block produced, each as
// telemetry.MetricObservedPrefix + its name, labelled with base and its own
// attributes (MetricAttributeValue). An author key that names a base
// attribute is dropped: the platform's identity of the node wins.
func (r *Recorder) Record(ctx context.Context, base MetricBase, points []MetricPoint) {
	for _, p := range points {
		inst, ok := r.instrument(p)
		if !ok {
			r.CountErrors(ctx, []Error{{Kind: telemetry.ObserveErrorKindConflict}})
			continue
		}
		name := telemetry.MetricObservedPrefix + p.Name
		set, overflowed := r.admit(name, metricAttributes(base, p.Attributes))
		if overflowed {
			r.overflow.Add(ctx, 1, metric.WithAttributes(attribute.String(telemetry.AttrObserveMetric, name)))
		}
		opt := metric.WithAttributeSet(set)
		switch inst.kind {
		case model.ObserveKindCounter:
			inst.counter.Add(ctx, p.Value, opt)
		case model.ObserveKindHistogram:
			inst.histogram.Record(ctx, p.Value, opt)
		case model.ObserveKindGauge:
			inst.gauge.Record(ctx, p.Value, opt)
		}
	}
}

// CountErrors counts errs by kind (telemetry.MetricObserveErrors).
func (r *Recorder) CountErrors(ctx context.Context, errs []Error) {
	byKind := map[string]int64{}
	for _, e := range errs {
		byKind[e.Kind]++
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		r.errors.Add(ctx, byKind[k], metric.WithAttributes(attribute.String(telemetry.AttrObserveErrorKind, k)))
	}
}

// CountDropped counts one observation that was not stored, for reason (a
// telemetry.ObserveDrop* value).
func (r *Recorder) CountDropped(ctx context.Context, reason string) {
	r.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String(telemetry.AttrObserveDropReason, reason)))
}

// instrument returns p's instrument, creating it on first use. A name this
// process first created with another kind is refused: one name, one
// instrument.
func (r *Recorder) instrument(p MetricPoint) (*instrument, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if inst, ok := r.instruments[p.Name]; ok {
		return inst, inst.kind == p.Kind
	}
	name := telemetry.MetricObservedPrefix + p.Name
	desc := "Declared by a workflow's observe: block."
	inst := &instrument{kind: p.Kind}
	var err error
	switch p.Kind {
	case model.ObserveKindCounter:
		o := []metric.Float64CounterOption{metric.WithDescription(desc)}
		if p.Unit != "" {
			o = append(o, metric.WithUnit(p.Unit))
		}
		inst.counter, err = r.meter.Float64Counter(name, o...)
	case model.ObserveKindHistogram:
		o := []metric.Float64HistogramOption{metric.WithDescription(desc)}
		if p.Unit != "" {
			o = append(o, metric.WithUnit(p.Unit))
		}
		inst.histogram, err = r.meter.Float64Histogram(name, o...)
	case model.ObserveKindGauge:
		o := []metric.Float64GaugeOption{metric.WithDescription(desc)}
		if p.Unit != "" {
			o = append(o, metric.WithUnit(p.Unit))
		}
		inst.gauge, err = r.meter.Float64Gauge(name, o...)
	default:
		return nil, false
	}
	handle(err)
	r.instruments[p.Name] = inst
	return inst, true
}

// admit returns attrs as a set when it is one the metric has recorded before,
// or there is room for a new one; otherwise the overflow set, and true.
func (r *Recorder) admit(name string, attrs []attribute.KeyValue) (attribute.Set, bool) {
	set := attribute.NewSet(attrs...)
	key := set.Equivalent()
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := r.series[name]
	if _, ok := seen[key]; ok {
		return set, false
	}
	if len(seen) >= r.perMetric || r.total >= r.perProcess {
		return attribute.NewSet(attribute.Bool(telemetry.AttrObserveOverflow, true)), true
	}
	if seen == nil {
		seen = map[attribute.Distinct]struct{}{}
		r.series[name] = seen
	}
	seen[key] = struct{}{}
	r.total++
	return set, false
}

func metricAttributes(base MetricBase, author map[string]interface{}) []attribute.KeyValue {
	out := base.attributes()
	reserved := map[string]bool{}
	for _, kv := range out {
		reserved[string(kv.Key)] = true
	}
	keys := make([]string, 0, len(author))
	for k := range author {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if reserved[k] || k == telemetry.AttrObserveOverflow {
			continue
		}
		out = append(out, attribute.KeyValue{Key: attribute.Key(k), Value: MetricAttributeValue(author[k])})
	}
	return out
}
