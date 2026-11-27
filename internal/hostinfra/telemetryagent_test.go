package hostinfra

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestTelemetryAgentPathsLiveUnderTheEnvsObservabilityDirectory(t *testing.T) {
	project := filepath.Join("proj", "x")
	dir := filepath.Join(project, ".forge", "observability", "dev")
	for name, tc := range map[string]struct{ got, want string }{
		"dir":         {TelemetryAgentDir(project, "dev"), dir},
		"secrets":     {TelemetryAgentSecretsDir(project, "dev"), filepath.Join(dir, "secrets")},
		"config":      {TelemetryAgentConfigPath(project, "dev"), filepath.Join(dir, "telemetry-agent.yaml")},
		"pid":         {telemetryAgentPIDFile(project, "dev"), filepath.Join(dir, "telemetry-agent.pid")},
		"credentials": {telemetryAgentCredentialsFile(project, "dev"), filepath.Join(dir, "telemetry-agent.credentials")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s path = %q, want %q", name, tc.got, tc.want)
		}
	}
}

func TestAgentArgsNameTheConfigAndTheFeatureGates(t *testing.T) {
	if got := strings.Join(agentArgs("c.yaml", nil), " "); got != "--config=c.yaml" {
		t.Errorf("args without gates = %q", got)
	}
	got := strings.Join(agentArgs("c.yaml", []string{"service.profilesSupport", "other.gate"}), " ")
	if got != "--config=c.yaml --feature-gates=service.profilesSupport,other.gate" {
		t.Errorf("args with gates = %q", got)
	}
}

func TestReadRevisionIsEmptyWhenNoneWasRecorded(t *testing.T) {
	if got := readRevision(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("readRevision of a missing file = %q", got)
	}
}

func TestFreeLoopbackPortIsUsableAndDistinctFromAHeldOne(t *testing.T) {
	port, err := freeLoopbackPort()
	if err != nil || port <= 0 {
		t.Fatalf("freeLoopbackPort = %d, %v", port, err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("the port was taken before the test could bind it: %v", err)
	}
	_ = ln.Close()
}

func renderConfig(t *testing.T, a TelemetryAgent, port int) map[string]any {
	t.Helper()
	doc, err := RenderTelemetryAgentConfig(a, port)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(doc, &out); err != nil {
		t.Fatalf("the rendered configuration is not YAML: %v\n%s", err, doc)
	}
	return out
}

func TestRenderTelemetryAgentConfigAddsTheSupervisorsOwnSettings(t *testing.T) {
	caller := map[string]any{
		"receivers": map[string]any{"otlp": map[string]any{}},
		"service": map[string]any{
			"extensions": []any{"pprof"},
			"pipelines":  map[string]any{"metrics": map[string]any{"receivers": []any{"otlp"}}},
		},
		"extensions": map[string]any{"pprof": map[string]any{}},
	}
	a := TelemetryAgent{Env: "dev", Config: caller, FeatureGates: []string{"service.profilesSupport"}}
	doc, err := RenderTelemetryAgentConfig(a, 4321)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "# args: --config=<this file> --feature-gates=service.profilesSupport\n") {
		t.Errorf("the header must name the arguments so a gate change changes the file:\n%s", doc)
	}
	cfg := renderConfig(t, a, 4321)
	ext := cfg["extensions"].(map[string]any)
	health := ext[healthExtension].(map[string]any)
	if health["endpoint"] != "127.0.0.1:4321" {
		t.Errorf("health endpoint = %v", health["endpoint"])
	}
	if _, ok := ext["pprof"]; !ok {
		t.Error("the caller's own extension was dropped")
	}
	svc := cfg["service"].(map[string]any)
	exts := svc["extensions"].([]any)
	if len(exts) != 2 || exts[0] != "pprof" || exts[1] != healthExtension {
		t.Errorf("service.extensions = %v, want the caller's then the health extension", exts)
	}
	tel := svc["telemetry"].(map[string]any)["metrics"].(map[string]any)
	if tel["level"] != "none" {
		t.Errorf("the collector's own metrics listener must be off: %v", tel)
	}
	if _, ok := svc["pipelines"]; !ok {
		t.Error("the caller's pipelines were dropped")
	}

	// The caller's map is never written.
	if _, touched := caller["extensions"].(map[string]any)[healthExtension]; touched {
		t.Error("RenderTelemetryAgentConfig mutated the caller's extensions")
	}
	if _, touched := caller["service"].(map[string]any)["telemetry"]; touched {
		t.Error("RenderTelemetryAgentConfig mutated the caller's service section")
	}
}

func TestRenderTelemetryAgentConfigAcceptsStringExtensionsAndNone(t *testing.T) {
	cfg := renderConfig(t, TelemetryAgent{Config: map[string]any{"service": map[string]any{"extensions": []string{"a", "b"}}}}, 1)
	exts := cfg["service"].(map[string]any)["extensions"].([]any)
	if len(exts) != 3 || exts[2] != healthExtension {
		t.Errorf("extensions = %v", exts)
	}
	bare := renderConfig(t, TelemetryAgent{}, 1)
	if exts := bare["service"].(map[string]any)["extensions"].([]any); len(exts) != 1 || exts[0] != healthExtension {
		t.Errorf("an empty configuration still gets the health extension: %v", exts)
	}
}

func TestRenderTelemetryAgentConfigRefusesWhatTheSupervisorOwns(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  map[string]any
		want string
	}{
		"health extension":  {map[string]any{"extensions": map[string]any{healthExtension: map[string]any{}}}, healthExtension},
		"service.telemetry": {map[string]any{"service": map[string]any{"telemetry": map[string]any{}}}, "service.telemetry"},
		"unmarshalable":     {map[string]any{"bad": make(chan int)}, "marshal the agent configuration"},
	} {
		_, err := RenderTelemetryAgentConfig(TelemetryAgent{Config: tc.cfg}, 1)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

// waitAgentHealthy returns promptly when the process is gone or the context
// is cancelled, and gives up at its own deadline otherwise.
func TestWaitAgentHealthyGivesUpPromptly(t *testing.T) {
	prev := agentReadyTimeout
	agentReadyTimeout = 400 * time.Millisecond
	t.Cleanup(func() { agentReadyTimeout = prev })
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}

	err = waitAgentHealthy(context.Background(), selfPID(), port)
	if err == nil || !strings.Contains(err.Error(), "did not report healthy") {
		t.Errorf("an agent that never answers: err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitAgentHealthy(ctx, selfPID(), port); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: err = %v, want context.Canceled", err)
	}

	if err := waitAgentHealthy(context.Background(), 0, port); err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("a dead process: err = %v", err)
	}
}

func selfPID() int { return os.Getpid() }
