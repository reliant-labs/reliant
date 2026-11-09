package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/dashspec"
)

// The check that would have caught the selfmon NATS dashboard: gauges declared
// as sums, and filter counters named with "_" where the collector writes ".".
func TestDashboardMetricFindingsNameWrongKindsAndAbsentMetrics(t *testing.T) {
	stored := map[string][]obsbackend.MetricInfo{
		"gnatsd_varz_connections": {{Name: "gnatsd_varz_connections", Type: "gauge", Seen: true}},
		"gnatsd_varz_in_msgs":     {{Name: "gnatsd_varz_in_msgs", Type: "gauge", Seen: true}},
		"rpc.server.duration":     {{Name: "rpc.server.duration", Type: "histogram", Seen: true}, {Name: "rpc.server.duration", Type: "exp_histogram", Seen: true}},
	}
	calls := map[string]int{}
	lookup := func(name string) ([]obsbackend.MetricInfo, error) {
		calls[name]++
		return stored[name], nil
	}
	panel := func(id string, series ...dashspec.Series) dashspec.Panel {
		return dashspec.Panel{ID: id, Kind: "line", Signal: dashspec.Metrics, Series: series}
	}
	set := obsdash.BundleSet{Dashboards: []obsdash.BundleDashboard{{
		Path: "observability/dashboards/nats.json",
		Dashboard: dashspec.Dashboard{ID: "nats", Panels: []dashspec.Panel{
			panel("conns", dashspec.Series{Label: "c", Agg: "max", Field: "gnatsd_varz_connections", MetricType: "gauge"}),
			panel("msgs", dashspec.Series{Label: "in", Agg: "sum", Field: "gnatsd_varz_in_msgs", MetricType: "sum"}),
			panel("lat", dashspec.Series{Label: "p95", Agg: "quantile", Quantile: 0.95, Field: "rpc.server.duration", MetricType: "exp_histogram"}),
			panel("filtered", dashspec.Series{Label: "logs", Agg: "sum", Field: "otelcol_processor_filter_logs_filtered", MetricType: "sum"}),
			panel("again", dashspec.Series{Label: "c", Agg: "avg", Field: "gnatsd_varz_connections", MetricType: "gauge"}),
			{ID: "traces", Kind: "line", Signal: dashspec.Traces, Series: []dashspec.Series{{Label: "n", Agg: "count"}}},
		}},
	}}}
	problems, absent, err := dashboardMetricFindings(set, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], `panel "msgs": gnatsd_varz_in_msgs is declared sum but stored as gauge`) {
		t.Errorf("problems = %q", problems)
	}
	if len(absent) != 1 || !strings.Contains(absent[0], `panel "filtered": otelcol_processor_filter_logs_filtered`) {
		t.Errorf("absent = %q", absent)
	}
	if calls["gnatsd_varz_connections"] != 1 {
		t.Errorf("a metric two panels read was looked up %d times, want once", calls["gnatsd_varz_connections"])
	}
}
