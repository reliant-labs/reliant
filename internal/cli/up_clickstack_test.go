package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// clickstackEntities is a dev env as the typed declaration renders it: the
// clickstack compose service plus `output.observability` naming the loopback
// endpoint of the port that service publishes.
func clickstackEntities(env map[string]string) *KCLEntities {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost}},
		{Name: "clickstack", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "clickstack", Env: env}}},
	}}
	if port := env["CLICKSTACK_OTLP_HTTP_PORT"]; port != "" && port != "0" {
		e.Observability = &ObservabilityEntity{Enabled: true, LocalEndpoint: "http://127.0.0.1:" + port}
	}
	return e
}

// The endpoint a frontend dev server's /_otel proxy aims at is read from the
// env's typed declaration, not re-derived from a compose port.
func TestFrontendOTLPEnvComesFromTheDeclaration(t *testing.T) {
	got := frontendOTLPEnv(clickstackEntities(map[string]string{"CLICKSTACK_OTLP_HTTP_PORT": "4419"}))
	want := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4419"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frontend OTLP env = %v, want %v", got, want)
	}
}

// No declaration, a disabled one, or one with no local collector means no
// endpoint is invented: processes run exactly as before.
func TestFrontendOTLPEnvIsNilWithoutALocalCollector(t *testing.T) {
	for name, e := range map[string]*KCLEntities{
		"nil entities":   nil,
		"no declaration": {Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost}}}},
		"disabled":       {Observability: &ObservabilityEntity{Enabled: false, LocalEndpoint: "http://127.0.0.1:4318"}},
		"no local port":  {Observability: &ObservabilityEntity{Enabled: true}},
	} {
		if got := frontendOTLPEnv(e); got != nil {
			t.Errorf("%s: got %v, want nil", name, got)
		}
	}
}

func TestWithDefaultEnvNeverOverridesWhatTheUserSet(t *testing.T) {
	defaults := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
	}
	t.Run("unset gets the default", func(t *testing.T) {
		got := strings.Join(withDefaultEnv([]string{"PATH=/bin"}, defaults), "\n")
		for _, want := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318", "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf", "PATH=/bin"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in\n%s", want, got)
			}
		}
	})
	t.Run("a user value wins", func(t *testing.T) {
		got := withDefaultEnv([]string{"OTEL_EXPORTER_OTLP_ENDPOINT=https://collector.example:4318"}, defaults)
		n := 0
		for _, kv := range got {
			if strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_ENDPOINT=") {
				n++
				if kv != "OTEL_EXPORTER_OTLP_ENDPOINT=https://collector.example:4318" {
					t.Errorf("endpoint was overridden: %s", kv)
				}
			}
		}
		if n != 1 {
			t.Errorf("endpoint appears %d times, want 1", n)
		}
	})
	t.Run("an empty value is an absence, not a decision", func(t *testing.T) {
		// The project-config projection emits one entry per field, so an unset
		// otlp_endpoint arrives as an empty string and must not shadow the default.
		got := strings.Join(withDefaultEnv([]string{"OTEL_EXPORTER_OTLP_ENDPOINT="}, defaults), "\n")
		if !strings.Contains(got, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318") || strings.Contains(got, "OTEL_EXPORTER_OTLP_ENDPOINT=\n") {
			t.Errorf("an empty endpoint shadowed the default:\n%s", got)
		}
	})
	t.Run("nil defaults change nothing", func(t *testing.T) {
		in := []string{"A=1"}
		if got := withDefaultEnv(in, nil); !reflect.DeepEqual(got, in) {
			t.Errorf("got %v", got)
		}
	})
}

// The endpoint and the protocol that speaks to it are a pair. A user who set one
// has made the transport decision; filling in the other half would pair their
// gRPC :4317 with our http/protobuf and export would fail with nothing on screen.
func TestWithDefaultEnvIsAllOrNothing(t *testing.T) {
	defaults := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
	}
	for name, user := range map[string][]string{
		"endpoint only (a gRPC collector)": {"OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317"},
		"protocol only":                    {"OTEL_EXPORTER_OTLP_PROTOCOL=grpc"},
		"both":                             {"OTEL_EXPORTER_OTLP_ENDPOINT=http://c:4317", "OTEL_EXPORTER_OTLP_PROTOCOL=grpc"},
	} {
		t.Run(name, func(t *testing.T) {
			in := append([]string{"PATH=/bin"}, user...)
			if got := withDefaultEnv(in, defaults); !reflect.DeepEqual(got, in) {
				t.Errorf("a default was injected beside the user's setting:\n got %v\nwant %v", got, in)
			}
		})
	}
	t.Run("empty values are unset, so the pair is injected whole", func(t *testing.T) {
		got := strings.Join(withDefaultEnv([]string{"OTEL_EXPORTER_OTLP_ENDPOINT=", "OTEL_EXPORTER_OTLP_PROTOCOL="}, defaults), "\n")
		for _, want := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318", "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in\n%s", want, got)
			}
		}
	})
}

// A developer's own exported transport beats the pair the declaration
// derives for host processes, whole: per-signal settings count too, headers
// alone do not, and an endpoint the AUTHOR declared in KCL is never dropped.
func TestShellTransportWinsOverTheDerivedPair(t *testing.T) {
	derived := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":           "api",
	}
	for _, kv := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://mine:4317",
		"OTEL_EXPORTER_OTLP_PROTOCOL=grpc",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://t:4317",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL=grpc",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://l:4318",
	} {
		got := withoutDerivedOTLPIfShellSet(derived, []string{"PATH=/bin", kv})
		if _, ok := got["OTEL_EXPORTER_OTLP_ENDPOINT"]; ok {
			t.Errorf("%s: the derived endpoint was kept beside it: %v", kv, got)
		}
		if _, ok := got["OTEL_EXPORTER_OTLP_PROTOCOL"]; ok {
			t.Errorf("%s: the derived protocol was kept beside it: %v", kv, got)
		}
		if got["OTEL_SERVICE_NAME"] != "api" {
			t.Errorf("%s: unrelated derived env was dropped: %v", kv, got)
		}
	}
	if got := withoutDerivedOTLPIfShellSet(derived, []string{"OTEL_EXPORTER_OTLP_HEADERS=authorization=x"}); !reflect.DeepEqual(got, derived) {
		t.Errorf("headers alone must not suppress the derived pair: %v", got)
	}
	authored := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://collector.example:4318", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf"}
	if got := withoutDerivedOTLPIfShellSet(authored, []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://mine:4317"}); !reflect.DeepEqual(got, authored) {
		t.Errorf("an author-declared endpoint is the declared value: %v", got)
	}
}

// The declaration derives a profiles endpoint aimed at the local profiles
// collector. A developer who pointed their telemetry elsewhere gets their
// profiles there too (the SDK appends the profiles path to their base), never
// sent to a collector they did not pick; their own profiles endpoint wins.
func TestDerivedProfilesEndpointNeverRoutesAroundTheDevelopersCollector(t *testing.T) {
	const pep = "OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT"
	derived := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		pep:                           "http://127.0.0.1:4328/v1development/profiles",
	}
	if got := withoutDerivedOTLPIfShellSet(derived, []string{"PATH=/bin"}); !reflect.DeepEqual(got, derived) {
		t.Errorf("a clean shell keeps every derived key: %v", got)
	}
	for _, kv := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://elsewhere:4318",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://elsewhere:4318/v1/traces",
		"OTEL_EXPORTER_OTLP_PROTOCOL=grpc",
	} {
		if _, ok := withoutDerivedOTLPIfShellSet(derived, []string{kv})[pep]; ok {
			t.Errorf("%s: forge kept profiles on its own collector over the developer's choice", kv)
		}
	}
	got := withoutDerivedOTLPIfShellSet(derived, []string{pep + "=http://mine:4318/x"})
	if _, ok := got[pep]; ok {
		t.Errorf("an exported profiles endpoint must displace the derived one: %v", got)
	}
	if got["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:4318" {
		t.Errorf("a profiles endpoint alone must leave the derived traces pair: %v", got)
	}
}

// A frontend dev server's /_otel proxy must aim at THIS project's collector,
// the port compose actually published, and the browser must learn the env name.
func TestFrontendDevServerGetsTheCollectorAndTheEnvName(t *testing.T) {
	e := clickstackEntities(map[string]string{"CLICKSTACK_OTLP_HTTP_PORT": "4419"})
	for ftype, envVar := range map[string]string{
		"nextjs": "NEXT_PUBLIC_ENVIRONMENT",
		"vite":   "VITE_ENVIRONMENT",
	} {
		t.Run(ftype, func(t *testing.T) {
			fe := FrontendEntity{Name: "web", Type: ftype}
			got := map[string]string{}
			for _, kv := range withFrontendTelemetryEnv([]string{"PATH=/bin"}, e, fe, "dev") {
				if i := strings.IndexByte(kv, '='); i > 0 {
					got[kv[:i]] = kv[i+1:]
				}
			}
			if got["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:4419" {
				t.Errorf("endpoint = %q, want the published port 4419", got["OTEL_EXPORTER_OTLP_ENDPOINT"])
			}
			if got[envVar] != "dev" {
				t.Errorf("%s = %q, want dev", envVar, got[envVar])
			}
			if _, ok := got["OTEL_EXPORTER_OTLP_PROTOCOL"]; ok {
				t.Error("a frontend dev server is handed the base URL only, not a protocol")
			}
		})
	}

	t.Run("the user's endpoint and an explicit environment win", func(t *testing.T) {
		fe := FrontendEntity{Name: "web", Type: "nextjs"}
		in := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://mine:4318", "NEXT_PUBLIC_ENVIRONMENT=staging"}
		if got := withFrontendTelemetryEnv(in, e, fe, "dev"); !reflect.DeepEqual(got, in) {
			t.Errorf("got %v, want the user's values untouched", got)
		}
	})
	t.Run("no clickstack: only the env name", func(t *testing.T) {
		fe := FrontendEntity{Name: "web", Type: "nextjs"}
		got := strings.Join(withFrontendTelemetryEnv(nil, &KCLEntities{}, fe, "dev"), "\n")
		if strings.Contains(got, "OTEL_EXPORTER_OTLP_ENDPOINT") || !strings.Contains(got, "NEXT_PUBLIC_ENVIRONMENT=dev") {
			t.Errorf("got %q", got)
		}
	})
}

// The command upFrontends actually runs carries both values.
func TestFrontendDevCmdCarriesTelemetryEnv(t *testing.T) {
	fl := frontendLaunch{
		entities: clickstackEntities(map[string]string{"CLICKSTACK_OTLP_HTTP_PORT": "4419"}),
		env:      "dev",
	}
	fe := FrontendEntity{Name: "web", Type: "nextjs", EnvFile: "/does/not/exist"}
	got := strings.Join(buildFrontendDevCmd(context.Background(), fl, fe, nil).Env, "\n")
	for _, want := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4419", "NEXT_PUBLIC_ENVIRONMENT=dev"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

// Docker creates a missing bind-mount source as root, which would leave forge
// unable to write the logs the collector tails.
func TestPrepareLocalCollectorOnlyWhenACollectorRuns(t *testing.T) {
	dir := t.TempDir()
	if err := prepareLocalCollector(dir, &KCLEntities{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".forge", "logs")); err == nil {
		t.Fatal("created .forge/logs although no local collector is declared")
	}
	if err := prepareLocalCollector(dir, clickstackEntities(nil)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".forge", "logs")); err != nil || !fi.IsDir() {
		t.Fatalf(".forge/logs not created: %v", err)
	}
}

// With a destination the forwarder replaces ClickStack: its pipeline file is
// rendered from the declaration, and the destination's headers reach it from
// the secret store through one fixed variable, never through the file.
func TestForwarderIsRenderedFromTheDeclaration(t *testing.T) {
	dir := t.TempDir()
	o := &ObservabilityEntity{Enabled: true, LocalEndpoint: "http://127.0.0.1:4318",
		Destination: &ObservabilityDestEntity{Endpoint: "https://otlp.example.com", Protocol: "http/protobuf", HeadersSecret: &struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		}{Name: "otlp", Key: "OTLP_HEADERS"}},
		Signals: ObservabilitySignalsEntity{Traces: true, Metrics: true, Logs: true}}
	e := &KCLEntities{Observability: o, Workloads: []WorkloadEntity{
		{Name: "otel-forwarder", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "otel-forwarder"}}},
	}}
	if err := prepareLocalCollector(dir, e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".forge", "observability", "forwarder.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	if !strings.Contains(cfg, "https://otlp.example.com") || !strings.Contains(cfg, "${env:FORGE_OTLP_HEADERS}") {
		t.Errorf("forwarder config does not name the destination and read its headers from the env:\n%s", cfg)
	}
	if strings.Contains(cfg, "secret-value") {
		t.Error("a secret value reached the config file")
	}
	got := forwarderHeadersEnv(e, map[string]string{"OTLP_HEADERS": `{"authorization":"secret-value"}`, "UNRELATED": "x"})
	if !reflect.DeepEqual(got, map[string]string{"FORGE_OTLP_HEADERS": `{"authorization":"secret-value"}`}) {
		t.Errorf("forwarder env = %v: only the declared key, under the fixed variable", got)
	}
	if refs := secretRefsForLaunch(e); len(refs) != 1 || refs[0].EnvName != "OTLP_HEADERS" {
		t.Errorf("forge env up must require the declared headers key before starting: %v", refs)
	}
}

// A destination on the developer's machine must be reachable FROM the
// forwarder's container, where "localhost" is the container itself. (Found by
// booting the forwarder against a receiver on 127.0.0.1: every export was
// "connection refused" while the declaration looked right.)
func TestHostReachableEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:14318":          "http://host.docker.internal:14318",
		"http://localhost:4318/v1/x":      "http://host.docker.internal:4318/v1/x",
		"https://localhost":               "https://host.docker.internal",
		"http://0.0.0.0:9":                "http://host.docker.internal:9",
		"https://otlp.example.com":        "https://otlp.example.com",
		"https://otlp.example.com:443":    "https://otlp.example.com:443",
		"http://10.1.2.3:4318":            "http://10.1.2.3:4318",
		"http://localhost.example.com:80": "http://localhost.example.com:80",
	} {
		if got := hostReachableEndpoint(in); got != want {
			t.Errorf("hostReachableEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
