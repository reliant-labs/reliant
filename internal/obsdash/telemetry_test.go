package obsdash

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// The vendor facts below (metric names, ports) are illustrative fixtures, as
// a project would declare them: forge knows none of them.

const partsDashboardJSON = `{"id":"clickhouse-parts","title":"ClickHouse parts","panels":[
  {"id":"parts","title":"Active parts","kind":"line","signal":"metrics","series":[{"label":"parts","agg":"last","field":"ClickHouseMetrics_PartsActive","metricType":"gauge"}]},
  {"id":"merge-p99","title":"Merge p99","kind":"number","signal":"metrics","series":[{"label":"p99","agg":"quantile","quantile":0.99,"field":"ch_merge_seconds","metricType":"summary"}]}
]}`

// temporalTarget is an image-only cluster workload that is scraped, pushes
// traces, is checked and has its profiles pulled: every section.
func temporalTarget() telemetry.Target {
	return telemetry.Target{
		ServiceName: "temporal", Scope: telemetry.ScopeProject, Source: "workload:temporal", Attachment: telemetry.AttachWorkload,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-acme", Namespaces: []string{"acme-prod"}, Selector: map[string]string{"app.kubernetes.io/name": "temporal"},
		Ports:         map[string]int{"metrics": 9090, "grpc": 7233},
		Metrics:       []telemetry.MetricsScrape{{Port: 9090, PortName: "metrics", Allow: "service_.*", SampleLimit: 8000}},
		OTLP:          &telemetry.OTLPPush{Signals: []string{"traces"}},
		Checks:        []telemetry.HealthCheck{{Kind: "tcp", Port: 7233}, {Kind: "http", Port: 7243, Path: "/health"}},
		Profiles:      []telemetry.PprofScrape{{Port: 7936, Types: []string{"heap", "cpu"}}},
		Dashboard:     telemetry.SystemDashboard{Enabled: true},
		DefaultAlerts: true,
	}
}

// clickhouseTarget is a platform-scope raw group with a declared dashboard.
func clickhouseTarget() telemetry.Target {
	return telemetry.Target{
		ServiceName: "clickhouse", Scope: telemetry.ScopePlatform, Source: "manifests:clickhouse", Attachment: telemetry.AttachManifests,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-acme", Namespaces: []string{"observability"}, Selector: map[string]string{"app": "clickhouse"},
		Metrics:       []telemetry.MetricsScrape{{Port: 9363, Allow: "ClickHouseMetrics_.*"}},
		Dashboard:     telemetry.SystemDashboard{Enabled: true},
		Dashboards:    []json.RawMessage{json.RawMessage(partsDashboardJSON)},
		DefaultAlerts: true,
	}
}

func telemetryFixture(t *testing.T) Input {
	t.Helper()
	systems, err := TelemetrySystemsOf([]EnvTelemetry{{Env: "prod", Targets: telemetry.Normalize([]telemetry.Target{temporalTarget(), clickhouseTarget()})}})
	if err != nil {
		t.Fatal(err)
	}
	in := fixture()
	in.Telemetry = systems
	return in
}

func TestTelemetrySystemsOfMergesEveryEnv(t *testing.T) {
	dev := telemetry.Target{ServiceName: "temporal", Scope: telemetry.ScopeProject, Source: "hostinfra:temporal", Attachment: telemetry.AttachHostInfra, Runtime: telemetry.RuntimeHost,
		Address: "127.0.0.1", Checks: []telemetry.HealthCheck{{Kind: "tcp", Port: 7233}}, Metrics: []telemetry.MetricsScrape{{Port: 9233, SampleLimit: 20000}}, Dashboard: telemetry.SystemDashboard{Enabled: true}}
	prod := temporalTarget()
	prod.Checks = nil
	systems, err := TelemetrySystemsOf([]EnvTelemetry{{Env: "dev", Targets: []telemetry.Target{dev}}, {Env: "prod", Targets: []telemetry.Target{prod}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(systems) != 1 {
		t.Fatalf("one service in two envs is one system, got %d", len(systems))
	}
	s := systems[0]
	if !s.TCPChecks || !s.Traces || !s.NodeMetadata || len(s.ProfileTypes) != 2 {
		t.Errorf("signals of either env must union: %+v", s)
	}
	if len(s.Scrapes) != 1 || s.Scrapes[0].SampleLimit != 20000 {
		t.Errorf("one unnamed scrape in both envs is one scrape at the larger limit: %+v", s.Scrapes)
	}
	if !slices.Equal(s.Workloads, []string{"temporal"}) || !s.DefaultAlerts {
		t.Errorf("workloads %v, default alerts %v", s.Workloads, s.DefaultAlerts)
	}
}

func TestTelemetrySystemsOfRefusesWhatEnvsDisagreeOn(t *testing.T) {
	platform := temporalTarget()
	platform.Scope = telemetry.ScopePlatform
	_, err := TelemetrySystemsOf([]EnvTelemetry{{Env: "dev", Targets: []telemetry.Target{temporalTarget()}}, {Env: "prod", Targets: []telemetry.Target{platform}}})
	if err == nil || !strings.Contains(err.Error(), "env dev") || !strings.Contains(err.Error(), "env prod") || !strings.Contains(err.Error(), "scope") {
		t.Errorf("a scope that differs per env must name both envs: %v", err)
	}

	other := clickhouseTarget()
	other.Dashboards = []json.RawMessage{json.RawMessage(`{"id":"clickhouse-parts","title":"Other","panels":[{"id":"p","title":"P","kind":"line","signal":"logs","series":[{"label":"n","agg":"count"}]}]}`)}
	_, err = TelemetrySystemsOf([]EnvTelemetry{{Env: "dev", Targets: []telemetry.Target{clickhouseTarget()}}, {Env: "prod", Targets: []telemetry.Target{other}}})
	if err == nil || !strings.Contains(err.Error(), `"clickhouse-parts"`) || !strings.Contains(err.Error(), "env dev") || !strings.Contains(err.Error(), "env prod") {
		t.Errorf("a dashboard declared differently in two envs must name both: %v", err)
	}

	systems, err := TelemetrySystemsOf([]EnvTelemetry{{Env: "dev", Targets: []telemetry.Target{clickhouseTarget()}}, {Env: "prod", Targets: []telemetry.Target{clickhouseTarget()}}})
	if err != nil || len(systems[0].Dashboards) != 1 {
		t.Errorf("the same dashboard in two envs is one dashboard: %v, %+v", err, systems)
	}

	bad := clickhouseTarget()
	bad.Dashboards = []json.RawMessage{json.RawMessage(`{"id":"x","title":"X","panels":[{"id":"p","title":"P","kind":"line","signal":"metrics","series":[{"label":"r","agg":"rate","field":"g","metricType":"gauge"}]}]}`)}
	if _, err := TelemetrySystemsOf([]EnvTelemetry{{Env: "prod", Targets: []telemetry.Target{bad}}}); err == nil || !strings.Contains(err.Error(), "manifests:clickhouse") {
		t.Errorf("a declared dashboard dashspec refuses (rate on a gauge) must fail, naming its declaration: %v", err)
	}
}

func TestDeclaredDashboardTakesTheSystemScope(t *testing.T) {
	in := telemetryFixture(t)
	var ch TelemetrySystem
	for _, s := range in.Telemetry {
		if s.Service == "clickhouse" {
			ch = s
		}
	}
	if len(ch.Dashboards) != 1 || ch.Dashboards[0].Scope != dashspec.ScopePlatform {
		t.Fatalf("a platform system's dashboard with no scope is scope platform: %+v", ch.Dashboards)
	}
}

// TestGoldenSystemDashboards pins the system dashboards (every section, and
// the platform-scope collection-only one) and requires each to pass what a
// release cut asks of a dashboard file and to render for the local HyperDX.
func TestGoldenSystemDashboards(t *testing.T) {
	in := telemetryFixture(t)
	for _, s := range in.Telemetry {
		d := BuildSystemDashboard(in.Project, s)
		data, err := marshalDashboardFile(d)
		if err != nil {
			t.Fatal(err)
		}
		file := DashboardFile(in.Project, d)
		if _, err := ParseDashboard(file, data); err != nil {
			t.Fatalf("system dashboard %s is invalid:\n%v", d.ID, err)
		}
		golden(t, strings.TrimSuffix(file, ".json")+".spec.json", data)
		files, err := RenderHyperDX(Spec{Project: in.Project, Dashboards: []Dashboard{d}})
		if err != nil {
			t.Fatalf("system dashboard %s does not render for HyperDX: %v", d.ID, err)
		}
		for _, f := range files {
			golden(t, f.File, f.Content)
		}
	}
}

func TestSystemDashboardDrawsOnlyWhatIsDeclared(t *testing.T) {
	logsOnly := TelemetrySystem{Service: "mailer", SystemDashboard: true}
	d := BuildSystemDashboard("shop", logsOnly)
	var sections []string
	for _, s := range d.Sections {
		sections = append(sections, s.ID)
	}
	if !slices.Equal(sections, []string{"logs"}) {
		t.Errorf("a system that declares only a log parse draws only logs: %v", sections)
	}
	for _, p := range d.Panels {
		if len(p.Where) == 0 || p.Where[0].Field != FieldService || p.Where[0].Op != OpEq || p.Where[0].Value != "mailer" {
			t.Errorf("panel %s is not scoped to the system's service.name: %+v", p.ID, p.Where)
		}
	}
	if d.ID != "telemetry-mailer" || d.Scope != "" || d.Filters[0].Field != FieldEnv {
		t.Errorf("a project system's dashboard is project-scoped with an env filter: %+v", d)
	}

	full := telemetryFixture(t).Telemetry[1] // temporal
	d = BuildSystemDashboard("shop", full)
	for _, p := range d.Panels {
		if p.Section == "resources" && p.Where[0].Field != fieldWorkload {
			t.Errorf("a workload's node metrics are keyed by its workload name: %s %+v", p.ID, p.Where)
		}
	}
	custom := full
	custom.Title = "Temporal cluster"
	if BuildSystemDashboard("shop", custom).Title != "Temporal cluster" {
		t.Error("SystemDashboard.title must replace the title")
	}
}

func TestWriteTelemetryDashboards(t *testing.T) {
	dir := t.TempDir()
	in := telemetryFixture(t)
	cs := &checksums.FileChecksums{}
	written, err := Write(dir, in, cs)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		DashboardsDir + "/shop-telemetry-temporal_gen.json",
		DashboardsDir + "/shop-telemetry-clickhouse_gen.json",
		DashboardsDir + "/shop-clickhouse-parts_gen.json",
		TelemetrySpecFile("temporal"),
		TelemetrySpecFile("clickhouse"),
	} {
		if !slices.Contains(written, want) {
			t.Errorf("%s not written (wrote %v)", want, written)
		}
	}
	// The local file of a declared dashboard draws what HyperDX can: the
	// summary quantile is the console's.
	local, err := os.ReadFile(filepath.Join(dir, DashboardsDir, "shop-clickhouse-parts_gen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(local), "t-parts") || strings.Contains(string(local), "t-merge-p99") {
		t.Errorf("the local declared dashboard must keep what HyperDX draws and drop the rest:\n%s", local)
	}

	sp, ok, err := ReadSpec(dir)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var ids []string
	for _, d := range sp.Dashboards {
		ids = append(ids, d.ID)
	}
	for _, want := range []string{"telemetry-clickhouse", "clickhouse-parts", "telemetry-temporal"} {
		if !slices.Contains(ids, want) {
			t.Errorf("ReadSpec lost %s: %v", want, ids)
		}
	}

	set, err := CollectBundleSet(BundleSetInput{ProjectDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	shipped := map[string]BundleDashboard{}
	for _, d := range set.Dashboards {
		shipped[d.Name] = d
	}
	parts, ok := shipped["shop-clickhouse-parts"]
	if !ok || len(parts.Dashboard.Panels) != 2 {
		t.Fatalf("the bundle must ship the declared dashboard whole: %+v", parts)
	}
	if _, ok := shipped["shop-telemetry-temporal"]; !ok {
		t.Error("the bundle must ship the system dashboard")
	}

	// A hand file replaces the system dashboard: its _gen twin is retired and
	// the bundle ships the hand file.
	own := Dashboard{ID: "telemetry-temporal", Title: "Mine", Panels: []Panel{{ID: "x", Title: "X", Kind: KindLine, Signal: Logs, Series: []Series{{Label: "n", Agg: AggCount}}}}}
	data, err := marshalDashboardFile(own)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, OverridePath("shop", own)), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, in, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DashboardsDir, "shop-telemetry-temporal_gen.json")); !os.IsNotExist(err) {
		t.Errorf("the overridden system dashboard's _gen file must be retired: %v", err)
	}
	set, err = CollectBundleSet(BundleSetInput{ProjectDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range set.Dashboards {
		if d.Dashboard.ID == "telemetry-temporal" && (d.Source != SourceUser || d.Dashboard.Title != "Mine") {
			t.Errorf("the hand file must replace the system dashboard in the bundle: %+v", d)
		}
	}

	// A system the project no longer declares leaves nothing behind.
	if _, err := Write(dir, fixture(), cs); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{TelemetrySpecFile("clickhouse"), DashboardsDir + "/shop-clickhouse-parts_gen.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s must be retired once undeclared: %v", gone, err)
		}
	}
}

// TestWriteWithoutTelemetryIsUnchanged is the semver guard: a project that
// declares no telemetry writes exactly the files, and the alert catalog, it
// wrote before declared telemetry existed (the shop goldens and
// alert-defaults.json pin their bytes).
func TestWriteWithoutTelemetryIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	written, err := Write(dir, fixture(), &checksums.FileChecksums{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range written {
		if strings.Contains(f, "telemetry") {
			t.Errorf("a project with no telemetry wrote %s", f)
		}
	}
	catalog, err := os.ReadFile(filepath.Join(dir, AlertDefaultsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(catalog), "telemetry") {
		t.Errorf("the alert catalog of a project with no telemetry lists a telemetry default:\n%s", catalog)
	}
}

func TestDeclaredDashboardCannotTakeAGeneratedID(t *testing.T) {
	in := fixture()
	in.Telemetry = []TelemetrySystem{{Service: "x", Dashboards: []Dashboard{{ID: "overview", Title: "Mine", Panels: []Panel{{ID: "p", Title: "P", Kind: KindLine, Signal: Logs, Series: []Series{{Label: "n", Agg: AggCount}}}}}}}}
	if _, err := Write(t.TempDir(), in, &checksums.FileChecksums{}); err == nil || !strings.Contains(err.Error(), `"overview"`) {
		t.Errorf("a declared dashboard named like the overview must be refused: %v", err)
	}
}
