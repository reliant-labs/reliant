package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// reliantDevFixture is reliant's deploy/kcl/dev (reliant#2412), minimized to
// what selects a ledger: a LOCAL env — host processes only, nothing hosted,
// no cluster — that declares forge's control plane for dev_telemetry, and
// keeps every secret in a file store.
func reliantDevFixture(endpoint string) string {
	return envFixture(endpoint, `"lifecycle":"local",`, `{"name":"reliant-api-server","kind":"service","runtime":{"type":"host"}}`)
}

// envFixture is a rendered env declaring a control plane at endpoint, with
// extra top-level fields and the given workloads.
func envFixture(endpoint, extra string, workloads ...string) string {
	return `{"output":{` + extra +
		`"control_plane":{"type":"control_plane","endpoint":"` + endpoint + `","token_env":"FORGE_TEST_CP_TOKEN"},` +
		`"observability":{"enabled":true,"dev_telemetry":"auto"},` +
		`"workloads":[` + strings.Join(workloads, ",") + `]}}`
}

// signedOut makes this test's process a developer who never signed in: no
// credential helper (a Reliant sandbox sets one), no token, an empty
// credentials file.
func signedOut(t *testing.T) {
	t.Helper()
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "")
	t.Setenv("FORGE_TEST_CP_TOKEN", "")
	t.Setenv("FORGE_HOME", t.TempDir())
}

// def7265e: declaring `control_plane` on a local dev env (so dev_telemetry can
// mint from it) made a signed-out `forge env up dev` fail in its deploy phase:
// "env "dev" keeps its release ledger on the control plane". For a local
// stack the ledger only pins a promoted release's digests, so a missing
// credential costs the pins, never the stack.
func TestEnvUpDeployPhaseRunsSignedOutOnALocalControlPlaneEnv(t *testing.T) {
	dir := newLedgerTestProject(t, "reliant")
	declareEnvDir(t, dir, "dev")
	signedOut(t)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, reliantDevFixture("https://api.reliantapi.com")))

	res, err := resolveDeployTags(context.Background(), dir, "dev", deployOptions{imageTag: "dev", purpose: renderToLaunch})
	if err != nil {
		t.Fatalf("env up's deploy phase failed signed out: %v", err)
	}
	if res.boundRelease != "" {
		t.Errorf("a signed-out local run pinned release %q from a ledger it could not read", res.boundRelease)
	}

	// Every other deploy still fails closed: it ships a release.
	if _, err := resolveDeployTags(context.Background(), dir, "dev", deployOptions{imageTag: "dev"}); err == nil ||
		!strings.Contains(err.Error(), "keeps its release ledger on the control plane") {
		t.Errorf("forge env deploy signed out must still refuse; got %v", err)
	}
}

// The same with a credential and a control plane that is down: the ledger's
// read fails on the transport, and the local stack starts anyway.
func TestEnvUpDeployPhaseRunsWithTheControlPlaneUnreachable(t *testing.T) {
	dir := newLedgerTestProject(t, "reliant")
	declareEnvDir(t, dir, "dev")
	signedOut(t)
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	// Port 1 on loopback: refused at once.
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, reliantDevFixture("http://127.0.0.1:1")))

	if _, err := resolveDeployTags(context.Background(), dir, "dev", deployOptions{imageTag: "dev", purpose: renderToLaunch}); err != nil {
		t.Fatalf("env up's deploy phase failed with the control plane down: %v", err)
	}
}

// renderToLaunch names why a render runs, not where the env runs. An env
// that places work off this machine — a cluster (a local k3d included), a
// hosted workload — or that declares no scratch lifecycle keeps failing
// closed under it, signed out or with the control plane down: there the
// ledger's pins decide which images ship.
func TestEnvUpDeployPhaseFailsClosedForAnEnvNotOnlyOnThisMachine(t *testing.T) {
	host := `{"name":"api","kind":"service","runtime":{"type":"host"}}`
	for _, tc := range []struct {
		name, extra string
		workloads   []string
	}{
		{"cluster workload", `"lifecycle":"local",`, []string{host, `{"name":"operator","kind":"service","runtime":{"type":"cluster"}}`}},
		{"hosted workload", `"lifecycle":"local",`, []string{host, `{"name":"web","kind":"service","runtime":{"type":"hosted"}}`}},
		{"no scratch lifecycle", ``, []string{host}},
	} {
		t.Run(tc.name+", signed out", func(t *testing.T) {
			dir := newLedgerTestProject(t, "reliant")
			declareEnvDir(t, dir, "dev")
			signedOut(t)
			t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, envFixture("https://api.reliantapi.com", tc.extra, tc.workloads...)))
			_, err := resolveDeployTags(context.Background(), dir, "dev", deployOptions{imageTag: "dev", purpose: renderToLaunch})
			if err == nil || !strings.Contains(err.Error(), "keeps its release ledger on the control plane") {
				t.Errorf("want the fail-closed ledger error, got %v", err)
			}
		})
		t.Run(tc.name+", control plane unreachable", func(t *testing.T) {
			dir := newLedgerTestProject(t, "reliant")
			declareEnvDir(t, dir, "dev")
			signedOut(t)
			t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
			t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, envFixture("http://127.0.0.1:1", tc.extra, tc.workloads...)))
			_, err := resolveDeployTags(context.Background(), dir, "dev", deployOptions{imageTag: "dev", purpose: renderToLaunch})
			if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
				t.Errorf("a transport failure on an env that is not only on this machine must fail closed with it; got %v", err)
			}
		})
	}
}

// Presence, the other half: a signed-out run reports no session and says so
// in one line, instead of failing or printing the credential resolver's
// multi-line fix list.
func TestEnvUpPresenceSignedOutIsOneNote(t *testing.T) {
	dir := newLedgerTestProject(t, "reliant")
	signedOut(t)
	e := &KCLEntities{
		Lifecycle:    lifecycleLocal,
		ControlPlane: &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://api.reliantapi.com", TokenEnv: "FORGE_TEST_CP_TOKEN"},
		Workloads:    []WorkloadEntity{{Name: "reliant-api-server", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}},
	}
	target, err := sessionTargetForEntities("dev", e, dir)
	var log bytes.Buffer
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if s := newUpSessionFor(context.Background(), dir, "dev", target, err, &log, at); s != nil {
		t.Fatal("a signed-out run has no control plane to report presence to")
	}
	if got := strings.TrimSpace(log.String()); strings.Count(got, "\n") != 0 || !strings.Contains(got, "not signed in") {
		t.Errorf("want one 'not signed in' note, got:\n%s", got)
	}
}

// The browser half of "export nothing": a container-free env whose telemetry
// stays local gets no collector address, and its frontend's browser SDK is
// switched off rather than POSTing every batch to a proxy with nowhere to
// forward. A declared endpoint still wins; an env with a ClickStack is not
// touched.
func TestContainerFreeLocalFrontendExportsNothing(t *testing.T) {
	fe := FrontendEntity{Name: "web", Type: "vite"}
	e := &KCLEntities{Observability: &ObservabilityEntity{Enabled: true, DevTelemetry: devTelemetryAuto}}
	env := withFrontendTelemetryEnv(nil, e, fe, "dev")
	if !hasEnv(env, "VITE_OTEL_ENDPOINT=off") || hasEnvKey(env, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Errorf("container-free local frontend env = %v; want VITE_OTEL_ENDPOINT=off and no collector", env)
	}
	if env := withFrontendTelemetryEnv([]string{"VITE_OTEL_ENDPOINT=/_otel"}, e, fe, "dev"); !hasEnv(env, "VITE_OTEL_ENDPOINT=/_otel") {
		t.Errorf("a declared endpoint must win: %v", env)
	}
	withStore := &KCLEntities{
		Observability: &ObservabilityEntity{Enabled: true},
		Workloads:     []WorkloadEntity{{Name: "clickstack", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "clickstack"}}}},
	}
	if env := withFrontendTelemetryEnv(nil, withStore, fe, "dev"); hasEnvKey(env, "VITE_OTEL_ENDPOINT") {
		t.Errorf("an env with a ClickStack keeps browser telemetry: %v", env)
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func hasEnvKey(env []string, key string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return true
		}
	}
	return false
}
