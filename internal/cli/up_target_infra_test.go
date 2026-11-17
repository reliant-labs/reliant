package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/clickstack"
)

// `forge env up dev --target api --target web --no-deploy` pulled and started
// ClickStack: the infra pre-warm converged every infrastructure service the
// env declares, whatever was targeted. The env declares observability
// optional, so a targeted run leaves it out unless a target names or
// requires it, and the plan says what it starts and why.

func withComposeRunning(t *testing.T, running ...string) {
	t.Helper()
	prev := composeServiceRunning
	composeServiceRunning = func(_ context.Context, _, _ string, w WorkloadEntity) bool {
		for _, r := range running {
			if w.Name == r {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { composeServiceRunning = prev })
}

func targetInfraEnv() *KCLEntities {
	collector := optComposeWL("clickstack", true)
	collector.Runtime.Compose.Service = clickstack.Service
	return &KCLEntities{
		Infra:         []HostInfraEntity{{Name: "postgres", Engine: "postgres"}},
		Observability: &ObservabilityEntity{Enabled: true, LocalEndpoint: "http://127.0.0.1:4318"},
		Workloads: []WorkloadEntity{
			optHostWL("api"),
			optHostWL("alerts", "clickstack", "alertmanager"),
			optComposeWL("zitadel", false),
			collector,
			optComposeWL("otel-profiles", true),
			optComposeWL("alertmanager", true),
		},
	}
}

func TestScopeOptionalInfra_TargetsStartOnlyTheInfraTheyNeed(t *testing.T) {
	withComposeRunning(t)
	e := targetInfraEnv()
	var out bytes.Buffer
	scopeOptionalInfra(context.Background(), e, t.TempDir(), "dev", []string{"api", "web"}, &out)

	if got := workloadNames(e); got != "api,alerts,zitadel" {
		t.Errorf("kept %q: the optional observability services must not start for api+web", got)
	}
	if got := strings.Join(e.OptionalSkipped, ","); got != "alertmanager,clickstack,otel-profiles" {
		t.Errorf("OptionalSkipped = %q; the deploy phase must drop the same services", got)
	}
	if e.Observability.Enabled {
		t.Error("the collector is not coming up, so local telemetry must be off for this run")
	}
	if e.Workloads[0].HostEnv()[otelSDKDisabledEnv] != "true" {
		t.Errorf("api would export to a collector that is not running: %v", e.Workloads[0].HostEnv())
	}
	for _, want := range []string{
		"shared infra for --target api, web",
		"starting: postgres (host process), zitadel (compose, required by the env)",
		"not starting: clickstack, otel-profiles, alertmanager",
		"add --target <name>",
		"local telemetry",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
}

// Naming an optional service, or a target that requires one, brings it back
// — with everything that one requires in turn.
func TestScopeOptionalInfra_ATargetCanSelectOrRequireOptionalInfra(t *testing.T) {
	withComposeRunning(t)
	e := targetInfraEnv()
	var out bytes.Buffer
	scopeOptionalInfra(context.Background(), e, t.TempDir(), "dev", []string{"alerts"}, &out)

	if got := strings.Join(e.OptionalSkipped, ","); got != "otel-profiles" {
		t.Errorf("OptionalSkipped = %q, want only otel-profiles (alerts requires clickstack and alertmanager)", got)
	}
	if !e.Observability.Enabled {
		t.Error("the collector is required by a target and starts; telemetry must stay on")
	}
	if !strings.Contains(out.String(), "clickstack (compose, required by a target)") {
		t.Errorf("plan does not say why clickstack starts:\n%s", out.String())
	}
}

// An optional service an earlier, untargeted run started is left alone: not
// pulled or recreated, and the targets keep exporting to it.
func TestScopeOptionalInfra_LeavesRunningOptionalInfraAlone(t *testing.T) {
	withComposeRunning(t, "clickstack")
	e := targetInfraEnv()
	var out bytes.Buffer
	scopeOptionalInfra(context.Background(), e, t.TempDir(), "dev", []string{"api"}, &out)

	if strings.Contains(workloadNames(e), "clickstack") {
		t.Error("a running optional service must not be re-converged (pulled, recreated) by a targeted run")
	}
	if !e.Observability.Enabled || e.Workloads[0].HostEnv()[otelSDKDisabledEnv] != "" {
		t.Error("the collector is running: the target must keep exporting to it")
	}
	if !strings.Contains(out.String(), "left running: clickstack") {
		t.Errorf("plan does not report the running collector:\n%s", out.String())
	}
}

func TestScopeOptionalInfra_NoTargetsRunsTheWholeEnv(t *testing.T) {
	withComposeRunning(t)
	e := targetInfraEnv()
	var out bytes.Buffer
	scopeOptionalInfra(context.Background(), e, t.TempDir(), "dev", nil, &out)
	if len(e.OptionalSkipped) != 0 || len(e.Workloads) != 6 || out.Len() != 0 {
		t.Errorf("an untargeted run changed: skipped=%v workloads=%s out=%q", e.OptionalSkipped, workloadNames(e), out.String())
	}
}
