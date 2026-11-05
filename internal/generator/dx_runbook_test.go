package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A new service project's operations scaffolding must point at the
// observability forge actually ships — ClickStack, the generated dashboards,
// the generated alert defaults tuned through forge.Alerts — and at nothing
// it does not. It used to write a Prometheus rules file no part of forge
// loads, an SLO doc whose targets nothing measured, and a runbook that sent
// the reader to "(Grafana dashboard)" and "Grafana: TODO (link)".
func TestGenerateDXFiles_ObservabilityPointsAtWhatForgeShips(t *testing.T) {
	root := t.TempDir()
	g := NewProjectGenerator("shop", root, "example.com/shop")
	g.Features = g.Features.With("observability", true)
	if err := g.generateDXFiles(); err != nil {
		t.Fatalf("generateDXFiles: %v", err)
	}

	for _, gone := range []string{
		"deploy/observability/prometheus-rules.yaml",
		"docs/slo.md",
	} {
		if _, err := os.Stat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s is scaffolded, but nothing in forge reads it", gone)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "docs", "runbook.md"))
	if err != nil {
		t.Fatalf("a service project gets a runbook: %v", err)
	}
	runbook := string(data)
	for _, stale := range []string{"Grafana", "Prometheus", "Loki", "Jaeger", "Tempo", "CloudWatch", "PagerDuty", "TODO (link"} {
		if strings.Contains(runbook, stale) {
			t.Errorf("runbook names %q, which forge does not ship:\n%s", stale, runbook)
		}
	}
	for _, want := range []string{
		"Apps → shop",                      // the hosted console
		"Observability",                    // ...its tab
		"HyperDX",                          // the local ClickStack UI
		"deploy/observability/dashboards/", // the generated dashboards
		"deploy/observability/alert-defaults_gen.json", // the generated alert catalog
		"forge.Alerts",         // where rules are declared and tuned
		"defaults",             // ...and the tuning map
		"ServiceErrorRateHigh", // the built-in alerts it explains
		"ServiceLatencyP95High",
		"forge env status",
	} {
		if !strings.Contains(runbook, want) {
			t.Errorf("runbook does not mention %q:\n%s", want, runbook)
		}
	}
}
