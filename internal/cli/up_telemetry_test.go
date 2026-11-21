package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// The fixtures are shaped after real systems so the goldens read like the
// configs they become. The names and metric names are illustrative: forge
// knows none of them.

// hostEngineTarget is a HostInfra temporal with its metrics port declared, a
// check and a JSON log parse.
func hostEngineTarget() telemetry.Target {
	return telemetry.Target{
		ServiceName: "temporal", Scope: telemetry.ScopePlatform,
		Source: "hostinfra:temporal", Attachment: telemetry.AttachHostInfra,
		Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Ports:   map[string]int{"server": 7233, "metrics": 9233},
		Metrics: []telemetry.MetricsScrape{{PortName: "metrics", Allow: `service_requests|service_latency_(bucket|sum|count)`}},
		Checks:  []telemetry.HealthCheck{{Kind: "tcp", PortName: "server"}},
		Logs: &telemetry.LogParse{
			Format:   "json",
			Severity: &telemetry.LogSeverity{Field: "level"},
			Body:     "msg",
		},
	}
}

// hostWorkloadTarget is an OnHost process renamed in telemetry, with a
// logfmt parse and an authenticated pprof pull.
func hostWorkloadTarget() telemetry.Target {
	return telemetry.Target{
		ServiceName: "billing-worker", Scope: telemetry.ScopeProject,
		Source: "workload:worker", Attachment: telemetry.AttachWorkload,
		Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Ports: map[string]int{"debug": 6060},
		Profiles: []telemetry.PprofScrape{{
			PortName: "debug", Types: []string{"heap"}, Scheme: "https",
			TLS:         &telemetry.TLS{InsecureSkipVerify: true},
			BearerToken: &telemetry.SecretMount{Secret: "worker-pprof", Key: "token", StoreKey: "WORKER_PPROF_TOKEN"},
		}},
		Logs: &telemetry.LogParse{Format: "logfmt"},
	}
}

// clusterTarget is reached by a cluster agent, never the local stack.
func clusterTarget() telemetry.Target {
	return telemetry.Target{
		ServiceName: "openbao", Scope: telemetry.ScopePlatform,
		Source: "observability", Attachment: telemetry.AttachObservability,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub",
		Selector: map[string]string{"app": "openbao"},
		Metrics:  []telemetry.MetricsScrape{{Port: 8200}},
	}
}

func telemetryEntities(targets ...telemetry.Target) *KCLEntities {
	e := clickstackEntities(map[string]string{"CLICKSTACK_OTLP_HTTP_PORT": "4419"})
	e.Observability.ProfilesEndpoint = "http://127.0.0.1:4420/v1development/profiles"
	e.Telemetry = telemetry.Normalize(targets)
	return e
}

func goldenFile(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "telemetry", name)
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", p, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs; FORGE_UPDATE_GOLDEN=1 go test ./internal/cli -run %s rewrites it, then review the diff\n--- got\n%s", p, t.Name(), got)
	}
}

// The agent's whole configuration, as env up writes it: the host targets'
// scrapes, pull and check (the cluster target is not the local stack's),
// exported to the local collector and the profiles collector, with the
// supervisor's health extension and the self-telemetry listener off.
func TestTelemetryAgentConfigGolden(t *testing.T) {
	e := telemetryEntities(hostEngineTarget(), hostWorkloadTarget(), clusterTarget())
	plan, err := planTelemetryAgent("/project", "dev", e)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("no agent planned for pulled host targets")
	}
	if got := serviceNames(plan.Pulled); got != "temporal, billing-worker" {
		t.Errorf("pulled = %s, want the two host targets", got)
	}
	if len(plan.Mounts) != 1 || plan.Mounts[0].Path != "worker-pprof/token" {
		t.Errorf("mounts = %+v, want the pprof bearer token", plan.Mounts)
	}
	if want := []string{profilesFeatureGate}; strings.Join(plan.Agent.FeatureGates, ",") != strings.Join(want, ",") {
		t.Errorf("feature gates = %v, want %v for a profiles pipeline", plan.Agent.FeatureGates, want)
	}
	doc, err := hostinfra.RenderTelemetryAgentConfig(plan.Agent, 47001)
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "agent_dev.golden.yaml", doc)
}

// No pulled host target, no agent: logs need none (the collector tails
// them), and a cluster target is the cluster agent's.
func TestTelemetryAgentOnlyForPulledHostTargets(t *testing.T) {
	logsOnly := hostWorkloadTarget()
	logsOnly.Profiles = nil
	for name, e := range map[string]*KCLEntities{
		"nothing declared": telemetryEntities(),
		"logs only":        telemetryEntities(logsOnly),
		"cluster only":     telemetryEntities(clusterTarget()),
	} {
		plan, err := planTelemetryAgent("/project", "dev", e)
		if err != nil || plan != nil {
			t.Errorf("%s: plan = %+v, %v; want none", name, plan, err)
		}
	}
	// Without a profiles collector the pull is skipped, with a warning, and
	// no feature gate is passed.
	e := telemetryEntities(hostEngineTarget(), hostWorkloadTarget())
	e.Observability.ProfilesEndpoint = ""
	plan, err := planTelemetryAgent("/project", "dev", e)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Agent.FeatureGates) != 0 || len(plan.Warnings) == 0 {
		t.Errorf("no profiles collector: gates %v, warnings %v; want none, and a warning", plan.Agent.FeatureGates, plan.Warnings)
	}
}

// The include files: the declared parse when there is one, loadable no-ops
// when there is none (the collector file includes them either way).
func TestTelemetryIncludes(t *testing.T) {
	logs, err := localTelemetryLogs(telemetryEntities(hostEngineTarget(), hostWorkloadTarget(), clusterTarget()))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(logs.Parsed, ","); got != "temporal,billing-worker" {
		t.Errorf("parsed = %s", got)
	}
	files, err := telemetryIncludes(logs)
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	for _, name := range sortedKeys(files) {
		all.WriteString("=== " + name + "\n")
		all.Write(files[name])
	}
	goldenFile(t, "includes_dev.golden.txt", all.Bytes()) // .txt: a stream of "=== <file>" sections, not YAML (pre-commit check-yaml)

	empty, err := localTelemetryLogs(telemetryEntities())
	if err != nil {
		t.Fatal(err)
	}
	files, err = telemetryIncludes(empty)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		clickstack.TelemetryPreserveFile: "type: noop",
		clickstack.TelemetryIdentityFile: "error_mode: ignore",
		clickstack.TelemetryParseFile:    "error_mode: ignore",
	} {
		if !strings.Contains(string(files[name]), want) {
			t.Errorf("%s with nothing declared:\n%s\nwant a no-op (%s)", name, files[name], want)
		}
	}
}

// prepareLocalCollector writes the includes for ClickStack, and reports a
// change only for a file a running collector already loaded.
func TestPrepareLocalCollectorWritesTelemetryIncludes(t *testing.T) {
	dir := t.TempDir()
	e := telemetryEntities()
	changed, err := prepareLocalCollector(dir, e)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a first write reported a change: nothing running loaded it yet")
	}
	for _, f := range []string{clickstack.TelemetryPreserveFile, clickstack.TelemetryIdentityFile, clickstack.TelemetryParseFile} {
		if _, err := os.Stat(filepath.Join(dir, clickstack.TelemetryIncludeDir, f)); err != nil {
			t.Errorf("include %s not written: %v", f, err)
		}
	}
	if changed, _ := prepareLocalCollector(dir, e); changed {
		t.Error("an identical rewrite reported a change")
	}
	if changed, _ := prepareLocalCollector(dir, telemetryEntities(hostEngineTarget())); !changed {
		t.Error("a new declared parse did not report a change")
	}
	b, _ := os.ReadFile(filepath.Join(dir, clickstack.TelemetryIncludeDir, clickstack.TelemetryParseFile))
	if !strings.Contains(string(b), `resource.attributes["service.name"] == "temporal"`) {
		t.Errorf("the parse include does not route temporal's lines:\n%s", b)
	}
}

// With a destination the forwarder carries the declared parse itself.
func TestForwarderSplicesTheDeclaredParse(t *testing.T) {
	dir := t.TempDir()
	e := telemetryEntities(hostEngineTarget())
	e.Workloads = []WorkloadEntity{{Name: "otel-forwarder", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "otel-forwarder"}}}}
	e.Observability.Destination = &ObservabilityDestEntity{Endpoint: "https://otlp.example.com", Protocol: "http/protobuf"}
	e.Observability.Signals = ObservabilitySignalsEntity{Logs: true}
	if _, err := prepareLocalCollector(dir, e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, clickstack.ForwarderConfigDir, clickstack.ForwarderConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Processors map[string]any `json:"processors"`
		Service    struct {
			Pipelines map[string]struct {
				Processors []string `json:"processors"`
			} `json:"pipelines"`
		} `json:"service"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Service.Pipelines["logs"].Processors, ","); got != "memory_limiter,transform/telemetry_identity,transform/telemetry_logs,batch" {
		t.Errorf("forwarder logs processors = %s", got)
	}
	if _, ok := cfg.Processors["transform/telemetry_logs"]; !ok {
		t.Error("the parse processor is not defined in the forwarder")
	}
	if !strings.Contains(string(b), "forge_telemetry_original") {
		t.Error("the preserve operator is not in the forwarder's filelog")
	}
}

func TestWriteAgentSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	stale := filepath.Join(dir, "old", "token")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mounts := []telemetry.SecretMount{
		{Secret: "worker-pprof", Key: "token", StoreKey: "WORKER_PPROF_TOKEN", Path: "worker-pprof/token"},
		{Secret: "ca", Key: "ca.crt", StoreKey: "ABSENT_CA", Optional: true, Path: "ca/ca.crt"},
	}
	write := func(mounts []telemetry.SecretMount, store map[string]string) (string, error) {
		s, err := stageAgentSecrets(dir, mounts, store, "dev")
		if err != nil {
			return "", err
		}
		if err := s.swap(); err != nil {
			return "", err
		}
		s.commit()
		return s.revision, nil
	}
	rev, err := write(mounts, map[string]string{"WORKER_PPROF_TOKEN": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "worker-pprof", "token")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "s3cret" || info.Mode().Perm() != 0o600 {
		t.Errorf("token file = %q mode %v, want the store's value at 0600", b, info.Mode().Perm())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a credential an earlier run wrote was left behind")
	}
	_, err = write(mounts[:1], map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "secret set --env dev WORKER_PPROF_TOKEN") {
		t.Errorf("a missing store key = %v, want an error naming the fix", err)
	}
	// A failed resolve touches nothing: the running agent keeps its token.
	if b, _ := os.ReadFile(p); string(b) != "s3cret" {
		t.Errorf("a missing store key changed the live token to %q", b)
	}

	// A rotated value is another revision (the agent restarts on it); the
	// same value is the same one.
	same, err := write(mounts, map[string]string{"WORKER_PPROF_TOKEN": "s3cret"})
	if err != nil || same != rev || rev == "" {
		t.Errorf("the same credentials = revision %q (err %v), want %q", same, err, rev)
	}
	rotated, err := write(mounts, map[string]string{"WORKER_PPROF_TOKEN": "rotated"})
	if err != nil || rotated == rev {
		t.Errorf("a rotated token kept revision %q (err %v)", rotated, err)
	}
}

// An agent that does not start on the new credentials gets the old ones
// back, so the one still running reads what it was started with.
func TestAgentSecretsRollBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	mounts := []telemetry.SecretMount{{Secret: "w", Key: "token", StoreKey: "TOKEN", Path: "w/token"}}
	first, err := stageAgentSecrets(dir, mounts, map[string]string{"TOKEN": "old"}, "dev")
	if err != nil || first.swap() != nil {
		t.Fatalf("stage the first set: %v", err)
	}
	first.commit()
	next, err := stageAgentSecrets(dir, mounts, map[string]string{"TOKEN": "new"}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "w", "token")
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Fatalf("staging changed the live token to %q", b)
	}
	if err := next.swap(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("after swap the token is %q, want new", b)
	}
	next.rollback()
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Errorf("after rollback the token is %q, want old", b)
	}
}

// upLocalTelemetry: started with the plan when something is pulled, stopped
// when nothing is, and a note — not a start — when nothing collects locally.
func TestUpLocalTelemetryConverges(t *testing.T) {
	var started []hostinfra.TelemetryAgent
	stops := 0
	origStart, origStop := startTelemetryAgentFn, stopTelemetryAgentFn
	t.Cleanup(func() { startTelemetryAgentFn, stopTelemetryAgentFn = origStart, origStop })
	startTelemetryAgentFn = func(_ context.Context, projectDir string, a hostinfra.TelemetryAgent) (hostinfra.TelemetryAgentResult, error) {
		started = append(started, a)
		return hostinfra.TelemetryAgentResult{State: hostinfra.AgentStarted, PID: 42,
			ConfigPath: hostinfra.TelemetryAgentConfigPath(projectDir, a.Env), LogPath: hostinfra.LogPath(projectDir, a.Env, hostinfra.TelemetryAgentName)}, nil
	}
	stopTelemetryAgentFn = func(string, string) (bool, error) { stops++; return true, nil }

	dir := t.TempDir()
	engine := hostEngineTarget()
	var out bytes.Buffer
	upLocalTelemetry(context.Background(), &out, dir, "dev", telemetryEntities(engine), nil)
	if len(started) != 1 || stops != 0 {
		t.Fatalf("started %d, stopped %d; want one start\n%s", len(started), stops, out.String())
	}
	for _, want := range []string{"telemetry agent: started (pid 42", "collecting temporal: metrics 127.0.0.1:9233/metrics, tcp check :7233", ".forge/logs/dev/telemetry-agent.log"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// The scaffolded collector file predates nothing here: the project has
	// none, so the parse's missing include is named.
	if !strings.Contains(out.String(), "is not applied") {
		t.Errorf("a missing collector include was not reported:\n%s", out.String())
	}

	out.Reset()
	upLocalTelemetry(context.Background(), &out, dir, "dev", telemetryEntities(), nil)
	if stops != 1 || !strings.Contains(out.String(), "telemetry agent: stopped") {
		t.Errorf("nothing pulled: stops %d, output %q; want the agent stopped", stops, out.String())
	}

	out.Reset()
	e := telemetryEntities(engine)
	e.Workloads = nil // hosted dev telemetry: no local collector
	upLocalTelemetry(context.Background(), &out, dir, "dev", e, nil)
	if len(started) != 1 || !strings.Contains(out.String(), "not collecting temporal") {
		t.Errorf("no local collector: started %d, output %q; want a note and no start", len(started), out.String())
	}
}

// A collector file scaffolded by this forge includes the parse, so env up
// says it is applied rather than warning.
func TestScaffoldedCollectorIncludesTelemetry(t *testing.T) {
	dir := t.TempDir()
	tmpl, err := templates.ProjectTemplates().Render("otel-collector.yaml.tmpl", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, clickstack.CollectorConfig)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, tmpl, 0o644); err != nil {
		t.Fatal(err)
	}
	if !collectorIncludesTelemetry(dir) {
		t.Fatal("the scaffolded collector file does not include the declared parse")
	}
	origStart, origStop := startTelemetryAgentFn, stopTelemetryAgentFn
	t.Cleanup(func() { startTelemetryAgentFn, stopTelemetryAgentFn = origStart, origStop })
	stopTelemetryAgentFn = func(string, string) (bool, error) { return false, nil }
	logsOnly := hostWorkloadTarget()
	logsOnly.Profiles = nil
	var out bytes.Buffer
	upLocalTelemetry(context.Background(), &out, dir, "dev", telemetryEntities(logsOnly), nil)
	if !strings.Contains(out.String(), "log lines parsed as declared for billing-worker") || strings.Contains(out.String(), "not applied") {
		t.Errorf("output:\n%s", out.String())
	}
}
