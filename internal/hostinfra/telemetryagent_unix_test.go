//go:build !windows

package hostinfra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func fakeAgent() TelemetryAgent {
	return TelemetryAgent{
		Env:    "dev",
		Config: map[string]any{"receivers": map[string]any{"otlp": map[string]any{}}},
	}
}

func setupAgent(t *testing.T) (project string) {
	t.Helper()
	testkit.Slow(t, "launches a real child process and polls its health endpoint")
	isolateCache(t)
	installFake(t, otelcolRelease.binaryPath(), "otelcol", otelcolRelease.versionNeedle)
	project = t.TempDir()
	reapPIDFile(t, telemetryAgentPIDFile(project, "dev"))
	t.Cleanup(func() { _, _ = StopTelemetryAgent(project, "dev") })
	return project
}

// The agent's whole lifecycle: started, adopted unchanged, restarted when its
// configuration or credentials change, refused when the new configuration
// does not load, and stopped.
func TestTelemetryAgentLifecycleWithAStandInCollector(t *testing.T) {
	project := setupAgent(t)
	ctx := context.Background()

	if _, running := TelemetryAgentRunning(project, "dev"); running {
		t.Fatal("an agent is reported running before it was started")
	}
	first, err := StartTelemetryAgent(ctx, project, fakeAgent())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if first.State != AgentStarted || first.PID == 0 || first.ConfigPath != TelemetryAgentConfigPath(project, "dev") {
		t.Fatalf("first start = %+v", first)
	}
	if first.LogPath != LogPath(project, "dev", TelemetryAgentName) {
		t.Errorf("log path = %q", first.LogPath)
	}
	if pid, running := TelemetryAgentRunning(project, "dev"); !running || pid != first.PID {
		t.Errorf("TelemetryAgentRunning = %d, %v; want pid %d", pid, running, first.PID)
	}
	cfg, err := os.ReadFile(first.ConfigPath)
	if err != nil || !strings.Contains(string(cfg), healthExtension) {
		t.Errorf("the written configuration = %q, %v", cfg, err)
	}

	// Same configuration and credentials: adopted, not restarted.
	same, err := StartTelemetryAgent(ctx, project, fakeAgent())
	if err != nil || same.State != AgentUnchanged || same.PID != first.PID {
		t.Fatalf("second start = %+v, %v; want unchanged on pid %d", same, err, first.PID)
	}

	// A rotated credential restarts it even though the configuration only
	// names the file.
	rotated := fakeAgent()
	rotated.CredentialsRevision = "rev-2"
	re, err := StartTelemetryAgent(ctx, project, rotated)
	if err != nil || re.State != AgentRestarted || re.PID == first.PID {
		t.Fatalf("a rotated credential = %+v, %v; want a restart on a new pid", re, err)
	}
	if b, _ := os.ReadFile(telemetryAgentCredentialsFile(project, "dev")); string(b) != "rev-2" {
		t.Errorf("recorded credentials revision = %q", b)
	}

	// A configuration that does not load never replaces the one that runs.
	t.Setenv(fakeValidateEnv, "fail")
	changed := rotated
	changed.FeatureGates = []string{"some.gate"}
	_, err = StartTelemetryAgent(ctx, project, changed)
	if err == nil || !strings.Contains(err.Error(), "does not load in otelcol-contrib") || !strings.Contains(err.Error(), "no receivers configured") {
		t.Fatalf("a bad configuration: %v", err)
	}
	if pid, running := TelemetryAgentRunning(project, "dev"); !running || pid != re.PID {
		t.Errorf("the running agent was disturbed by a rejected configuration: %d, %v", pid, running)
	}

	stopped, err := StopTelemetryAgent(project, "dev")
	if err != nil || !stopped {
		t.Fatalf("stop = %v, %v", stopped, err)
	}
	if _, running := TelemetryAgentRunning(project, "dev"); running {
		t.Error("still running after stop")
	}
	if stopped, err := StopTelemetryAgent(project, "dev"); stopped || err != nil {
		t.Errorf("a second stop = %v, %v", stopped, err)
	}
}

func TestStartTelemetryAgentRefusesConfigurationsTheSupervisorOwns(t *testing.T) {
	project := setupAgent(t)
	a := fakeAgent()
	a.Config["service"] = map[string]any{"telemetry": map[string]any{}}
	_, err := StartTelemetryAgent(context.Background(), project, a)
	if err == nil || !strings.Contains(err.Error(), "service.telemetry") {
		t.Errorf("err = %v", err)
	}
}

func TestStartTelemetryAgentReportsACollectorThatDies(t *testing.T) {
	project := setupAgent(t)
	t.Setenv(fakeExitEnv, "1")
	prev := agentReadyTimeout
	agentReadyTimeout = 5 * time.Second
	t.Cleanup(func() { agentReadyTimeout = prev })
	_, err := StartTelemetryAgent(context.Background(), project, fakeAgent())
	if err == nil || !strings.Contains(err.Error(), "exited during startup") || !strings.Contains(err.Error(), "logs: ") {
		t.Errorf("err = %v", err)
	}
}

func TestStartTelemetryAgentWithoutAUsableCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if otelcolRelease.binaryPath() != "" {
		t.Skip("this OS has a cache directory without HOME")
	}
	_, err := StartTelemetryAgent(context.Background(), t.TempDir(), fakeAgent())
	if err == nil || !strings.Contains(err.Error(), "telemetry agent:") {
		t.Errorf("err = %v", err)
	}
	if _, err := EnsureOtelcolContrib(context.Background()); err == nil {
		t.Error("EnsureOtelcolContrib must fail without a cache directory")
	}
}

func TestEnsureOtelcolContribReturnsTheCachedPinnedBinary(t *testing.T) {
	isolateCache(t)
	installFake(t, otelcolRelease.binaryPath(), "otelcol", otelcolRelease.versionNeedle)
	got, err := EnsureOtelcolContrib(context.Background())
	if err != nil || got != otelcolRelease.binaryPath() {
		t.Errorf("EnsureOtelcolContrib = %q, %v", got, err)
	}
}

func TestStopAgentLeavesAnUnverifiableOrForeignProcessAlone(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "agent.pid")
	if err := writePIDFile(pidFile, os.Getpid(), 1); err != nil {
		t.Fatal(err)
	}
	// This process is alive but is not the collector binary: foreign.
	if stopped, err := stopAgent(pidFile, filepath.Join(dir, "otelcol-contrib")); stopped || err != nil {
		t.Errorf("stopAgent of a foreign pid = %v, %v", stopped, err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("the pidfile naming a foreign process should be removed")
	}
	if pid, port, ok := runningAgent(filepath.Join(dir, "none.pid"), "x"); ok || pid != 0 || port != 0 {
		t.Errorf("runningAgent with no pidfile = %d %d %v", pid, port, ok)
	}
	// An unverifiable identity (pidfile gone before the check) is never signalled.
	if err := writePIDFile(pidFile, os.Getpid(), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := runningAgent(pidFile, filepath.Join(dir, "otelcol-contrib")); ok {
		t.Error("a process that is not the collector was reported as the agent")
	}
}
