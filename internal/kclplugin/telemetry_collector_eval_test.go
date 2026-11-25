package kclplugin_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/telemetry/collector"
)

// renderKCL evaluates main as a one-file KCL package and returns its output.
func renderKCL(t *testing.T, main string) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	mod := "[package]\nname = \"telcollector\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	for name, body := range map[string]string{"kcl.mod": mod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return kclrender.Run(dir, dir, nil)
}

// canonical re-encodes JSON so two documents compare by content: object keys
// sorted, numbers as written.
func canonical(t *testing.T, data []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// The own-collector fixture renders through forge.telemetry_collector, and
// what reaches KCL must be exactly what collector.Build returns for the same
// targets and options: the plugin is a transport, never a second translator.
func TestTelemetryCollectorPluginMatchesBuild(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "kcl", "tests", "positive_telemetry_own_collector.k"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderKCL(t, string(src))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var got struct {
		Targets  json.RawMessage            `json:"targets"`
		Fragment map[string]json.RawMessage `json:"fragment"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	targets, err := telemetry.Decode(got.Targets)
	if err != nil {
		t.Fatalf("telemetry_targets is not a valid output.telemetry: %v", err)
	}
	// The fixture's TelemetryCollectorOptions, as collector.Options.
	opts := collector.Options{
		Runtime:          telemetry.RuntimeCluster,
		Cluster:          "k3d-hub",
		Discovery:        collector.DiscoverAll,
		ResourceAttrs:    map[string]string{"deployment.environment.name": "dev"},
		Processors:       []string{"memory_limiter"},
		PostProcessors:   []string{"batch"},
		MetricsExporters: []string{"otlphttp/platform"},
		Isolated: &collector.IsolatedExporter{
			Name:   "otlphttp/selfmon",
			Config: map[string]any{"endpoint": "http://gateway.observability.svc:4318"},
		},
	}
	f, err := collector.Build(targets, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var reached []telemetry.Target
	for _, tg := range targets {
		if tg.Runtime == telemetry.RuntimeCluster && tg.Cluster == "k3d-hub" {
			reached = append(reached, tg)
		}
	}
	policies, skipped := collector.NetworkPolicies(reached, collector.CollectorRef{
		Namespace: "observability",
		PodLabels: map[string]string{"app.kubernetes.io/name": "agent"},
	})
	if len(policies) == 0 {
		t.Fatal("fixture renders no NetworkPolicy; the comparison would prove nothing about them")
	}

	logs := map[string]any{"preserve": f.Logs.Preserve, "processors": orEmpty(f.Logs.Processors)}
	want := map[string]any{
		"extensions":              f.Extensions,
		"receivers":               f.Receivers,
		"processors":              f.Processors,
		"exporters":               f.Exporters,
		"pipelines":               f.Pipelines,
		"service_extensions":      orEmpty(f.ServiceExtensions),
		"logs":                    logs,
		"node_metrics_processors": orEmpty(f.NodeMetricsProcessors),
		"k8sattributes_labels":    orEmpty(f.K8sAttributesLabels),
		"network_policies":        policies,
		"warnings":                append(orEmpty(f.Warnings), skipped...),
	}
	if len(got.Fragment) != len(want) {
		t.Errorf("fragment has keys %v, want %d", keys(got.Fragment), len(want))
	}
	for k, w := range want {
		g, ok := got.Fragment[k]
		if !ok {
			t.Errorf("fragment lacks %q", k)
			continue
		}
		if gs, ws := canonical(t, g), canonical(t, marshal(t, w)); gs != ws {
			t.Errorf("fragment.%s differs from collector.Build\n got: %s\nwant: %s", k, gs, ws)
		}
	}
	if len(f.Receivers) == 0 || f.Logs.Preserve == nil {
		t.Fatalf("fixture exercises too little of Build: receivers %v, preserve %v", f.Receivers, f.Logs.Preserve)
	}
}

func keys(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A target Build refuses fails the render, and the KCL error names the
// target, so the project sees which declaration to fix.
func TestTelemetryCollectorPluginErrorsNameTheTarget(t *testing.T) {
	cases := []struct {
		name string
		main string
		want []string
	}{
		{
			name: "isolated target with no isolated exporter",
			main: `import forge

_b = forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, telemetry = [forge.Telemetry {
        service_name = "bus", cluster = "k3d-hub", address = "bus.messaging.svc", isolated = True
        metrics = [forge.MetricsScrape {port = 7777}]
    }]}
}
out = forge.telemetry_collector(forge.telemetry_targets(_b), forge.TelemetryCollectorOptions {
    runtime = "cluster"
    metrics_exporters = ["otlphttp/platform"]
})
`,
			want: []string{"telemetry_collector:", "telemetry observability (service bus) is isolated"},
		},
		{
			name: "target Validate refuses",
			main: `import kcl_plugin.forge as plugin

out = plugin.telemetry_collector([{
    service_name = "engine", scope = "project", source = "manifests:engine", attachment = "manifests"
    runtime = "cluster", cluster = "k3d-hub", namespaces = ["engine"], selector = {app = "engine"}
    metrics = [{port = 9090, allow = "engine_(", path = "/metrics"}]
}], {runtime = "cluster", metrics_exporters = ["otlphttp/platform"]})
`,
			want: []string{"telemetry_collector:", "telemetry manifests:engine (service engine): metrics[0].allow"},
		},
		{
			name: "target that does not decode",
			main: `import kcl_plugin.forge as plugin

out = plugin.telemetry_collector([{service_name = "engine", runtime = "cluster", bogus = 1}], {runtime = "cluster"})
`,
			want: []string{"telemetry_collector: targets[0] (service engine)", "bogus"},
		},
		{
			name: "unknown option",
			main: `import kcl_plugin.forge as plugin

out = plugin.telemetry_collector([], {runtime = "cluster", exporter = "x"})
`,
			want: []string{"telemetry_collector: options:", "exporter"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := renderKCL(t, tc.main)
			if err == nil {
				t.Fatalf("render succeeded, want a refusal naming %q:\n%s", tc.want, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error does not name %q:\n%v", w, err)
				}
			}
		})
	}
}
