package metrics

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func jobGaugeSpec(name string) Spec {
	return Spec{
		Name: name, Unit: "{generation}", Description: "Latest generation per led job.", Since: "v1.0.0",
		Attributes: []Attribute{{Key: "job"}, {Key: "phase", Values: []string{"live", "draining"}}},
	}
}

// gaugePoints returns a gauge's exported series, keyed by their "job"
// attribute; nil when the metric was not exported at all.
func gaugePoints[N int64 | float64](t *testing.T, got map[string]metricdata.Metrics, name string) map[string]metricdata.DataPoint[N] {
	t.Helper()
	m, ok := got[name]
	if !ok {
		return nil
	}
	g, ok := m.Data.(metricdata.Gauge[N])
	if !ok {
		t.Fatalf("%s exported as %T, want a gauge", name, m.Data)
	}
	out := map[string]metricdata.DataPoint[N]{}
	for _, p := range g.DataPoints {
		v, _ := p.Attributes.Value("job")
		out[v.AsString()] = p
	}
	return out
}

func TestKeyedGaugeDeletedSeriesStopsBeingExported(t *testing.T) {
	reg, reader := newTestRegistry(t, WithMode(ModeDevelopment))
	ig := reg.Int64KeyedGauge(jobGaugeSpec("job_generation"))
	fg := reg.Float64KeyedGauge(Spec{Name: "job_imbalance", Unit: "1", Description: "Imbalance per led job.", Since: "v1.0.0", Attributes: []Attribute{{Key: "job"}}})
	ctx := context.Background()

	ig.Set(ctx, 3, attribute.String("job", "ns/a"))
	ig.Set(ctx, 7, attribute.String("job", "ns/b"))
	ig.Set(ctx, 8, attribute.String("job", "ns/b")) // a Set replaces, it does not add
	fg.Set(ctx, 1.5, attribute.String("job", "ns/a"))

	ints := gaugePoints[int64](t, collect(t, reader), "job_generation")
	if len(ints) != 2 || ints["ns/a"].Value != 3 || ints["ns/b"].Value != 8 {
		t.Fatalf("series before Delete = %v, want ns/a=3 and ns/b=8", ints)
	}

	ig.Delete(attribute.String("job", "ns/a"))
	fg.Delete(attribute.String("job", "ns/a"))
	got := collect(t, reader)
	ints = gaugePoints[int64](t, got, "job_generation")
	if _, still := ints["ns/a"]; still {
		t.Errorf("a deleted series is still exported: %v", ints)
	}
	if ints["ns/b"].Value != 8 {
		t.Errorf("deleting ns/a disturbed ns/b: %v", ints)
	}
	if floats := gaugePoints[float64](t, got, "job_imbalance"); len(floats) != 0 {
		t.Errorf("the float gauge's only series was deleted, yet it exports %v", floats)
	}
	if ig.Len() != 1 || fg.Len() != 0 {
		t.Errorf("Len = %d, %d; want 1, 0", ig.Len(), fg.Len())
	}
}

func TestKeyedGaugeDeleteMatching(t *testing.T) {
	reg, reader := newTestRegistry(t)
	g := reg.Int64KeyedGauge(jobGaugeSpec("job_generation"))
	ctx := context.Background()
	g.Set(ctx, 1, attribute.String("job", "ns/a"), attribute.String("phase", "live"))
	g.Set(ctx, 2, attribute.String("job", "ns/a"), attribute.String("phase", "draining"))
	g.Set(ctx, 3, attribute.String("job", "ns/b"), attribute.String("phase", "live"))

	g.DeleteMatching(attribute.String("job", "ns/a"))
	if pts := gaugePoints[int64](t, collect(t, reader), "job_generation"); len(pts) != 1 || pts["ns/b"].Value != 3 {
		t.Fatalf("after DeleteMatching(job=ns/a) = %v, want only ns/b", pts)
	}
	g.DeleteMatching()
	if g.Len() != 0 {
		t.Fatalf("DeleteMatching() left %d series, want none", g.Len())
	}
}

func TestKeyedGaugeChecksAttributesAtSet(t *testing.T) {
	t.Run("development panics at the Set", func(t *testing.T) {
		reg, _ := newTestRegistry(t, WithMode(ModeDevelopment))
		g := reg.Int64KeyedGauge(jobGaugeSpec("job_generation"))
		defer func() {
			e, ok := recover().(*AttributeError)
			if !ok || e.Key != "user_id" || e.Reason != RejectUndeclaredKey || e.Metric != "job_generation" {
				t.Fatalf("panic = %#v, want an undeclared-key *AttributeError for user_id", e)
			}
		}()
		g.Set(context.Background(), 1, attribute.String("job", "ns/a"), attribute.String("user_id", "u-1"))
	})

	t.Run("production corrects, counts, and Delete finds the corrected series", func(t *testing.T) {
		reg, reader := newTestRegistry(t)
		g := reg.Int64KeyedGauge(jobGaugeSpec("job_generation"))
		ctx := context.Background()
		g.Set(ctx, 5, attribute.String("job", "ns/a"), attribute.String("user_id", "u-1"), attribute.String("phase", "paused"))

		got := collect(t, reader)
		pts := gaugePoints[int64](t, got, "job_generation")
		p, ok := pts["ns/a"]
		if !ok || p.Value != 5 {
			t.Fatalf("series = %v, want ns/a=5", pts)
		}
		if _, leaked := p.Attributes.Value("user_id"); leaked {
			t.Errorf("an undeclared key reached the exporter: %v", p.Attributes)
		}
		if v, _ := p.Attributes.Value("phase"); v.AsString() != OtherValue {
			t.Errorf("phase = %q, want %s", v.AsString(), OtherValue)
		}
		if rej := rejectedCounts(got); rej["job_generation/undeclared_key"] != 1 || rej["job_generation/undeclared_value"] != 1 {
			t.Errorf("rejected_attributes = %v", rej)
		}

		g.Delete(attribute.String("job", "ns/a"), attribute.String("user_id", "u-1"), attribute.String("phase", "paused"))
		if g.Len() != 0 {
			t.Errorf("Delete with the same attributes as Set left %d series", g.Len())
		}
	})
}

func rejectedCounts(got map[string]metricdata.Metrics) map[string]int64 {
	out := map[string]int64{}
	m, ok := got[rejectedName]
	if !ok {
		return out
	}
	for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
		name, _ := p.Attributes.Value("metric")
		reason, _ := p.Attributes.Value("reason")
		out[name.AsString()+"/"+reason.AsString()] += p.Value
	}
	return out
}

func TestObservableGaugeObservesWhatTheCallbackReports(t *testing.T) {
	reg, reader := newTestRegistry(t, WithMode(ModeDevelopment))
	jobs := map[string]int64{"ns/a": 4, "ns/b": 9}
	reg.Int64ObservableGauge(jobGaugeSpec("job_generation"), func(_ context.Context, o metric.Int64Observer) error {
		for job, v := range jobs {
			o.Observe(v, metric.WithAttributes(attribute.String("job", job)))
		}
		return nil
	})
	reg.Float64ObservableGauge(Spec{Name: "pool_usage", Unit: "1", Description: "Share of the pool in use.", Since: "v1.0.0"},
		func(_ context.Context, o metric.Float64Observer) error { o.Observe(0.25); return nil })

	got := collect(t, reader)
	if pts := gaugePoints[int64](t, got, "job_generation"); len(pts) != 2 || pts["ns/b"].Value != 9 {
		t.Fatalf("series = %v", pts)
	}
	if fp := got["pool_usage"].Data.(metricdata.Gauge[float64]).DataPoints; len(fp) != 1 || fp[0].Value != 0.25 {
		t.Fatalf("pool_usage = %v", fp)
	}

	delete(jobs, "ns/a")
	if pts := gaugePoints[int64](t, collect(t, reader), "job_generation"); len(pts) != 1 {
		t.Fatalf("a series the callback stopped observing is still exported: %v", pts)
	}

	for _, name := range []string{"job_generation", "pool_usage"} {
		if s, ok := reg.Lookup(name); !ok || s.Type != Gauge {
			t.Errorf("Lookup(%q) = %+v, %v; want a declared gauge", name, s, ok)
		}
	}
}

func TestObservableGaugeChecksAttributes(t *testing.T) {
	t.Run("production", func(t *testing.T) {
		reg, reader := newTestRegistry(t)
		reg.Int64ObservableGauge(jobGaugeSpec("job_generation"), func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1, metric.WithAttributes(attribute.String("job", "ns/a"), attribute.String("user_id", "u-1")))
			return nil
		})
		got := collect(t, reader)
		for _, p := range got["job_generation"].Data.(metricdata.Gauge[int64]).DataPoints {
			if _, leaked := p.Attributes.Value("user_id"); leaked {
				t.Errorf("an undeclared key reached the exporter: %v", p.Attributes)
			}
		}
		if rej := rejectedCounts(got); rej["job_generation/undeclared_key"] != 1 {
			t.Errorf("rejected_attributes = %v", rej)
		}
	})

	t.Run("development", func(t *testing.T) {
		reg, reader := newTestRegistry(t, WithMode(ModeDevelopment))
		reg.Float64ObservableGauge(Spec{Name: "pool_usage", Unit: "1", Description: "x", Since: "v1.0.0"},
			func(_ context.Context, o metric.Float64Observer) error {
				o.Observe(1, metric.WithAttributes(attribute.String("pool", "main")))
				return nil
			})
		defer func() {
			if e, ok := recover().(*AttributeError); !ok || e.Key != "pool" {
				t.Fatalf("panic = %#v, want an *AttributeError for pool", e)
			}
		}()
		collect(t, reader)
	})
}

func TestRegisterCallbackChecksEveryInstrument(t *testing.T) {
	reg, reader := newTestRegistry(t)
	gen := reg.Int64ObservableGauge(jobGaugeSpec("job_generation"))
	imb := reg.Float64ObservableGauge(Spec{Name: "job_imbalance", Unit: "1", Description: "x", Since: "v1.0.0", Attributes: []Attribute{{Key: "job"}}})
	reg2, err := reg.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		job := metric.WithAttributes(attribute.String("job", "ns/a"))
		o.ObserveInt64(gen, 12, job)
		o.ObserveFloat64(imb, 0.5, metric.WithAttributes(attribute.String("job", "ns/a"), attribute.String("pod", "p-1")))
		return nil
	}, gen, imb)
	if err != nil {
		t.Fatal(err)
	}

	got := collect(t, reader)
	if pts := gaugePoints[int64](t, got, "job_generation"); pts["ns/a"].Value != 12 {
		t.Errorf("job_generation = %v", pts)
	}
	fp := gaugePoints[float64](t, got, "job_imbalance")
	if p := fp["ns/a"]; p.Value != 0.5 {
		t.Errorf("job_imbalance = %v", fp)
	} else if _, leaked := p.Attributes.Value("pod"); leaked {
		t.Errorf("an undeclared key reached the exporter: %v", p.Attributes)
	}
	if rej := rejectedCounts(got); rej["job_imbalance/undeclared_key"] != 1 {
		t.Errorf("rejected_attributes = %v", rej)
	}

	if err := reg2.Unregister(); err != nil {
		t.Fatal(err)
	}
	if pts := gaugePoints[int64](t, collect(t, reader), "job_generation"); len(pts) != 0 {
		t.Errorf("an unregistered callback's series is still exported: %v", pts)
	}
}

func TestRegisterCallbackRefusesUndeclaredInstruments(t *testing.T) {
	reg, _ := newTestRegistry(t)
	other, _ := newTestRegistry(t)
	foreign := other.Int64ObservableGauge(jobGaugeSpec("job_generation"))
	if _, err := reg.RegisterCallback(func(context.Context, metric.Observer) error { return nil }, foreign); err == nil ||
		!strings.Contains(err.Error(), "not created by this registry") {
		t.Fatalf("RegisterCallback(another registry's instrument) = %v, want a refusal", err)
	}
	if _, err := reg.RegisterCallback(nil); err == nil {
		t.Fatal("RegisterCallback(nil) succeeded")
	}
}

func TestGaugeShapesConflictLikeAnyRedeclaration(t *testing.T) {
	reg, _ := newTestRegistry(t)
	reg.Int64KeyedGauge(jobGaugeSpec("job_generation"))
	reg.Int64ObservableGauge(jobGaugeSpec("job_generation")) // same declaration: fine
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(error).Error(), "already declared differently") {
			t.Fatalf("redeclaring a gauge as a counter: recover() = %v", r)
		}
	}()
	reg.Int64Counter(jobGaugeSpec("job_generation"))
}
