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
		Processes: []Process{
			{Name: "shop-api", Binary: "shop", Args: []string{"api"}, Root: "api", ServiceNames: []string{"shop-api"}, Workloads: []string{"shop-api"}, RPCServices: []string{"billing.v1.BillingService", "orders.v1.OrdersService"}, HasDatabase: true, Kind: "service", CPULimit: true, MemoryLimit: true, NodeMetadata: true},
			{Name: "shop-worker", Binary: "shop", Args: []string{"worker"}, Root: "worker", ServiceNames: []string{"shop-worker"}, Workloads: []string{"shop-worker"}, Kind: "worker", NodeMetadata: true},
			{Name: "shop-migrate", Binary: "shop", Args: []string{"db", "migrate", "up"}, ServiceNames: []string{"shop-migrate"}, Workloads: []string{"shop-migrate"}, Kind: "job", NodeMetadata: true},
			{Name: "shop-prune", Binary: "shop", Args: []string{"prune"}, ServiceNames: []string{"shop-prune"}, Workloads: []string{"shop-prune"}, Kind: "cron", NodeMetadata: true},
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
	if got, want := strings.Join(ids, ","), "overview,process-shop-api,process-shop-worker,workloads,releases,jobs,frontend"; got != want {
		t.Fatalf("dashboards = %s, want %s", got, want)
	}

	in := fixture()
	in.Frontends, in.HasDatabase = nil, false
	for i := range in.Processes {
		in.Processes[i].HasDatabase = false
	}
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

func TestProcessPanelsStayBoundedAndScoped(t *testing.T) {
	in := fixture()
	for i := 0; i < 100; i++ {
		in.Services[0].Methods = append(in.Services[0].Methods, Method{Name: "Extra"})
	}
	sp := Build(in)
	for _, d := range sp.Dashboards {
		if !strings.HasPrefix(d.ID, "process-") {
			continue
		}
		if len(d.Panels) > 20 {
			t.Fatalf("RPC inventory expanded %s to %d tiles", d.ID, len(d.Panels))
		}
		runtime := 0
		for _, p := range d.Panels {
			if p.ID == "go-memory" {
				runtime++
			}
			if strings.HasPrefix(p.ID, "rpc-") {
				t.Fatalf("per-RPC tile survived: %s", p.ID)
			}
			if len(p.Where) == 0 || (p.Where[0].Field != FieldService && p.Where[0].Field != fieldWorkload) {
				t.Fatalf("unscoped %s/%s", d.ID, p.ID)
			}
		}
		if runtime != 1 {
			t.Fatalf("%s has %d runtime memory panels", d.ID, runtime)
		}
		_, rpc := findPanel(d, "red-by-rpc")
		if rpc != (d.ID == "process-shop-api") {
			t.Fatalf("incorrect RPC ownership for %s", d.ID)
		}
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
		// The Jobs dashboard is all SQL panels, which take the selection only
		// through the service resolver and the console's own environment scope
		// ({env_ids}). A deployment.environment.name filter would be pushed onto
		// each SQL panel as a predicate, which a SQL panel refuses.
		if d.ID == JobsID {
			if !got[FieldService] || got[FieldEnv] {
				t.Errorf("jobs: filters %v, want only service.name", d.Filters)
			}
			continue
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

	if s := series("orders-created"); len(s) != 1 || s[0].Agg != AggRate || s[0].MetricType != "sum" || s[0].Label != "per second" {
		t.Errorf("counter series = %+v, want one per-second rate", s)
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
