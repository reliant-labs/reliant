package metrics

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func newTestRegistry(t *testing.T, opts ...Option) (*Registry, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	return NewRegistry(mp.Meter("test"), opts...), reader
}

func collect(t *testing.T, r *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func orders() Spec {
	return Spec{
		Name: "orders_created", Unit: "{order}", Description: "Orders created.", Since: "v1.0.0",
		Attributes: []Attribute{{Key: "status", Values: []string{"ok", "failed"}}, {Key: "region"}},
	}
}

func TestSpecValidate(t *testing.T) {
	good := orders()
	good.Type = Counter
	if err := good.Validate(); err != nil {
		t.Fatalf("a valid spec was rejected: %v", err)
	}

	cases := map[string]struct {
		mut  func(*Spec)
		want string
	}{
		"no name":             {func(s *Spec) { s.Name = "" }, "name is required"},
		"upper case name":     {func(s *Spec) { s.Name = "OrdersCreated" }, "lower snake case"},
		"dash in name":        {func(s *Spec) { s.Name = "orders-created" }, "lower snake case"},
		"no type":             {func(s *Spec) { s.Type = "" }, "type is required"},
		"unknown type":        {func(s *Spec) { s.Type = "summary" }, "not one of counter"},
		"no unit":             {func(s *Spec) { s.Unit = "" }, "unit is required"},
		"word unit":           {func(s *Spec) { s.Unit = "seconds" }, "not a valid UCUM"},
		"per-sec unit":        {func(s *Spec) { s.Unit = "req/sec" }, "not a valid UCUM"},
		"trailing space":      {func(s *Spec) { s.Unit = "s " }, "not a valid UCUM"},
		"unclosed annotation": {func(s *Spec) { s.Unit = "{order" }, "not a valid UCUM"},
		"no description":      {func(s *Spec) { s.Description = "  " }, "description is required"},
		"bad stability":       {func(s *Spec) { s.Stability = "beta" }, "not one of stable"},
		"no since":            {func(s *Spec) { s.Since = "" }, "since is required"},
		"bad since":           {func(s *Spec) { s.Since = "latest" }, "not a version"},
		"bad key":             {func(s *Spec) { s.Attributes = []Attribute{{Key: "Status"}} }, "attribute key"},
		"duplicate key": {func(s *Spec) {
			s.Attributes = []Attribute{{Key: "a"}, {Key: "a"}}
		}, "declared twice"},
		"empty value":    {func(s *Spec) { s.Attributes = []Attribute{{Key: "a", Values: []string{""}}} }, "empty value"},
		"reserved value": {func(s *Spec) { s.Attributes = []Attribute{{Key: "a", Values: []string{OtherValue}}} }, "reserved"},
		"duplicate value": {func(s *Spec) {
			s.Attributes = []Attribute{{Key: "a", Values: []string{"x", "x"}}}
		}, "twice"},
		"buckets on counter": {func(s *Spec) { s.Buckets = []float64{1, 2} }, "histograms only"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := good
			tc.mut(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}

	t.Run("buckets must increase", func(t *testing.T) {
		s := good
		s.Type, s.Buckets = Histogram, []float64{1, 1}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "strictly increasing") {
			t.Fatalf("Validate() = %v", err)
		}
	})
}

func TestUCUMUnits(t *testing.T) {
	for _, u := range []string{"s", "ms", "us", "ns", "By", "KiBy", "MiBy", "kBy", "{order}", "{request}", "1", "%", "By/s", "{call}/s", "m2", "s-1", "1/s", "Cel", "Hz", "{error}.s"} {
		if !validUCUM(u) {
			t.Errorf("validUCUM(%q) = false, want true", u)
		}
	}
	for _, u := range []string{"", "seconds", "milliseconds", "bytes", "requests", "req/sec", "s ", "{", "{}", "By/", "/", ".s", "KiB", "ss", "s s"} {
		if validUCUM(u) {
			t.Errorf("validUCUM(%q) = true, want false", u)
		}
	}
}

func TestDeclaredInstrumentRecordsThroughOTel(t *testing.T) {
	reg, reader := newTestRegistry(t)
	c := reg.Int64Counter(orders())
	c.Add(context.Background(), 2, metric.WithAttributes(attribute.String("status", "ok"), attribute.String("region", "eu")))

	m, ok := collect(t, reader)["orders_created"]
	if !ok {
		t.Fatal("orders_created was not exported")
	}
	if m.Unit != "{order}" || m.Description != "Orders created." {
		t.Errorf("exported unit/description = %q/%q", m.Unit, m.Description)
	}
	sum := m.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 2 {
		t.Fatalf("data points = %+v", sum.DataPoints)
	}
	if v, _ := sum.DataPoints[0].Attributes.Value("status"); v.AsString() != "ok" {
		t.Errorf("status attribute = %v", v)
	}
	spec, ok := reg.Lookup("orders_created")
	if !ok || spec.Type != Counter || spec.Stability != Experimental {
		t.Errorf("Lookup = %+v, %v; want a counter defaulting to experimental", spec, ok)
	}
}

func TestEveryInstrumentKindIsDeclaredAndRecords(t *testing.T) {
	reg, reader := newTestRegistry(t)
	ctx := context.Background()
	spec := func(name string) Spec {
		return Spec{Name: name, Unit: "1", Description: name, Since: "v0.1.0", Attributes: []Attribute{{Key: "k"}}}
	}
	reg.Int64Counter(spec("i_counter")).Add(ctx, 1)
	reg.Float64Counter(spec("f_counter")).Add(ctx, 1.5)
	reg.Int64UpDownCounter(spec("i_updown")).Add(ctx, -1)
	reg.Float64UpDownCounter(spec("f_updown")).Add(ctx, -1.5)
	reg.Int64Histogram(spec("i_hist")).Record(ctx, 3)
	reg.Float64Histogram(spec("f_hist")).Record(ctx, 3.5)
	reg.Int64Gauge(spec("i_gauge")).Record(ctx, 7)
	reg.Float64Gauge(spec("f_gauge")).Record(ctx, 7.5)

	got := collect(t, reader)
	want := map[string]Type{
		"i_counter": Counter, "f_counter": Counter, "i_updown": UpDownCounter, "f_updown": UpDownCounter,
		"i_hist": Histogram, "f_hist": Histogram, "i_gauge": Gauge, "f_gauge": Gauge,
	}
	for name, typ := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s was not exported", name)
		}
		if s, _ := reg.Lookup(name); s.Type != typ {
			t.Errorf("%s declared as %q, want %q", name, s.Type, typ)
		}
	}
}

func TestHistogramBuckets(t *testing.T) {
	reg, reader := newTestRegistry(t)
	s := Spec{Name: "latency", Unit: "s", Description: "x", Since: "v1.0.0", Buckets: []float64{0.1, 1}}
	reg.Float64Histogram(s).Record(context.Background(), 0.5)
	h := collect(t, reader)["latency"].Data.(metricdata.Histogram[float64])
	if got := h.DataPoints[0].Bounds; len(got) != 2 || got[0] != 0.1 || got[1] != 1 {
		t.Errorf("bounds = %v, want [0.1 1]", got)
	}
}

// A histogram declared in seconds with no Buckets gets SecondsBuckets, through
// both registry constructors, as callers normally declare it (Type and Buckets
// omitted). The SDK default boundaries are sized for milliseconds: against
// seconds they put 5ms..5s in one bucket and every quantile reads 4.75s.
func TestSecondsHistogramsGetSecondsBuckets(t *testing.T) {
	reg, reader := newTestRegistry(t)
	ctx := context.Background()
	reg.Float64Histogram(Spec{Name: "f_seconds", Unit: "s", Description: "x", Since: "v1.0.0"}).Record(ctx, 0.06)
	reg.Int64Histogram(Spec{Name: "i_seconds", Unit: "s", Description: "x", Since: "v1.0.0"}).Record(ctx, 3)
	reg.Float64Histogram(Spec{Name: "f_millis", Unit: "ms", Description: "x", Since: "v1.0.0"}).Record(ctx, 60)
	got := collect(t, reader)

	f := got["f_seconds"].Data.(metricdata.Histogram[float64]).DataPoints[0]
	if !slices.Equal(f.Bounds, SecondsBuckets()) {
		t.Fatalf("Float64Histogram bounds = %v, want SecondsBuckets %v", f.Bounds, SecondsBuckets())
	}
	// 0.06s lands in (0.05, 0.075]: a bucket 25ms wide, not [0, 5].
	if i := slices.Index(f.Bounds, 0.075); f.BucketCounts[i] != 1 {
		t.Errorf("0.06s is not in the (0.05, 0.075] bucket: counts %v", f.BucketCounts)
	}
	if b := got["i_seconds"].Data.(metricdata.Histogram[int64]).DataPoints[0].Bounds; !slices.Equal(b, SecondsBuckets()) {
		t.Errorf("Int64Histogram bounds = %v, want SecondsBuckets", b)
	}
	if b := got["f_millis"].Data.(metricdata.Histogram[float64]).DataPoints[0].Bounds; slices.Equal(b, SecondsBuckets()) {
		t.Errorf("a millisecond histogram got the seconds boundaries %v", b)
	}
}

// An explicit Buckets on the constructor wins, even when the same metric was
// declared earlier without any (Describe, or a built-in): buckets are not part
// of a metric's identity, so the earlier declaration must not substitute
// SecondsBuckets for the caller's override.
func TestExplicitBucketsWinOverAnEarlierDeclaration(t *testing.T) {
	reg, reader := newTestRegistry(t)
	ctx := context.Background()
	custom := []float64{0.1, 1}
	for _, name := range []string{"f_custom", "i_custom"} {
		if err := reg.Describe(Spec{Name: name, Type: Histogram, Unit: "s", Description: "x", Since: "v1.0.0"}); err != nil {
			t.Fatal(err)
		}
	}
	reg.Float64Histogram(Spec{Name: "f_custom", Unit: "s", Description: "x", Since: "v1.0.0", Buckets: custom}).Record(ctx, 0.5)
	reg.Int64Histogram(Spec{Name: "i_custom", Unit: "s", Description: "x", Since: "v1.0.0", Buckets: custom}).Record(ctx, 3)
	got := collect(t, reader)
	if b := got["f_custom"].Data.(metricdata.Histogram[float64]).DataPoints[0].Bounds; !slices.Equal(b, custom) {
		t.Errorf("Float64Histogram bounds = %v, want the explicit %v", b, custom)
	}
	if b := got["i_custom"].Data.(metricdata.Histogram[int64]).DataPoints[0].Bounds; !slices.Equal(b, custom) {
		t.Errorf("Int64Histogram bounds = %v, want the explicit %v", b, custom)
	}
}

func TestUndeclaredAttributeInDevelopmentPanics(t *testing.T) {
	reg, _ := newTestRegistry(t, WithMode(ModeDevelopment))
	c := reg.Int64Counter(orders())

	expectPanic := func(name string, record func(), reason, key string) {
		t.Helper()
		defer func() {
			e, ok := recover().(*AttributeError)
			if !ok {
				t.Fatalf("%s: want an *AttributeError panic", name)
			}
			if e.Reason != reason || e.Key != key || e.Metric != "orders_created" {
				t.Errorf("%s: panic = %+v", name, e)
			}
			if !strings.Contains(e.Error(), "orders_created") || !strings.Contains(e.Error(), key) {
				t.Errorf("%s: message %q does not name the metric and key", name, e.Error())
			}
		}()
		record()
	}
	expectPanic("undeclared key", func() {
		c.Add(context.Background(), 1, metric.WithAttributes(attribute.String("user_id", "u-1")))
	}, RejectUndeclaredKey, "user_id")
	expectPanic("undeclared value", func() {
		c.Add(context.Background(), 1, metric.WithAttributes(attribute.String("status", "maybe")))
	}, RejectUndeclaredValue, "status")
}

func TestUndeclaredAttributeInProductionIsCorrectedAndCounted(t *testing.T) {
	reg, reader := newTestRegistry(t)
	c := reg.Int64Counter(orders())
	h := reg.Float64Histogram(Spec{Name: "order_value", Unit: "1", Description: "x", Since: "v1.0.0", Attributes: []Attribute{{Key: "status", Values: []string{"ok"}}}})
	ctx := context.Background()

	c.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "ok"), attribute.String("user_id", "u-1")))
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "maybe"), attribute.String("region", "eu")))
	h.Record(ctx, 4, metric.WithAttributes(attribute.String("status", "nope")))

	got := collect(t, reader)

	points := got["orders_created"].Data.(metricdata.Sum[int64]).DataPoints
	seen := map[string]int64{}
	for _, p := range points {
		if _, leaked := p.Attributes.Value("user_id"); leaked {
			t.Errorf("an undeclared key reached the exporter: %v", p.Attributes)
		}
		v, _ := p.Attributes.Value("status")
		seen[v.AsString()] += p.Value
	}
	if seen["ok"] != 1 || seen[OtherValue] != 1 {
		t.Errorf("status series = %v, want ok=1 and %s=1", seen, OtherValue)
	}

	hp := got["order_value"].Data.(metricdata.Histogram[float64]).DataPoints
	if v, _ := hp[0].Attributes.Value("status"); v.AsString() != OtherValue {
		t.Errorf("histogram status = %v, want %s", v, OtherValue)
	}

	rejected := map[string]int64{}
	for _, p := range got[rejectedName].Data.(metricdata.Sum[int64]).DataPoints {
		m, _ := p.Attributes.Value("metric")
		r, _ := p.Attributes.Value("reason")
		rejected[m.AsString()+"/"+r.AsString()] += p.Value
	}
	for key, want := range map[string]int64{
		"orders_created/undeclared_key":   1,
		"orders_created/undeclared_value": 1,
		"order_value/undeclared_value":    1,
	} {
		if rejected[key] != want {
			t.Errorf("rejected_attributes[%s] = %d, want %d (all: %v)", key, rejected[key], want, rejected)
		}
	}
}

func TestDeclaredAttributesTakeTheFastPath(t *testing.T) {
	reg, reader := newTestRegistry(t, WithMode(ModeDevelopment))
	c := reg.Int64Counter(orders())
	c.Add(context.Background(), 1, metric.WithAttributes(attribute.String("status", "failed"), attribute.String("region", "us")))
	if _, ok := collect(t, reader)[rejectedName]; ok {
		t.Error("a fully declared measurement was counted as rejected")
	}
	c.Add(context.Background(), 1) // no attributes at all is always fine
}

func TestSetModeAppliesToInstrumentsAlreadyHandedOut(t *testing.T) {
	reg, _ := newTestRegistry(t)
	c := reg.Int64Counter(orders())
	bad := metric.WithAttributes(attribute.String("nope", "x"))

	c.Add(context.Background(), 1, bad) // production: corrected, no panic
	reg.SetMode(ModeDevelopment)
	defer func() {
		if recover() == nil {
			t.Error("SetMode(ModeDevelopment) did not take effect on an existing instrument")
		}
	}()
	c.Add(context.Background(), 1, bad)
}

func TestDeclarationConflicts(t *testing.T) {
	reg, _ := newTestRegistry(t)
	reg.Int64Counter(orders())
	reg.Int64Counter(orders()) // identical redeclaration is idempotent

	mustPanic := func(name string, f func(), want string) {
		t.Helper()
		defer func() {
			r := recover()
			err, _ := r.(error)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: panic = %v, want an error containing %q", name, r, want)
			}
		}()
		f()
	}
	other := orders()
	other.Unit = "1"
	mustPanic("different unit", func() { reg.Int64Counter(other) }, "already declared differently")
	mustPanic("different type", func() { reg.Int64Gauge(orders()) }, "already declared differently")
	bad := orders()
	bad.Name = "Bad Name"
	mustPanic("invalid", func() { reg.Int64Counter(bad) }, "lower snake case")
	builtin := orders()
	builtin.Name = "rpc.server.calls"
	mustPanic("builtin name", func() { reg.Int64Counter(builtin) }, "")

	if err := reg.Describe(Spec{Name: "ext_metric", Type: Gauge, Unit: "By", Description: "x", Since: "v1.0.0"}); err != nil {
		t.Errorf("Describe: %v", err)
	}
	if err := reg.Describe(Spec{Name: "ext_metric", Type: Counter, Unit: "By", Description: "x", Since: "v1.0.0"}); err == nil {
		t.Error("Describe accepted a conflicting redeclaration")
	}
}

func TestDeclaredIsACopy(t *testing.T) {
	reg, _ := newTestRegistry(t)
	reg.Int64Counter(orders())
	got, _ := reg.Lookup("orders_created")
	got.Attributes[0].Values[0] = "mutated"
	again, _ := reg.Lookup("orders_created")
	if again.Attributes[0].Values[0] != "ok" {
		t.Error("mutating a returned Spec changed the registry")
	}
}

func TestBuiltInMetricsAreDeclared(t *testing.T) {
	reg, _ := newTestRegistry(t)
	have := map[string]Spec{}
	for _, s := range reg.Declared() {
		if err := s.Validate(); err != nil {
			t.Errorf("built-in %s is invalid: %v", s.Name, err)
		}
		have[s.Name] = s
	}
	for _, name := range []string{
		"rpc.server.calls", "rpc.server.errors", "rpc.server.duration",
		"go.memory.used", "go.memory.allocated", "go.goroutine.count", "go.config.gogc",
		"go.sql.query_timing", "go.sql.connections_in_use", rejectedName,
	} {
		if _, ok := have[name]; !ok {
			t.Errorf("built-in metric %s is not declared", name)
		}
	}
	if s := have["rpc.server.errors"]; len(s.Attributes) != 3 {
		t.Errorf("rpc.server.errors attributes = %v, want procedure, code, error_class", s.Attributes)
	}
	if got := BuiltinSpec("rpc.server.duration"); got.Unit != "s" {
		t.Errorf("BuiltinSpec unit = %q", got.Unit)
	}
}

func TestDescribeCallMetrics(t *testing.T) {
	DescribeCallMetrics("userstore")
	DescribeCallMetrics("userstore") // idempotent
	for _, n := range []string{"userstore.calls", "userstore.errors", "userstore.duration"} {
		if _, ok := Default().Lookup(n); !ok {
			t.Errorf("%s not declared", n)
		}
	}
	DescribeCallMetrics("Bad-Namespace") // invalid: skipped, never panics
}

func goldenRegistry(t *testing.T) *Registry {
	reg, _ := newTestRegistry(t)
	reg.Int64Counter(orders())
	reg.Float64Histogram(Spec{
		Name: "order_value", Unit: "{USD}", Description: "Order value at creation.", Since: "v1.1.0", Stability: Stable,
		Attributes: []Attribute{{Key: "channel", Values: []string{"web", "api"}}},
	})
	reg.Int64Gauge(Spec{Name: "queue_depth", Unit: "{job}", Description: "Jobs waiting.", Since: "v1.2.0", Stability: Deprecated})
	reg.Int64UpDownCounter(Spec{Name: "jobs_in_flight", Unit: "{job}", Description: "Jobs running.", Since: "v1.2.0"})
	return reg
}

func TestSchemaGolden(t *testing.T) {
	got, err := goldenRegistry(t).Schema("shop", "v1.4.0").JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "schema.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("schema differs from %s; run `go test ./pkg/observe/metrics -update` and review the diff\n--- got\n%s", path, got)
	}
}

func TestSchemaShapeMatchesTheContract(t *testing.T) {
	s := goldenRegistry(t).Schema("shop", "v1.4.0")
	if s.SchemaVersion != "forge.metrics/v1" || s.Service != "shop" || s.Version != "v1.4.0" {
		t.Fatalf("envelope = %+v", s)
	}
	for i := 1; i < len(s.Metrics); i++ {
		if s.Metrics[i-1].Name >= s.Metrics[i].Name {
			t.Errorf("metrics not sorted: %s before %s", s.Metrics[i-1].Name, s.Metrics[i].Name)
		}
	}
	b, _ := s.JSON()
	if strings.Contains(string(b), `"attributes": null`) || strings.Contains(string(b), "buckets") {
		t.Errorf("schema leaks a null attribute list or buckets:\n%s", b)
	}
	back, err := ParseSchema(b)
	if err != nil || len(back.Metrics) != len(s.Metrics) {
		t.Fatalf("round trip: %v, %d metrics", err, len(back.Metrics))
	}
	if _, err := ParseSchema([]byte(`{"schema_version":"forge.metrics/v9"}`)); err == nil {
		t.Error("ParseSchema accepted an unknown version")
	}
}

func TestSchemaHandler(t *testing.T) {
	reg := goldenRegistry(t)
	h := SchemaHandler(reg, "shop", "v1.4.0")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SchemaPath, nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	want, _ := reg.Schema("shop", "v1.4.0").JSON()
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("handler body differs from Schema().JSON()")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, SchemaPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestAttributeErrorIsAnError(t *testing.T) {
	var err error = &AttributeError{Metric: "m", Key: "k", Reason: RejectUndeclaredKey}
	var ae *AttributeError
	if !errors.As(err, &ae) {
		t.Error("AttributeError does not satisfy errors.As")
	}
}
