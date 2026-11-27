package metrics

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
)

// Observable gauges report a value read at collection time, from a callback,
// instead of one recorded when it changes. A series is exported only while a
// callback observes it, so — unlike a synchronous gauge, whose last value the
// SDK exports forever — a series nobody observes any more disappears.
//
// KeyedGauge is the shape most callers want: a gauge whose series are Set and
// Deleted by attribute set, one per entity (a queue's depth per tenant, a
// job's generation on the replica that leads it), backed by one observable
// callback. Int64ObservableGauge and Float64ObservableGauge are the primitive
// beneath it, for a value that is cheapest to read when asked for.
//
// Both are declared exactly like the registry's other instruments: Type is
// Gauge in the schema, and every observation passes the declared-attribute
// check.

// observeOpts is addOpts for an observation.
func (g *guard) observeOpts(ctx context.Context, opts []metric.ObserveOption) []metric.ObserveOption {
	set := metric.NewObserveConfig(opts).Attributes()
	if fixed, changed := g.check(ctx, set); changed {
		return []metric.ObserveOption{metric.WithAttributeSet(fixed)}
	}
	return opts
}

// int64Observer is the observer a registry callback receives: the SDK's, with
// the declared attribute check in front of it.
type int64Observer struct {
	embedded.Int64Observer
	ctx   context.Context
	g     *guard
	inner metric.Int64Observer
}

func (o *int64Observer) Observe(v int64, opts ...metric.ObserveOption) {
	o.inner.Observe(v, o.g.observeOpts(o.ctx, opts)...)
}

type float64Observer struct {
	embedded.Float64Observer
	ctx   context.Context
	g     *guard
	inner metric.Float64Observer
}

func (o *float64Observer) Observe(v float64, opts ...metric.ObserveOption) {
	o.inner.Observe(v, o.g.observeOpts(o.ctx, opts)...)
}

// Int64ObservableGauge declares s as a gauge whose value the callbacks observe
// at collection time, and returns its instrument. Each callback's observations
// pass the declared-attribute check (in development an undeclared attribute
// panics inside the callback; elsewhere it is corrected and counted). A series
// the callbacks stop observing stops being exported.
//
// To observe several instruments from one callback, register it with
// Registry.RegisterCallback. For per-entity series you Set and Delete, use
// Int64KeyedGauge.
func (r *Registry) Int64ObservableGauge(s Spec, callbacks ...metric.Int64Callback) metric.Int64ObservableGauge {
	g, opts := r.instrument(s, Gauge)
	o := counterOpts[metric.Int64ObservableGaugeOption](opts)
	for _, cb := range callbacks {
		if cb == nil {
			continue
		}
		o = append(o, metric.WithInt64Callback(func(ctx context.Context, obs metric.Int64Observer) error {
			return cb(ctx, &int64Observer{ctx: ctx, g: g, inner: obs})
		}))
	}
	inst, err := r.meter.Int64ObservableGauge(s.Name, o...)
	inst = mustCreate(inst, err, s.Name)
	r.trackObservable(inst, g)
	return inst
}

// Float64ObservableGauge is Int64ObservableGauge for fractional values.
func (r *Registry) Float64ObservableGauge(s Spec, callbacks ...metric.Float64Callback) metric.Float64ObservableGauge {
	g, opts := r.instrument(s, Gauge)
	o := counterOpts[metric.Float64ObservableGaugeOption](opts)
	for _, cb := range callbacks {
		if cb == nil {
			continue
		}
		o = append(o, metric.WithFloat64Callback(func(ctx context.Context, obs metric.Float64Observer) error {
			return cb(ctx, &float64Observer{ctx: ctx, g: g, inner: obs})
		}))
	}
	inst, err := r.meter.Float64ObservableGauge(s.Name, o...)
	inst = mustCreate(inst, err, s.Name)
	r.trackObservable(inst, g)
	return inst
}

// trackObservable remembers which declaration an observable instrument
// belongs to, for RegisterCallback. An instrument whose type cannot be a map
// key (no known meter returns one) is simply not tracked; RegisterCallback
// then refuses it.
func (r *Registry) trackObservable(inst metric.Observable, g *guard) {
	if !reflect.TypeOf(inst).Comparable() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observables == nil {
		r.observables = map[metric.Observable]*guard{}
	}
	r.observables[inst] = g
}

// RegisterCallback registers f to observe several of the registry's
// observable instruments at once, as metric.Meter.RegisterCallback does, with
// the declared-attribute check on every observation. Every instrument must
// have been created by this registry (Int64ObservableGauge,
// Float64ObservableGauge): an undeclared one is an error, as is anything the
// underlying meter refuses.
func (r *Registry) RegisterCallback(f metric.Callback, instruments ...metric.Observable) (metric.Registration, error) {
	if f == nil {
		return nil, errors.New("metrics: RegisterCallback: callback is nil")
	}
	guards := make(map[metric.Observable]*guard, len(instruments))
	r.mu.Lock()
	for _, inst := range instruments {
		var g *guard
		if inst != nil && reflect.TypeOf(inst).Comparable() {
			g = r.observables[inst]
		}
		if g == nil {
			r.mu.Unlock()
			return nil, fmt.Errorf("metrics: RegisterCallback: an instrument was not created by this registry; declare it with Int64ObservableGauge or Float64ObservableGauge")
		}
		guards[inst] = g
	}
	r.mu.Unlock()
	return r.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		return f(ctx, &observer{ctx: ctx, inner: o, guards: guards})
	}, instruments...)
}

// observer is the metric.Observer a RegisterCallback callback receives.
type observer struct {
	embedded.Observer
	ctx    context.Context
	inner  metric.Observer
	guards map[metric.Observable]*guard
}

func (o *observer) guardOf(inst metric.Observable) *guard {
	if inst == nil || !reflect.TypeOf(inst).Comparable() {
		return nil
	}
	return o.guards[inst]
}

func (o *observer) ObserveInt64(inst metric.Int64Observable, v int64, opts ...metric.ObserveOption) {
	if g := o.guardOf(inst); g != nil {
		opts = g.observeOpts(o.ctx, opts)
	}
	o.inner.ObserveInt64(inst, v, opts...)
}

func (o *observer) ObserveFloat64(inst metric.Float64Observable, v float64, opts ...metric.ObserveOption) {
	if g := o.guardOf(inst); g != nil {
		opts = g.observeOpts(o.ctx, opts)
	}
	o.inner.ObserveFloat64(inst, v, opts...)
}

// KeyedGauge is a gauge with one series per attribute set, each Set and
// Deleted explicitly: the gauge for a value that belongs to an entity which
// can go away. A synchronous gauge exports its last value for an attribute set
// forever, so a per-tenant or per-job series outlives its tenant or job; a
// KeyedGauge series stops being exported the moment it is Deleted. It is safe
// for concurrent use.
//
//	queueDepth := metrics.Default().Int64KeyedGauge(metrics.Spec{
//		Name: "queue_depth", Unit: "{message}", Since: "v1.2.0",
//		Description: "Messages waiting, per tenant this replica serves.",
//		Attributes:  []metrics.Attribute{{Key: "tenant"}},
//	})
//	queueDepth.Set(ctx, 42, attribute.String("tenant", id))
//	queueDepth.Delete(attribute.String("tenant", id)) // tenant moved away
type KeyedGauge[N int64 | float64] struct {
	g *guard

	mu     sync.Mutex
	series map[attribute.Distinct]keyedPoint[N]
}

type keyedPoint[N int64 | float64] struct {
	set attribute.Set
	v   N
}

// Int64KeyedGauge declares s as a gauge and returns it as a KeyedGauge whose
// series are observed by one callback.
func (r *Registry) Int64KeyedGauge(s Spec) *KeyedGauge[int64] {
	k := newKeyedGauge[int64]()
	g, opts := r.instrument(s, Gauge)
	k.g = g
	o := append(counterOpts[metric.Int64ObservableGaugeOption](opts),
		metric.WithInt64Callback(func(_ context.Context, obs metric.Int64Observer) error {
			for _, p := range k.snapshot() {
				obs.Observe(p.v, metric.WithAttributeSet(p.set))
			}
			return nil
		}))
	inst, err := r.meter.Int64ObservableGauge(s.Name, o...)
	mustCreate(inst, err, s.Name)
	return k
}

// Float64KeyedGauge is Int64KeyedGauge for fractional values.
func (r *Registry) Float64KeyedGauge(s Spec) *KeyedGauge[float64] {
	k := newKeyedGauge[float64]()
	g, opts := r.instrument(s, Gauge)
	k.g = g
	o := append(counterOpts[metric.Float64ObservableGaugeOption](opts),
		metric.WithFloat64Callback(func(_ context.Context, obs metric.Float64Observer) error {
			for _, p := range k.snapshot() {
				obs.Observe(p.v, metric.WithAttributeSet(p.set))
			}
			return nil
		}))
	inst, err := r.meter.Float64ObservableGauge(s.Name, o...)
	mustCreate(inst, err, s.Name)
	return k
}

func newKeyedGauge[N int64 | float64]() *KeyedGauge[N] {
	return &KeyedGauge[N]{series: map[attribute.Distinct]keyedPoint[N]{}}
}

// Set makes v the value of the series attrs identify, creating it if absent.
// The attributes are checked against the declaration here, at the call that
// set them: in development an undeclared one panics; elsewhere it is corrected
// (and counted) before the series is stored.
func (k *KeyedGauge[N]) Set(ctx context.Context, v N, attrs ...attribute.KeyValue) {
	set, _ := k.g.check(ctx, attribute.NewSet(attrs...))
	k.mu.Lock()
	k.series[set.Equivalent()] = keyedPoint[N]{set: set, v: v}
	k.mu.Unlock()
}

// Delete removes the series attrs identify, so it is no longer exported. The
// attributes are checked and corrected as Set does, so Delete finds the
// series the same attributes Set. Deleting an absent series does nothing.
func (k *KeyedGauge[N]) Delete(attrs ...attribute.KeyValue) {
	set, _ := k.g.check(context.Background(), attribute.NewSet(attrs...))
	k.mu.Lock()
	delete(k.series, set.Equivalent())
	k.mu.Unlock()
}

// DeleteMatching removes every series that carries all of attrs — every
// series of one job, whatever its other attributes. With no attrs it removes
// every series.
func (k *KeyedGauge[N]) DeleteMatching(attrs ...attribute.KeyValue) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for key, p := range k.series {
		if carriesAll(p.set, attrs) {
			delete(k.series, key)
		}
	}
}

// Len reports how many series the gauge currently exports.
func (k *KeyedGauge[N]) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.series)
}

// snapshot copies the series out, so the callback observes without holding
// the gauge's lock.
func (k *KeyedGauge[N]) snapshot() []keyedPoint[N] {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]keyedPoint[N], 0, len(k.series))
	for _, p := range k.series {
		out = append(out, p)
	}
	return out
}

func carriesAll(set attribute.Set, attrs []attribute.KeyValue) bool {
	for _, kv := range attrs {
		v, ok := set.Value(kv.Key)
		if !ok || v.Type() != kv.Value.Type() || v.Emit() != kv.Value.Emit() {
			return false
		}
	}
	return true
}
