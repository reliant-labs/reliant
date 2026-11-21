package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// pinnedCollector is the collector the local agent runs, fetched into the
// release cache (a skip when the host cannot download it).
func pinnedCollector(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	bin, err := hostinfra.EnsureOtelcolContrib(ctx)
	if err != nil {
		if hostinfra.IsDownloadUnavailable(err) {
			t.Skipf("host resource missing: the otelcol-contrib release download is unreachable: %v", err)
		}
		t.Fatal(err)
	}
	return bin
}

// The project's collector file as scaffolded, with the includes `forge env
// up` writes, parses a declared target's lines in the pinned collector: the
// include mechanism (${file:} in an operator list and as a processor's whole
// config, `$$` escapes inside an included file), the preserve operator ahead
// of forge's generic JSON stage, and the parse winning over it.
func TestCollectorFileParsesDeclaredLogsThroughItsIncludes(t *testing.T) {
	testkit.Slow(t, "runs the pinned otelcol-contrib over a log file")
	bin := pinnedCollector(t)
	dir := t.TempDir()
	logsDir := filepath.Join(dir, ".forge", "logs")
	if err := os.MkdirAll(filepath.Join(logsDir, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := hostEngineTarget()
	target.Logs = &telemetry.LogParse{
		Format:    "json",
		Timestamp: &telemetry.LogTimestamp{Field: "ts", Layout: "epoch_s"},
		Severity:  &telemetry.LogSeverity{Field: "level"},
		Body:      "msg",
		Drop:      []string{"logging-call-at"},
		Rules:     []telemetry.LogSeverityRule{{BodyMatch: `not found$`, FromSeverity: "error", Severity: "warn"}},
	}
	logs, err := localTelemetryLogs(telemetryEntities(target))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeTelemetryIncludes(dir, logs); err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.ProjectTemplates().Render("otel-collector.yaml.tmpl", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	// The container's mount point, here.
	project := strings.ReplaceAll(string(tmpl), "/var/log/forge", logsDir)
	out := filepath.Join(dir, "out.json")
	// What ClickStack's own config defines, and a file instead of ClickHouse.
	base := map[string]any{
		"processors": map[string]any{
			"memory_limiter": map[string]any{"check_interval": "1s", "limit_mib": 100},
			"transform":      map[string]any{"error_mode": "ignore"},
			"batch":          map[string]any{"timeout": "200ms"},
		},
		"exporters": map[string]any{"file": map[string]any{"path": out}},
		"service":   map[string]any{"telemetry": map[string]any{"metrics": map[string]any{"level": "none"}}},
	}
	override := map[string]any{
		"receivers": map[string]any{"filelog/forge": map[string]any{"start_at": "beginning"}},
		"service":   map[string]any{"pipelines": map[string]any{"logs/forge": map[string]any{"exporters": []any{"file"}}}},
	}
	paths := map[string][]byte{"project.yaml": []byte(project)}
	for name, doc := range map[string]any{"base.yaml": base, "override.yaml": override} {
		b, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		paths[name] = b
	}
	var args []string
	for _, name := range []string{"base.yaml", "project.yaml", "override.yaml"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, paths[name], 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--config="+p)
	}
	lines := `{"level":"error","ts":1763614801.25,"msg":"workflow not found","logging-call-at":"handler.go:12","wf":"a"}` + "\n" +
		`{"level":"info","ts":1763614802,"msg":"started"}` + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "dev", "temporal.log"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- the pinned collector
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var records []map[string]any
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && len(records) < 2; time.Sleep(250 * time.Millisecond) {
		records = logRecords(t, out)
	}
	if len(records) < 2 {
		t.Fatalf("the collector exported %d record(s), want 2\nstderr:\n%s", len(records), stderr.String())
	}
	first := records[0]
	if first["severityNumber"] != float64(13) {
		t.Errorf("first record severity = %v %v, want WARN (13) from the demotion rule", first["severityNumber"], first["severityText"])
	}
	if second := records[1]; second["severityNumber"] != float64(9) {
		t.Errorf("second record severity = %v %v, want INFO (9) from the declared level", second["severityNumber"], second["severityText"])
	}
	if body, _ := first["body"].(map[string]any); body["stringValue"] != "workflow not found" {
		t.Errorf("first record body = %v", first["body"])
	}
	if first["timeUnixNano"] != "1763614801250000000" {
		t.Errorf("first record time = %v, want the declared ts", first["timeUnixNano"])
	}
	attrs := fmt.Sprint(first["attributes"])
	for _, gone := range []string{"forge.telemetry", "logging-call-at"} {
		if strings.Contains(attrs, gone) {
			t.Errorf("attribute %s survived: %s", gone, attrs)
		}
	}
	if !strings.Contains(attrs, `wf`) {
		t.Errorf("the line's own fields were dropped: %s", attrs)
	}
}

// logRecords reads the file exporter's output: one JSON ExportLogsRequest per
// line, flattened to its records (each with its resource's service.name
// checked).
func logRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own file
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var req struct {
			ResourceLogs []struct {
				Resource struct {
					Attributes []struct {
						Key   string         `json:"key"`
						Value map[string]any `json:"value"`
					} `json:"attributes"`
				} `json:"resource"`
				ScopeLogs []struct {
					LogRecords []map[string]any `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		for _, rl := range req.ResourceLogs {
			svc := ""
			for _, a := range rl.Resource.Attributes {
				if a.Key == "service.name" {
					svc = fmt.Sprint(a.Value["stringValue"])
				}
			}
			if svc != "temporal" {
				t.Errorf("a record's service.name is %q, want temporal", svc)
			}
			for _, sl := range rl.ScopeLogs {
				out = append(out, sl.LogRecords...)
			}
		}
	}
	return out
}

// The whole local path for a host-run engine: a HostInfra temporal started
// with its declared temporal_metrics_port (--metrics-port), and `forge env
// up`'s telemetry step starting the pinned collector as the local agent,
// scraping that port and exporting to the local collector's endpoint — here a
// fake one that records what arrives. Then the agent is adopted unchanged,
// restarted for a changed declaration, and stopped.
func TestLocalTelemetryAgentScrapesAHostTemporal(t *testing.T) {
	testkit.Slow(t, "downloads and runs the Temporal dev server and otelcol-contrib")
	pinnedCollector(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	project := t.TempDir()
	metricsPort := freePort(t)
	engine := hostinfra.Spec{
		Name: "temporal", Env: "dev", Engine: hostinfra.EngineTemporal,
		Port: freePort(t), TemporalMetricsPort: metricsPort,
	}
	if err := hostinfra.Start(ctx, project, engine); err != nil {
		if hostinfra.IsDownloadUnavailable(err) {
			t.Skipf("host resource missing: the Temporal release download is unreachable: %v", err)
		}
		t.Fatalf("start temporal: %v", err)
	}
	t.Cleanup(func() { _, _ = hostinfra.StopReport(project, engine) })

	sink := &otlpSink{metrics: map[string]map[string]bool{}}
	srv := httptest.NewServer(sink)
	defer srv.Close()

	target := telemetry.Target{
		ServiceName: "temporal", Scope: telemetry.ScopePlatform,
		Source: "hostinfra:temporal", Attachment: telemetry.AttachHostInfra,
		Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Ports:   map[string]int{"server": engine.Port, "metrics": metricsPort},
		Metrics: []telemetry.MetricsScrape{{PortName: "metrics", Interval: "10s", Timeout: "5s"}},
		Checks:  []telemetry.HealthCheck{{Kind: "tcp", PortName: "server", Interval: "10s"}},
	}
	e := telemetryEntities(target)
	e.Observability.LocalEndpoint = srv.URL
	e.Observability.ProfilesEndpoint = ""
	t.Cleanup(func() { _, _ = hostinfra.StopTelemetryAgent(project, "dev") })

	var out bytes.Buffer
	upLocalTelemetry(ctx, &out, project, "dev", e, nil)
	if !strings.Contains(out.String(), "telemetry agent: started") {
		t.Fatalf("the agent did not start:\n%s", out.String())
	}
	cfg, err := os.ReadFile(hostinfra.TelemetryAgentConfigPath(project, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "127.0.0.1:" + strconv.Itoa(metricsPort); !strings.Contains(string(cfg), want) {
		t.Fatalf("the agent's config does not scrape the declared metrics port %s:\n%s", want, cfg)
	}

	// The scrape's own series (up, scrape_*) and the check's arrive even
	// from a port that serves nothing; the engine's own series prove the
	// --metrics-port listener is there.
	engineSeries := func(names []string) []string {
		var out []string
		for _, n := range names {
			if n != "up" && !strings.HasPrefix(n, "scrape_") && !strings.HasPrefix(n, "tcpcheck.") {
				out = append(out, n)
			}
		}
		return out
	}
	var names []string
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		names = sink.names("temporal")
		if len(engineSeries(names)) > 0 && strings.Contains(strings.Join(names, " "), "tcpcheck.") {
			break
		}
	}
	if len(engineSeries(names)) == 0 {
		logs, _ := os.ReadFile(hostinfra.LogPath(project, "dev", hostinfra.TelemetryAgentName))
		t.Fatalf("no temporal metrics reached the local collector (got %v)\nagent log:\n%s", names, logs)
	}
	joined := strings.Join(names, " ")
	for _, want := range []string{"up", "tcpcheck.status"} {
		if !strings.Contains(" "+joined+" ", " "+want+" ") {
			t.Errorf("metrics %v lack %s", names, want)
		}
	}

	out.Reset()
	upLocalTelemetry(ctx, &out, project, "dev", e, nil)
	if !strings.Contains(out.String(), "telemetry agent: unchanged") {
		t.Errorf("an identical run did not adopt the agent:\n%s", out.String())
	}
	target.Metrics[0].Interval = "15s"
	out.Reset()
	upLocalTelemetry(ctx, &out, project, "dev", telemetryEntitiesAt(srv.URL, target), nil)
	if !strings.Contains(out.String(), "telemetry agent: restarted") {
		t.Errorf("a changed declaration did not restart the agent:\n%s", out.String())
	}
	stopped, err := hostinfra.StopTelemetryAgent(project, "dev")
	if err != nil || !stopped {
		t.Errorf("stop = %v, %v; want the running agent stopped", stopped, err)
	}
	if _, running := hostinfra.TelemetryAgentRunning(project, "dev"); running {
		t.Error("the agent still runs after stop")
	}
}

func telemetryEntitiesAt(endpoint string, targets ...telemetry.Target) *KCLEntities {
	e := telemetryEntities(targets...)
	e.Observability.LocalEndpoint = endpoint
	e.Observability.ProfilesEndpoint = ""
	return e
}
