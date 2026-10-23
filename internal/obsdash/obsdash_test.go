package obsdash

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func fixture() Input {
	return Input{
		Project: "shop",
		Services: []Service{
			{Name: "orders", Package: "orders.v1", ProtoService: "OrdersService", Methods: []Method{{Name: "CreateOrder"}, {Name: "GetOrder"}, {Name: "ListOrders"}}},
			{Name: "billing", Package: "billing.v1", ProtoService: "BillingService", Methods: []Method{{Name: "Charge"}}},
		},
		Frontends:   []string{"web"},
		HasDatabase: true,
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
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
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file; run `go test ./internal/obsdash -update` and review the diff", name)
	}
}

func TestGoldenFixtureProject(t *testing.T) {
	sp := Build(fixture())
	files, err := RenderHyperDX(sp)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		golden(t, f.File, f.Content)
	}
	b, _ := json.MarshalIndent(sp, "", "  ")
	golden(t, "dashboard-spec.json", append(b, '\n'))
}

func TestDashboardSetFollowsTheProject(t *testing.T) {
	sp := Build(fixture())
	var ids []string
	for _, d := range sp.Dashboards {
		ids = append(ids, d.ID)
	}
	if got, want := strings.Join(ids, ","), "overview,service-billing,service-orders,workloads,releases,frontend"; got != want {
		t.Fatalf("dashboards = %s, want %s", got, want)
	}

	in := fixture()
	in.Frontends, in.HasDatabase = nil, false
	sp = Build(in)
	for _, d := range sp.Dashboards {
		if d.ID == "frontend" {
			t.Error("a project with no frontend got a frontend dashboard")
		}
		for _, p := range d.Panels {
			if strings.HasPrefix(p.ID, "db-") {
				t.Errorf("%s/%s: database panel without a database", d.ID, p.ID)
			}
		}
	}
}

func TestPerRPCPanelsUseTheProcedureTheInterceptorNamesSpans(t *testing.T) {
	sp := Build(fixture())
	var orders Dashboard
	for _, d := range sp.Dashboards {
		if d.ID == "service-orders" {
			orders = d
		}
	}
	files, _ := RenderHyperDX(sp)
	var raw string
	for _, f := range files {
		if f.File == "shop-service-orders_gen.json" {
			raw = string(f.Content)
		}
	}
	for _, m := range []string{"CreateOrder", "GetOrder", "ListOrders"} {
		// otelconnect names the server span "<package>.<Service>/<Method>"
		// (no leading slash).
		if !strings.Contains(raw, `SpanName = 'orders.v1.OrdersService/`+m+`'`) {
			t.Errorf("no tile selects the span %q", "orders.v1.OrdersService/"+m)
		}
		for _, suffix := range []string{"-rate", "-errors", "-latency"} {
			if _, ok := findPanel(orders, "rpc-"+m+suffix); !ok {
				t.Errorf("missing panel rpc-%s%s", m, suffix)
			}
		}
	}
	if strings.Contains(raw, "SpanName = '/") || strings.Contains(raw, "startsWith(SpanName, '/") {
		t.Error("span names carry no leading slash (otelconnect trims connect.Spec.Procedure); log `procedure` values do")
	}
	if !strings.Contains(raw, "LogAttributes['procedure'], '/orders.v1.OrdersService/'") {
		t.Error("the log scope must match the logging interceptor's procedure, which keeps the leading slash")
	}
}

func findPanel(d Dashboard, id string) (Panel, bool) {
	for _, p := range d.Panels {
		if p.ID == id {
			return p, true
		}
	}
	return Panel{}, false
}

func TestEveryDashboardFiltersOnServiceAndEnvironment(t *testing.T) {
	for _, d := range Build(fixture()).Dashboards {
		got := map[string]bool{}
		for _, f := range d.Filters {
			got[f.Field] = true
		}
		if !got[FieldService] || !got[FieldEnv] {
			t.Errorf("%s: filters %v lack service.name and deployment.environment.name", d.ID, d.Filters)
		}
	}
}

func TestSpecCarriesNoAlerts(t *testing.T) {
	b, _ := json.Marshal(Build(fixture()))
	if strings.Contains(string(b), `"alerts"`) {
		t.Errorf("the dashboard spec still carries alerts; they are forge.AlertRule defaults now (alert-defaults_gen.json): %s", b)
	}
	files, _ := RenderHyperDX(Build(fixture()))
	for _, f := range files {
		if strings.Contains(string(f.Content), `"alert"`) {
			t.Errorf("%s embeds an alert; provisioned alerts never fire in %s and must not be faked", f.File, PinnedHyperDX)
		}
	}
}

func TestRenderingIsDeterministic(t *testing.T) {
	a, _ := RenderHyperDX(Build(fixture()))
	in := fixture()
	in.Services[0], in.Services[1] = in.Services[1], in.Services[0]
	b, _ := RenderHyperDX(Build(in))
	if len(a) != len(b) {
		t.Fatal("different file sets")
	}
	for i := range a {
		if a[i].File != b[i].File || string(a[i].Content) != string(b[i].Content) {
			t.Errorf("%s differs when services are declared in another order", a[i].File)
		}
	}
}

func TestQuotingCannotBreakOutOfASQLString(t *testing.T) {
	s, err := predicateSQL(Traces, Predicate{Field: "span.name", Op: OpEq, Value: `x' OR 1=1 --`})
	if err != nil {
		t.Fatal(err)
	}
	if s != `SpanName = 'x\' OR 1=1 --'` {
		t.Errorf("got %s", s)
	}
}

func TestUnknownFieldIsAnError(t *testing.T) {
	if _, err := fieldSQL(Metrics, "span.name"); err == nil {
		t.Error("a span field on the metrics signal must be rejected, not rendered")
	}
}

func customSchema(t *testing.T) metrics.Schema {
	t.Helper()
	return metrics.Schema{
		SchemaVersion: metrics.SchemaVersion, Service: "shop", Version: "v1.4.0",
		Metrics: []metrics.Spec{
			{Name: "go.memory.used", Type: metrics.UpDownCounter, Unit: "By", Description: "builtin", Stability: metrics.Stable, Since: "v0.1.0"},
			{Name: "orders.calls", Type: metrics.Counter, Unit: "{call}", Description: "a component call metric", Stability: metrics.Experimental, Since: "v0.1.0"},
			{Name: "orders_created", Type: metrics.Counter, Unit: "{order}", Description: "Orders created.", Stability: metrics.Stable, Since: "v1.0.0",
				Attributes: []metrics.Attribute{{Key: "status", Values: []string{"ok", "failed"}}, {Key: "region"}}},
			{Name: "order_value", Type: metrics.Histogram, Unit: "{USD}", Description: "Order value.", Stability: metrics.Experimental, Since: "v1.1.0",
				Attributes: []metrics.Attribute{{Key: "channel", Values: []string{"web", "api"}}}},
			{Name: "queue_depth", Type: metrics.Gauge, Unit: "{job}", Description: "Jobs waiting.", Stability: metrics.Stable, Since: "v1.2.0"},
			{Name: "jobs_in_flight", Type: metrics.UpDownCounter, Unit: "{job}", Description: "Jobs running.", Stability: metrics.Stable, Since: "v1.2.0"},
			{Name: "old_metric", Type: metrics.Counter, Unit: "1", Description: "gone soon", Stability: metrics.Deprecated, Since: "v0.5.0"},
		},
	}
}

func TestCustomMetricsDashboardGolden(t *testing.T) {
	schema := customSchema(t)
	d, ok := BuildCustomMetrics("shop", schema)
	if !ok {
		t.Fatal("no custom metrics dashboard for a schema that declares metrics")
	}
	sp := Spec{Version: SpecVersion, Project: "shop", Dashboards: []Dashboard{d}, SavedSearches: []SavedSearch{}}
	files, err := RenderHyperDX(sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].File != "shop-custom-metrics_gen.json" {
		t.Fatalf("files = %+v", files)
	}
	golden(t, "shop-custom-metrics_gen.json", files[0].Content)
	if errs := validateHyperDXDashboard(files[0].Content); len(errs) > 0 {
		t.Errorf("custom metrics dashboard fails the HyperDX schema mirror:\n%s", strings.Join(errs, "\n"))
	}
}

func TestCustomMetricsDashboardFollowsTheTypes(t *testing.T) {
	d, _ := BuildCustomMetrics("shop", customSchema(t))
	panels := map[string]Panel{}
	for _, p := range d.Panels {
		panels[p.ID] = p
	}

	for id := range panels {
		if strings.Contains(id, "go-memory") || strings.Contains(id, "orders-calls") || strings.Contains(id, "old-metric") {
			t.Errorf("panel %s: built-in, component-call and deprecated metrics do not belong on the custom dashboard", id)
		}
	}
	series := func(id string) []Series { return panels[id].Series }

	if s := series("orders-created"); len(s) != 1 || s[0].Agg != AggSum || s[0].MetricType != "sum" {
		t.Errorf("counter series = %+v, want one sum (rate)", s)
	}
	if s := series("queue-depth"); len(s) != 1 || s[0].Agg != AggLast || s[0].MetricType != "gauge" {
		t.Errorf("gauge series = %+v, want last value of a gauge", s)
	}
	if s := series("jobs-in-flight"); len(s) != 1 || s[0].Agg != AggLast {
		t.Errorf("updowncounter series = %+v, want last value", s)
	}
	h := series("order-value")
	if len(h) != 3 || h[0].Quantile != 0.5 || h[1].Quantile != 0.95 || h[2].Quantile != 0.99 || h[0].MetricType != "histogram" {
		t.Errorf("histogram series = %+v, want p50/p95/p99", h)
	}

	by, ok := panels["orders-created-by-status"]
	if !ok || len(by.GroupBy) != 1 || by.GroupBy[0] != "metric.attr.status" {
		t.Errorf("an enumerated attribute should give a group-by panel, got %+v", by)
	}
	if _, ok := panels["orders-created-by-region"]; ok {
		t.Error("an attribute with no enumerated values must not become a group-by")
	}
	filters := map[string]bool{}
	for _, f := range d.Filters {
		filters[f.Field] = true
	}
	if !filters["metric.attr.region"] || filters["metric.attr.status"] {
		t.Errorf("filters = %v: want region (open-ended) as a filter and status (enumerated) as a group-by", filters)
	}
	if panels["order-value"].Unit != UnitCount || panels["orders-created"].Unit != UnitCount {
		t.Errorf("units: order-value=%q orders-created=%q", panels["order-value"].Unit, panels["orders-created"].Unit)
	}
}

func TestCustomMetricsDashboardIsOmittedWithoutCustomMetrics(t *testing.T) {
	builtinOnly := metrics.NewRegistry(nil).Schema("shop", "v1")
	if _, ok := BuildCustomMetrics("shop", builtinOnly); ok {
		t.Error("a schema of only built-in metrics produced a custom dashboard")
	}
	// The Custom metrics dashboard needs the built binary's schema, which
	// `forge generate` never has: it is added at release cut (CollectBundleSet),
	// not by Build.
	for _, d := range Build(fixture()).Dashboards {
		if d.ID == CustomMetricsID {
			t.Error("Build added a custom-metrics dashboard; it is drawn from a schema Build is never given")
		}
	}
}
