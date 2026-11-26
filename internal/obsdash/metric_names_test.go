package obsdash

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// TestForgeMetricPanelsReadDeclaredMetrics pins the generated dashboards to
// the metrics forge's runtime declares (metrics.builtinSpecs, which the
// Connect interceptor, the Go runtime instrumentation and pkg/resmetrics emit
// under). Every metrics series over a forge-owned name (forgeOwned) must name a
// declared built-in, with a metricType its declared instrument is stored as.
// A dashboard that queries a name nothing emits draws "no data" forever, and
// pkg/observe's TestChainCountsRPCsOnTheGlobalMeterProvider pins the other
// half: that a server's chain really emits these names.
func TestForgeMetricPanelsReadDeclaredMetrics(t *testing.T) {
	in := fixture()
	// A host process (no node agent) draws the host resource panels too.
	in.Processes = append(in.Processes, Process{Name: "shop-local", Binary: "shop", Args: []string{"local"}, ServiceNames: []string{"shop-local"}, Workloads: []string{"shop-local"}, Kind: "worker"})
	sp := Build(in)
	stored := map[metrics.Type][]string{
		metrics.Counter:       {"sum"},
		metrics.UpDownCounter: {"sum"},
		metrics.Histogram:     {"histogram", "exp_histogram"},
		metrics.Gauge:         {"gauge"},
	}
	seen := map[string]bool{}
	for _, d := range sp.Dashboards {
		for _, p := range d.Panels {
			if p.Signal != Metrics {
				continue
			}
			for _, s := range p.Series {
				if !forgeOwned(s.Field) {
					continue
				}
				seen[s.Field] = true
				if !metrics.IsBuiltin(s.Field) {
					t.Errorf("%s/%s series %q reads %s, which forge does not declare (pkg/observe/metrics/builtin.go)", d.ID, p.ID, s.Label, s.Field)
					continue
				}
				spec := metrics.BuiltinSpec(s.Field)
				ok := false
				for _, mt := range stored[spec.Type] {
					ok = ok || s.MetricType == mt
				}
				if !ok {
					t.Errorf("%s/%s series %q reads %s as metricType %q; it is declared a %s (stored as %v)", d.ID, p.ID, s.Label, s.Field, s.MetricType, spec.Type, stored[spec.Type])
				}
			}
		}
	}
	for _, name := range []string{"rpc.server.calls", "rpc.server.errors", "rpc.server.duration", "process.cpu.utilization", "system.cpu.utilization"} {
		if !seen[name] {
			t.Errorf("no generated dashboard reads %s; this test would pass vacuously", name)
		}
	}
}

// forgeOwned reports whether a metric name is in a namespace forge's runtime
// emits under: the Connect interceptor's, the Go runtime's, and the process
// and host resource metrics of pkg/resmetrics.
func forgeOwned(name string) bool {
	for _, p := range []string{"rpc.server.", "go.", "process.", "system.", "forge.process.", "forge.system.", "forge.cgroup.", "forge.resource."} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
