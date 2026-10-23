package metrics

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ScopeName is the OpenTelemetry instrumentation scope the default registry's
// instruments are created under.
const ScopeName = "github.com/reliant-labs/forge/pkg/observe/metrics"

// Mode says what happens when a measurement carries an attribute the metric
// did not declare.
type Mode int32

const (
	// ModeProduction (the zero value) corrects the measurement — an undeclared
	// key is dropped, a value outside an enumerated set becomes OtherValue —
	// and counts the correction on forge.metrics.rejected_attributes. A stray
	// attribute never takes a service down and never becomes a new time series.
	ModeProduction Mode = iota
	// ModeDevelopment panics with an *AttributeError, at the call that made the
	// mistake. serverkit selects it for ENVIRONMENT=development.
	ModeDevelopment
)

// AttributeError is the panic value of a ModeDevelopment violation.
type AttributeError struct {
	Metric string
	Key    string
	// Value is the offending value when the key is declared with an enumerated
	// set; empty for an undeclared key.
	Value string
	// Reason is RejectUndeclaredKey or RejectUndeclaredValue.
	Reason string
}

// The reasons a measurement's attribute is rejected, as they appear on
// forge.metrics.rejected_attributes.
const (
	RejectUndeclaredKey   = "undeclared_key"
	RejectUndeclaredValue = "undeclared_value"
)

func (e *AttributeError) Error() string {
	if e.Reason == RejectUndeclaredValue {
		return fmt.Sprintf("metric %q: attribute %q has value %q, which is not one of its declared values; add it to the declaration or record a declared value", e.Metric, e.Key, e.Value)
	}
	return fmt.Sprintf("metric %q: attribute %q is not declared; add it to the metric's Attributes or stop recording it", e.Metric, e.Key)
}

// Registry holds the declared metrics of one process and creates their
// OpenTelemetry instruments. It is safe for concurrent use.
type Registry struct {
	meter metric.Meter
	mode  atomic.Int32

	mu    sync.Mutex
	decls map[string]*decl

	rejected metric.Int64Counter
}

// decl is one registered metric: its declaration, and the lookup tables the
// record-time check reads.
type decl struct {
	spec Spec
	// attrs maps a declared key to its enumerated values (nil: any value).
	attrs map[string]map[string]struct{}
	// builtin marks forge's own metrics, which are described, not created
	// through the registry.
	builtin bool
}

// Option configures NewRegistry.
type Option func(*Registry)

// WithMode sets the registry's initial Mode.
func WithMode(m Mode) Option { return func(r *Registry) { r.mode.Store(int32(m)) } }

// NewRegistry returns a registry creating its instruments on meter, with
// forge's built-in metrics already declared. A nil meter uses the process-wide
// OpenTelemetry meter provider, which delegates to whatever observe.Setup
// installs later, so a registry built at package init is bound correctly.
func NewRegistry(meter metric.Meter, opts ...Option) *Registry {
	if meter == nil {
		meter = otel.Meter(ScopeName)
	}
	r := &Registry{meter: meter, decls: map[string]*decl{}}
	for _, o := range opts {
		o(r)
	}
	for _, s := range builtinSpecs() {
		if err := r.describe(s, true); err != nil {
			panic(err) // forge's own table; covered by a test
		}
	}
	// The rejection counter is the one built-in the registry creates itself:
	// it has to exist before any measurement can be rejected.
	if c, err := r.meter.Int64Counter(rejectedName,
		metric.WithUnit("{attribute}"),
		metric.WithDescription(r.decls[rejectedName].spec.Description)); err == nil {
		r.rejected = c
	}
	return r
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default is the process-wide registry: the one a project's internal/metrics
// package declares into, and the one `<bin> metrics schema`, the debug
// endpoint and serverkit read. Its mode is ModeProduction until serverkit (or
// SetMode) says otherwise.
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewRegistry(nil) })
	return defaultReg
}

// SetMode changes what a violation does. It applies to measurements recorded
// afterwards, on every instrument the registry has already handed out.
func (r *Registry) SetMode(m Mode) { r.mode.Store(int32(m)) }

// Mode reports the current mode.
func (r *Registry) Mode() Mode { return Mode(r.mode.Load()) }

// Describe adds a declaration for a metric whose instrument something else
// creates — a library's own meter, an observable callback. It is how the
// schema stays complete for metrics the registry cannot wrap. It returns an
// error for an invalid spec or a name already declared differently.
func (r *Registry) Describe(s Spec) error { return r.describe(s, false) }

func (r *Registry) describe(s Spec, builtin bool) error {
	_, err := r.declare(s, builtin)
	return err
}

// declare validates s and registers it, returning the registered declaration.
// Redeclaring an identical metric returns the existing one.
func (r *Registry) declare(s Spec, builtin bool) (*decl, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s = s.normalized()
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.decls[s.Name]; ok {
		if prev.builtin != builtin && !sameDeclaration(prev.spec, s) {
			return nil, fmt.Errorf("metric %q is reserved by forge's built-in metrics", s.Name)
		}
		if !sameDeclaration(prev.spec, s) {
			return nil, fmt.Errorf("metric %q is already declared differently (as a %s in %q); declare it once", s.Name, prev.spec.Type, prev.spec.Unit)
		}
		return prev, nil
	}
	d := &decl{spec: s, attrs: make(map[string]map[string]struct{}, len(s.Attributes)), builtin: builtin}
	for _, a := range s.Attributes {
		var vals map[string]struct{}
		if len(a.Values) > 0 {
			vals = make(map[string]struct{}, len(a.Values))
			for _, v := range a.Values {
				vals[v] = struct{}{}
			}
		}
		d.attrs[a.Key] = vals
	}
	r.decls[s.Name] = d
	return d, nil
}

// instrument declares s as type t and returns the guard for its instrument.
// A bad declaration is a programmer error found at package init, so it panics,
// like regexp.MustCompile.
func (r *Registry) instrument(s Spec, t Type) (*guard, []metric.InstrumentOption) {
	s.Type = t
	d, err := r.declare(s, false)
	if err != nil {
		panic(fmt.Errorf("metrics: %w", err))
	}
	return &guard{reg: r, d: d}, []metric.InstrumentOption{metric.WithUnit(d.spec.Unit), metric.WithDescription(d.spec.Description)}
}

// mustCreate unwraps the (instrument, error) pair OpenTelemetry returns. An
// error here means the SDK rejected the instrument (an invalid name that
// passed our check is the only known cause).
func mustCreate[T any](v T, err error, name string) T {
	if err != nil {
		panic(fmt.Errorf("metrics: create instrument %q: %w", name, err))
	}
	return v
}

// guard enforces one metric's declared attributes on every measurement.
type guard struct {
	reg *Registry
	d   *decl
}

// check returns the attribute set to record and whether it differs from set:
// set itself when every attribute is declared (the common path), a corrected
// set otherwise.
func (g *guard) check(ctx context.Context, set attribute.Set) (attribute.Set, bool) {
	var bad []violation
	for iter := set.Iter(); iter.Next(); {
		kv := iter.Attribute()
		allowed, declared := g.d.attrs[string(kv.Key)]
		switch {
		case !declared:
			bad = append(bad, violation{kv: kv, reason: RejectUndeclaredKey})
		case allowed != nil:
			if _, ok := allowed[kv.Value.Emit()]; !ok {
				bad = append(bad, violation{kv: kv, reason: RejectUndeclaredValue})
			}
		}
	}
	if len(bad) == 0 {
		return set, false
	}
	if g.reg.Mode() == ModeDevelopment {
		v := bad[0]
		e := &AttributeError{Metric: g.d.spec.Name, Key: string(v.kv.Key), Reason: v.reason}
		if v.reason == RejectUndeclaredValue {
			e.Value = v.kv.Value.Emit()
		}
		panic(e)
	}
	kvs := make([]attribute.KeyValue, 0, set.Len())
	for iter := set.Iter(); iter.Next(); {
		kv := iter.Attribute()
		reason := ""
		for _, v := range bad {
			if v.kv.Key == kv.Key {
				reason = v.reason
			}
		}
		switch reason {
		case RejectUndeclaredKey:
		case RejectUndeclaredValue:
			kvs = append(kvs, attribute.String(string(kv.Key), OtherValue))
		default:
			kvs = append(kvs, kv)
		}
	}
	if g.reg.rejected != nil {
		for _, v := range bad {
			g.reg.rejected.Add(ctx, 1, metric.WithAttributes(
				attribute.String("metric", g.d.spec.Name),
				attribute.String("reason", v.reason)))
		}
	}
	return attribute.NewSet(kvs...), true
}

type violation struct {
	kv     attribute.KeyValue
	reason string
}

func (g *guard) addOpts(ctx context.Context, opts []metric.AddOption) []metric.AddOption {
	set := metric.NewAddConfig(opts).Attributes()
	if fixed, changed := g.check(ctx, set); changed {
		return []metric.AddOption{metric.WithAttributeSet(fixed)}
	}
	return opts
}

func (g *guard) recordOpts(ctx context.Context, opts []metric.RecordOption) []metric.RecordOption {
	set := metric.NewRecordConfig(opts).Attributes()
	if fixed, changed := g.check(ctx, set); changed {
		return []metric.RecordOption{metric.WithAttributeSet(fixed)}
	}
	return opts
}

// Declared returns the registry's declarations, sorted by name.
func (r *Registry) Declared() []Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Spec, 0, len(r.decls))
	for _, d := range r.decls {
		out = append(out, d.spec.normalized())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the declaration of name.
func (r *Registry) Lookup(name string) (Spec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.decls[name]
	if !ok {
		return Spec{}, false
	}
	return d.spec.normalized(), true
}
