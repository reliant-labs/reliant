package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func optComposeWL(name string, optional bool) WorkloadEntity {
	return WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: name, Optional: optional}}}
}

func optHostWL(name string, requires ...string) WorkloadEntity {
	return WorkloadEntity{Name: name, Requires: requires, Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}
}

func workloadNames(e *KCLEntities) string {
	var n []string
	for _, w := range e.Workloads {
		n = append(n, w.Name)
	}
	return strings.Join(n, ",")
}

func withDockerDown(t *testing.T) {
	t.Helper()
	withDockerProbe(t, func(context.Context) (string, error) {
		return "failed to connect to the docker API at unix:///nonexistent", errors.New("exit status 1")
	})
}

func TestDegradeWithoutDocker_SkipsOptionalAndDependents(t *testing.T) {
	withDockerDown(t)
	e := &KCLEntities{Workloads: []WorkloadEntity{
		optHostWL("api"),
		optComposeWL("clickstack", true),
		optComposeWL("alertmanager", true),
		optHostWL("alerts", "clickstack", "alertmanager"),
		optHostWL("worker", "alerts"),
	}}
	var out bytes.Buffer
	if err := degradeWithoutDocker(context.Background(), e, "dev", nil, &out); err != nil {
		t.Fatal(err)
	}
	if got := workloadNames(e); got != "api" {
		t.Errorf("kept %q, want only api", got)
	}
	if got := strings.Join(e.OptionalSkipped, ","); got != "alertmanager,alerts,clickstack,worker" {
		t.Errorf("OptionalSkipped = %q", got)
	}
	if n := strings.Count(out.String(), "⚠"); n != 1 {
		t.Errorf("want exactly one warning, got %d:\n%s", n, out.String())
	}
	for _, want := range []string{"Docker is not available", "clickstack, alertmanager", "alerts, worker", "failed to connect", "start Docker and re-run `" + Name() + " env up dev`", "_observability = False"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("warning lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDegradeWithoutDocker_RequiredServiceFailsNamingDocker(t *testing.T) {
	withDockerDown(t)
	e := &KCLEntities{Workloads: []WorkloadEntity{optComposeWL("postgres", false), optComposeWL("clickstack", true)}}
	err := degradeWithoutDocker(context.Background(), e, "dev", nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a required compose service must fail without Docker")
	}
	for _, want := range []string{"postgres", "Docker", "optional"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "clickstack") {
		t.Errorf("the error should name only the services that need Docker: %v", err)
	}
	if len(e.OptionalSkipped) != 0 || workloadNames(e) != "postgres,clickstack" {
		t.Errorf("a failed run must not mutate the env: %v / %s", e.OptionalSkipped, workloadNames(e))
	}
}

func TestDegradeWithoutDocker_NoopWhenDockerAnswers(t *testing.T) {
	withReachableDocker(t)
	e := &KCLEntities{Workloads: []WorkloadEntity{optComposeWL("clickstack", true), optHostWL("alerts", "clickstack")}}
	var out bytes.Buffer
	if err := degradeWithoutDocker(context.Background(), e, "dev", nil, &out); err != nil {
		t.Fatal(err)
	}
	if workloadNames(e) != "clickstack,alerts" || out.Len() != 0 || e.OptionalSkipped != nil {
		t.Errorf("a reachable daemon must change nothing: %s %q", workloadNames(e), out.String())
	}
}

func TestDegradeWithoutDocker_NeverProbesWithoutComposeWorkloads(t *testing.T) {
	withDockerProbe(t, func(context.Context) (string, error) {
		t.Error("docker was probed though nothing in scope runs in compose")
		return "", errors.New("boom")
	})
	e := &KCLEntities{Workloads: []WorkloadEntity{optHostWL("api")}}
	if err := degradeWithoutDocker(context.Background(), e, "dev", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestDegradeWithoutDocker_TargetsScopeWhatNeedsDocker(t *testing.T) {
	withDockerDown(t)
	// `--target api` does not bring postgres up, so its being required is moot.
	e := &KCLEntities{Workloads: []WorkloadEntity{optHostWL("api"), optComposeWL("postgres", false)}}
	if err := degradeWithoutDocker(context.Background(), e, "dev", []string{"api"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("target api needs no Docker: %v", err)
	}
}

func TestDegradeWithoutDocker_CollectorSkippedSilencesTelemetry(t *testing.T) {
	withDockerDown(t)
	e := &KCLEntities{
		Observability: &ObservabilityEntity{Enabled: true, LocalEndpoint: "http://127.0.0.1:4318"},
		Workloads:     []WorkloadEntity{optHostWL("api"), optComposeWL("clickstack", true)},
	}
	if err := degradeWithoutDocker(context.Background(), e, "dev", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if e.Observability.Enabled {
		t.Error("observability must be off: its collector is not coming up")
	}
	if got := e.Workloads[0].Runtime.Host.LaunchEnv[otelSDKDisabledEnv]; got != "true" {
		t.Errorf("host process must not export to the absent collector: %s=%q", otelSDKDisabledEnv, got)
	}
}

func TestWithoutWorkloads(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{optHostWL("a"), optHostWL("b"), optHostWL("c")}}
	got := withoutWorkloads(e, []string{"b"})
	if workloadNames(got) != "a,c" || workloadNames(e) != "a,b,c" {
		t.Errorf("got %s, original %s", workloadNames(got), workloadNames(e))
	}
	if withoutWorkloads(e, nil) != e {
		t.Error("nothing to drop must return the same env")
	}
}
