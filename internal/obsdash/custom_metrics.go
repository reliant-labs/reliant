package obsdash

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// CustomMetricsID is the id of the dashboard BuildCustomMetrics returns, so its
// file is "<project>-custom-metrics_gen.json".
const CustomMetricsID = "custom-metrics"

// IsBuiltinMetric reports whether name is one of forge's own metrics (rpc.*,
// go.*, forge.*, a component's .calls/.errors/.duration). Those have
// purpose-built dashboards; the custom-metrics dashboard is for the rest.
func IsBuiltinMetric(name string) bool { return metrics.IsBuiltin(name) }

// BuildCustomMetrics turns a metrics schema into the "Custom metrics"
// dashboard: one section per project-declared metric, drawn by its type.
//
//	counter        a rate, per second
//	updowncounter  the current value (last)
//	gauge          the current value (last)
//	histogram      p50, p95 and p99
//
// A metric with enumerated attribute values is grouped by each such attribute
// (one panel per attribute) — the closed set is what makes a group-by safe,
// since the series count is bounded by the declaration. Attributes without an
// enumerated set are offered as dashboard filters, not group-bys, so a
// high-cardinality key never fans a panel out into hundreds of lines.
//
// Deprecated metrics are omitted. It returns false when the schema declares
// nothing of the project's own.
func BuildCustomMetrics(project string, schema metrics.Schema) (Dashboard, bool) {
	d := Dashboard{
		ID:          CustomMetricsID,
		Title:       project + ": custom metrics",
		Group:       GroupCustom,
		Description: "Every metric this project declares in internal/metrics, drawn by type: counters as a rate, histograms as p50/p95/p99, gauges as the last value, grouped by their enumerated attributes. Generated from the metrics schema (`metrics schema --json`).",
	}
	for _, f := range commonFilters() {
		f.Signal = Metrics
		d.Filters = append(d.Filters, f)
	}
	filters := map[string]bool{}

	for _, m := range schema.Metrics {
		if m.Stability == metrics.Deprecated || IsBuiltinMetric(m.Name) || isCallMetric(m.Name) {
			continue
		}
		panels := metricPanels(m)
		if len(panels) == 0 {
			continue
		}
		d.Panels = append(d.Panels, panels...)
		for _, a := range m.Attributes {
			if len(a.Values) == 0 && !filters[a.Key] {
				filters[a.Key] = true
				d.Filters = append(d.Filters, Filter{ID: "attr-" + a.Key, Label: a.Key, Field: "metric.attr." + a.Key, Signal: Metrics})
			}
		}
	}
	return withReleaseMarkers(d), len(d.Panels) > 0
}

// isCallMetric matches the per-component <package>.calls/.errors/.duration
// metrics the generated component wrappers emit; they are infrastructure, not
// the project's own declarations.
func isCallMetric(name string) bool {
	for _, suffix := range []string{".calls", ".errors", ".duration"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func metricPanels(m metrics.Spec) []Panel {
	id := panelID(m.Name)
	title := m.Name
	if m.Stability == metrics.Experimental {
		title += " (experimental)"
	}
	unit := unitFor(m.Unit)

	var series []Series
	switch m.Type {
	case metrics.Counter:
		// A rate is per second whatever the bucket width, so the number does
		// not change when the reader zooms (an increase per bucket would).
		series = []Series{{Label: "per second", Agg: AggRate, Field: m.Name, MetricType: "sum"}}
	case metrics.UpDownCounter:
		series = []Series{{Label: "value", Agg: AggLast, Field: m.Name, MetricType: "sum"}}
	case metrics.Gauge:
		series = []Series{{Label: "value", Agg: AggLast, Field: m.Name, MetricType: "gauge"}}
	case metrics.Histogram:
		series = []Series{
			{Label: "p50", Agg: AggQuantile, Quantile: 0.5, Field: m.Name, MetricType: "histogram"},
			{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: m.Name, MetricType: "histogram"},
			{Label: "p99", Agg: AggQuantile, Quantile: 0.99, Field: m.Name, MetricType: "histogram"},
		}
	default:
		return nil
	}

	desc := m.Description
	if m.Unit != "" {
		desc = fmt.Sprintf("%s Unit: %s.", strings.TrimSpace(desc), m.Unit)
	}
	panels := []Panel{{
		ID: id, Title: title, Description: desc, Kind: KindLine, Signal: Metrics,
		Series: series, Unit: unit, Width: 12, Height: 7,
	}}
	for _, a := range m.Attributes {
		if len(a.Values) == 0 {
			continue
		}
		panels = append(panels, Panel{
			ID: id + "-by-" + panelID(a.Key), Title: title + " by " + a.Key,
			Description: fmt.Sprintf("%s, one line per %s (%s).", m.Name, a.Key, strings.Join(a.Values, ", ")),
			Kind:        KindLine, Signal: Metrics, Series: series,
			GroupBy: []string{"metric.attr." + a.Key}, Unit: unit, Width: 12, Height: 7,
		})
	}
	return panels
}

// panelID makes a metric name or attribute key usable as a panel id.
func panelID(s string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(s)
}

// unitFor maps a UCUM unit onto the display units a panel formats: "ms", "s"
// and "By" are formatted as such. A counted thing ("{order}") shows as a plain
// count. "1" and "%" are left unformatted:
// UnitPercent formats a fraction in [0,1], and a dimensionless metric need not
// be one.
func unitFor(ucum string) Unit {
	switch ucum {
	case "ms":
		return UnitMillis
	case "By":
		return UnitBytes
	case "s":
		return UnitSeconds
	}
	if strings.HasPrefix(ucum, "{") {
		return UnitCount
	}
	return UnitNone
}
