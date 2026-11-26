package lint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/internal/lint/secrets"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// telSeries is one metrics series of a test dashboard.
type telSeries struct {
	agg, field, metricType string
}

// telDashboard is a forge.dashboards/v1 object with one metrics line panel
// per series, as a Telemetry's dashboards render into its target.
func telDashboard(t *testing.T, id string, series ...telSeries) json.RawMessage {
	t.Helper()
	var panels []map[string]any
	for i, s := range series {
		row := map[string]any{"label": fmt.Sprintf("s%d", i), "agg": s.agg, "field": s.field, "metricType": s.metricType}
		if s.agg == "quantile" {
			row["quantile"] = 0.95
		}
		panels = append(panels, map[string]any{
			"id": fmt.Sprintf("p%d", i), "title": fmt.Sprintf("Panel %d", i), "kind": "line", "signal": "metrics",
			"series": []any{row},
		})
	}
	raw, err := json.Marshal(map[string]any{"id": id, "title": id, "panels": panels})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func telScrape(allow string) telemetry.MetricsScrape {
	return telemetry.MetricsScrape{Port: 9363, Path: "/metrics", Allow: allow, DropLabels: []string{"user_id"}, SampleLimit: 5000}
}

func telTarget(name string, scrapes ...telemetry.MetricsScrape) telemetry.Target {
	return telemetry.Target{ServiceName: name, Source: "observability", Attachment: telemetry.AttachObservability, Metrics: scrapes}
}

func runTelemetry(targets ...telemetry.Target) map[string][]finding.Finding {
	return ruleSet(checkTelemetry([]telemetryEnv{{Name: "prod", File: "deploy/kcl/prod/main.k", Targets: targets}}, nil))
}

func TestTelemetryScrapeUnbounded(t *testing.T) {
	unbounded := telScrape("")
	got := runTelemetry(telTarget("clickhouse", unbounded))[ruleTelemetryScrapeUnbounded]
	if len(got) != 1 || got[0].Severity != finding.SeverityWarning || !strings.Contains(got[0].Message, `"clickhouse"`) {
		t.Fatalf("a scrape with no allow: %+v", got)
	}
	if got := runTelemetry(telTarget("clickhouse", telScrape("x_.*")))[ruleTelemetryScrapeUnbounded]; len(got) != 0 {
		t.Fatalf("a scrape with allow is bounded: %+v", got)
	}
}

func TestTelemetryHighCardinalityRisk(t *testing.T) {
	cases := []struct {
		name   string
		scrape telemetry.MetricsScrape
		want   bool
	}{
		{"bounded, labels dropped", telScrape("x_.*"), false},
		{"bounded, no drop_labels, default limit", telemetry.MetricsScrape{Port: 1, Allow: "x_.*", SampleLimit: 5000}, false},
		{"raised limit, no drop_labels", telemetry.MetricsScrape{Port: 1, Allow: "x_.*", SampleLimit: 20000}, true},
		{"raised limit, labels dropped", telemetry.MetricsScrape{Port: 1, Allow: "x_.*", SampleLimit: 20000, DropLabels: []string{"id"}}, false},
		{"no allow, no drop_labels", telemetry.MetricsScrape{Port: 1, SampleLimit: 5000}, true},
		{"no allow, labels dropped", telemetry.MetricsScrape{Port: 1, SampleLimit: 5000, DropLabels: []string{"id"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runTelemetry(telTarget("svc", tc.scrape))[ruleTelemetryHighCardinality]
			if (len(got) == 1) != tc.want || len(got) > 1 {
				t.Fatalf("want finding=%v, got %+v", tc.want, got)
			}
			if tc.want && got[0].Severity != finding.SeverityWarning {
				t.Errorf("severity %s, want warning", got[0].Severity)
			}
		})
	}
}

func TestTelemetryDashboardMetricUncollected(t *testing.T) {
	withDash := func(tg telemetry.Target, series ...telSeries) telemetry.Target {
		tg.Dashboards = []json.RawMessage{telDashboard(t, "ch", series...)}
		return tg
	}
	cases := []struct {
		name   string
		target telemetry.Target
		others []telemetry.Target
		want   bool
	}{
		{"kept by allow", withDash(telTarget("ch", telScrape("ClickHouseMetrics_.*")), telSeries{"last", "ClickHouseMetrics_PartsActive", "gauge"}), nil, false},
		{"not kept by allow", withDash(telTarget("ch", telScrape("ClickHouseMetrics_.*")), telSeries{"last", "ClickHouseAsync_Mem", "gauge"}), nil, true},
		{"dropped by deny", withDash(telTarget("ch", telemetry.MetricsScrape{Port: 1, Allow: "ch_.*", Deny: "ch_debug_.*", DropLabels: []string{"x"}}), telSeries{"last", "ch_debug_x", "gauge"}), nil, true},
		{"no allow keeps everything", withDash(telTarget("ch", telemetry.MetricsScrape{Port: 1}), telSeries{"last", "anything", "gauge"}), nil, false},
		{"histogram drawn by base, bucket allowed", withDash(telTarget("ch", telScrape("req_seconds_(bucket|sum|count)")), telSeries{"quantile", "req_seconds", "histogram"}), nil, false},
		{"histogram not allowed", withDash(telTarget("ch", telScrape("other_.*")), telSeries{"quantile", "req_seconds", "histogram"}), nil, true},
		{"summary drawn by base, base allowed", withDash(telTarget("ch", telScrape("rpc_latency")), telSeries{"quantile", "rpc_latency", "summary"}), nil, false},
		{"sum named exactly is not a histogram", withDash(telTarget("ch", telScrape("req_seconds_count")), telSeries{"rate", "req_seconds", "sum"}), nil, true},
		{"no scrape at all", withDash(telTarget("ch"), telSeries{"last", "m", "gauge"}), nil, true},
		{"pushed over OTLP", func() telemetry.Target {
			tg := withDash(telTarget("ch"), telSeries{"last", "m", "gauge"})
			tg.OTLP = &telemetry.OTLPPush{Signals: []string{"traces", "metrics"}}
			return tg
		}(), nil, false},
		{"an OpenTelemetry name is not judged", withDash(telTarget("ch", telScrape("x")), telSeries{"last", "k8s.pod.cpu.time", "sum"}), nil, false},
		{"kept by a neighbour's scrape", withDash(telTarget("ch"), telSeries{"last", "node_load1", "gauge"}), []telemetry.Target{telTarget("node", telScrape("node_.*"))}, false},
		{"an allow that does not compile assumes nothing", withDash(telTarget("ch", telScrape("(")), telSeries{"last", "m", "gauge"}), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runTelemetry(append([]telemetry.Target{tc.target}, tc.others...)...)[ruleTelemetryMetricUncollected]
			if (len(got) == 1) != tc.want || len(got) > 1 {
				t.Fatalf("want finding=%v, got %+v", tc.want, got)
			}
			if tc.want && (got[0].Severity != finding.SeverityError || !strings.Contains(got[0].Message, `dashboard "ch" panel "p0"`)) {
				t.Errorf("want an error naming the dashboard and panel, got %+v", got[0])
			}
		})
	}
}

func TestTelemetryRateOnGauge(t *testing.T) {
	target := func(scrape telemetry.MetricsScrape, s telSeries) telemetry.Target {
		tg := telTarget("svc", scrape)
		tg.Dashboards = []json.RawMessage{telDashboard(t, "svc-dash", s)}
		return tg
	}
	cases := []struct {
		name   string
		target telemetry.Target
		want   bool
	}{
		{"rate on _total", target(telScrape(".*"), telSeries{"rate", "http_requests_total", "sum"}), false},
		{"increase on _count", target(telScrape(".*"), telSeries{"increase", "jobs_count", "sum"}), false},
		{"rate on _sum", target(telScrape(".*"), telSeries{"rate", "bytes_sum", "sum"}), false},
		{"rate on an unmarked name", target(telScrape(".*"), telSeries{"rate", "ClickHouseProfileEvents_Query", "sum"}), true},
		{"rate on a name counters marks", target(telemetry.MetricsScrape{Port: 1, Allow: ".*", Counters: "ClickHouseProfileEvents_.*", DropLabels: []string{"x"}, SampleLimit: 5000}, telSeries{"rate", "ClickHouseProfileEvents_Query", "sum"}), false},
		{"last on an unmarked name", target(telScrape(".*"), telSeries{"last", "ClickHouseMetrics_Query", "gauge"}), false},
		{"rate on a histogram", target(telScrape(".*"), telSeries{"rate", "latency", "histogram"}), false},
		{"rate on an uncollected name is the other rule's", target(telScrape("only_this"), telSeries{"rate", "unmarked", "sum"}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runTelemetry(tc.target)[ruleTelemetryRateOnGauge]
			if (len(got) == 1) != tc.want || len(got) > 1 {
				t.Fatalf("want finding=%v, got %+v", tc.want, got)
			}
			if tc.want && got[0].Severity != finding.SeverityWarning {
				t.Errorf("severity %s, want warning", got[0].Severity)
			}
		})
	}
}

// One declaration rendered into several envs is one finding naming them all.
func TestTelemetryFindingsMergeAcrossEnvs(t *testing.T) {
	tg := telTarget("clickhouse", telScrape(""))
	got := checkTelemetry([]telemetryEnv{
		{Name: "staging", File: "deploy/kcl/staging/main.k", Targets: []telemetry.Target{tg}},
		{Name: "prod", File: "deploy/kcl/prod/main.k", Targets: []telemetry.Target{tg}},
	}, newTelemetryLocator([]secrets.SourceFile{{Path: "deploy/kcl/lib/telemetry.k", Text: "_ch = forge.Telemetry {\n    service_name = \"clickhouse\"\n    metrics = [forge.MetricsScrape {port = 9363}]\n}\n"}}))
	unb := ruleSet(got)[ruleTelemetryScrapeUnbounded]
	if len(unb) != 1 || !strings.HasPrefix(unb[0].Message, "envs prod, staging: ") {
		t.Fatalf("want one finding naming both envs, got %+v", unb)
	}
	if unb[0].File != "deploy/kcl/lib/telemetry.k" || unb[0].Line != 3 {
		t.Errorf("anchored at %s:%d, want the shared lib's MetricsScrape at line 3", unb[0].File, unb[0].Line)
	}
}

// The locator puts a finding on the line that declares it: a scrape by its
// name, a series by its metric under its dashboard's and panel's ids, and
// never on another env's main.k.
func TestTelemetryLocator(t *testing.T) {
	prod := "_ch = forge.Telemetry {\n" + // 1
		"    service_name = \"clickhouse\"\n" + // 2
		"    metrics = [\n" + // 3
		"        forge.MetricsScrape {name = \"server\", port = 9363, allow = \"a\"}\n" + // 4
		"        forge.MetricsScrape {name = \"keeper\", port = 9182}\n" + // 5
		"    ]\n" + // 6
		"    dashboards = [ds.Dashboard {\n" + // 7
		"        id = \"ch\"\n" + // 8
		"        panels = [ds.Panel {id = \"p0\", title = \"x\", kind = \"line\", signal = \"metrics\"\n" + // 9
		"            series = [ds.gauge(\"s\", \"missing_metric\")]\n" + // 10
		"        }]\n" + // 11
		"    }]\n" + // 12
		"}\n"
	other := "# a decoy: another env declares the same service\n_x = forge.Telemetry {service_name = \"clickhouse\"}\n"
	loc := newTelemetryLocator([]secrets.SourceFile{
		{Path: "deploy/kcl/prod/main.k", Text: prod},
		{Path: "deploy/kcl/dev/main.k", Text: other},
	})
	env := telemetryEnv{Name: "prod", File: "deploy/kcl/prod/main.k"}
	tg := telTarget("clickhouse",
		telemetry.MetricsScrape{Name: "server", Port: 9363, Allow: "a"},
		telemetry.MetricsScrape{Name: "keeper", Port: 9182, Deny: "missing_.*"})
	tg.Dashboards = []json.RawMessage{telDashboard(t, "ch", telSeries{"last", "missing_metric", "gauge"})}
	env.Targets = []telemetry.Target{tg}

	got := ruleSet(checkTelemetry([]telemetryEnv{env}, loc))
	if f := got[ruleTelemetryScrapeUnbounded]; len(f) != 1 || f[0].File != env.File || f[0].Line != 5 {
		t.Errorf("unbounded keeper scrape: %+v, want %s:5", f, env.File)
	}
	if f := got[ruleTelemetryMetricUncollected]; len(f) != 1 || f[0].File != env.File || f[0].Line != 10 {
		t.Errorf("uncollected series: %+v, want %s:10", f, env.File)
	}
	if f := checkTelemetry([]telemetryEnv{{Name: "dev", File: "deploy/kcl/dev/main.k", Targets: []telemetry.Target{telTarget("clickhouse", telScrape(""))}}}, loc); len(f) != 1 || f[0].File != "deploy/kcl/dev/main.k" || f[0].Line != 2 {
		t.Errorf("dev's declaration: %+v, want deploy/kcl/dev/main.k:2 (comment lines skipped)", f)
	}
}

func TestTelemetryAdopted(t *testing.T) {
	for text, want := range map[string]bool{
		"x = forge.Telemetry {service_name = \"a\"}": true,
		"x = forge.Telemetry{}":                      true,
		"# forge.Telemetry {}":                       false,
		"t = forge.BrowserTelemetry {replay = True}": false,
		"telemetry = []":                             false,
	} {
		if got := telemetryAdopted([]secrets.SourceFile{{Path: "a.k", Text: text}}); got != want {
			t.Errorf("%q: adopted=%v, want %v", text, got, want)
		}
	}
}

func TestTelemetryLaneIsRegistered(t *testing.T) {
	found := false
	for _, s := range lintPipeline() {
		if s.name == "telemetry lint" {
			found = true
			if !s.gates || s.scope != scopeByFile {
				t.Errorf("telemetry lint: gates=%v scope=%v, want a gating lane scoped by file", s.gates, s.scope)
			}
		}
	}
	if !found {
		t.Fatal("telemetry lint is not in the forge lint pipeline")
	}
	for _, r := range []string{ruleTelemetryScrapeUnbounded, ruleTelemetryMetricUncollected, ruleTelemetryRateOnGauge, ruleTelemetryHighCardinality} {
		ro, ok := lintRollouts[r]
		if !ok || ro.GatesFrom == "" {
			t.Errorf("%s: rollout %+v, want a warn-first row", r, ro)
		}
	}
}

const telemetryMainK = `import forge
import forge.dashspec as ds

_parts = ds.Dashboard {
    id = "clickhouse-parts"
    title = "ClickHouse parts"
    panels = [ds.Panel {
        id = "parts", title = "Active parts", kind = "line", signal = "metrics"
        series = [ds.gauge("parts", "ClickHouseMetrics_PartsActive")]
    }]
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [
            forge.Telemetry {
                service_name = "clickhouse"
                scope = "platform"
                address = "127.0.0.1"
                metrics = [forge.MetricsScrape {port = 9363, allow = "ClickHouseAsync_.*"}]
                dashboards = [_parts]
            }
        ]
    }
})
`

// The lane renders each env and judges its output.telemetry, with the
// finding on the KCL line that declares it.
func TestTelemetryLaneJudgesARenderedEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"forge.yaml":            "name: acme\nmodule_path: github.com/acme/acme\n",
		"deploy/kcl/kcl.mod":    secretsKCLMod,
		"deploy/kcl/dev/main.k": telemetryMainK,
	})
	res := telemetryFindings(context.Background(), root, &config.ProjectConfig{})
	if !res.adopted || res.envs != 1 || res.targets != 1 || len(res.unrendered) != 0 {
		t.Fatalf("adopted=%v envs=%d targets=%d unrendered=%v", res.adopted, res.envs, res.targets, res.unrendered)
	}
	got := ruleSet(res.findings)
	unc := got[ruleTelemetryMetricUncollected]
	if len(unc) != 1 || unc[0].File != "deploy/kcl/dev/main.k" {
		t.Fatalf("uncollected: %+v", unc)
	}
	if line := mainKLineIn(t, filepath.Join(root, "deploy/kcl/dev/main.k"), `"ClickHouseMetrics_PartsActive"`); unc[0].Line != line {
		t.Errorf("anchored at line %d, want the series at %d", unc[0].Line, line)
	}
	if n := len(res.findings); n != 1 {
		t.Errorf("want only the uncollected series, got %+v", res.findings)
	}

	// A project with no forge.Telemetry is not rendered.
	clean := t.TempDir()
	writeTree(t, clean, map[string]string{"deploy/kcl/dev/main.k": "this would not render\n"})
	if res := telemetryFindings(context.Background(), clean, &config.ProjectConfig{}); res.adopted || len(res.findings) != 0 {
		t.Errorf("a project without telemetry: %+v", res)
	}
}

func mainKLineIn(t *testing.T, path, needle string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("%q not in %s", needle, path)
	return 0
}
