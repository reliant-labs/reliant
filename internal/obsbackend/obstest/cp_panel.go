package obstest

import (
	"encoding/json"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

var wireMetricType = map[obsquery.MetricType]string{
	obsquery.MetricGauge: "METRIC_TYPE_GAUGE", obsquery.MetricSum: "METRIC_TYPE_SUM", obsquery.MetricHistogram: "METRIC_TYPE_HISTOGRAM",
}

// CPPanelResponse is what the control plane's QueryPanel returns for a metrics
// panel whose evaluator produced res: the QueryPanelResponse message as
// protojson writes it (camelCase, a default-valued field omitted, int64 as a
// string). The control plane's handler runs obsquery.EvalMetricPanel and maps
// its result field for field, so a test can build the cloud reply from the same
// data the local backend reads and compare the two backends' output.
func CPPanelResponse(kind string, res obsquery.MetricPanelResult) string {
	out := map[string]any{"kind": kind}
	if res.StepSeconds != 0 {
		out["stepSeconds"] = res.StepSeconds
	}
	if res.Truncated {
		out["truncated"] = true
	}
	if res.SeriesTotal != 0 {
		out["seriesTotal"] = res.SeriesTotal
	}
	if len(res.Warnings) > 0 {
		out["warnings"] = res.Warnings
	}
	if meta := wireSeriesMetas(res.Meta); len(meta) > 0 {
		out["seriesMeta"] = meta
	}
	points := func(in []obsquery.MetricPoint) []map[string]any {
		var ps []map[string]any
		for _, p := range in {
			e := map[string]any{"time": p.Time.UTC().Format(time.RFC3339)}
			if p.Value != 0 {
				e["value"] = p.Value
			}
			if p.Partial {
				e["partial"] = true
			}
			if p.Null {
				e["null"] = true
			}
			ps = append(ps, e)
		}
		return ps
	}
	switch kind {
	case "table", "bar":
		var columns []string
		cols := map[string]int{}
		for _, s := range res.Series {
			if _, ok := cols[s.Label]; !ok {
				cols[s.Label] = len(columns)
				columns = append(columns, s.Label)
			}
		}
		type row struct {
			group  map[string]string
			values []float64
		}
		var rows []*row
		byKey := map[string]*row{}
		for _, s := range res.Series {
			key := groupKey(s.Labels)
			r := byKey[key]
			if r == nil {
				r = &row{group: s.Labels, values: make([]float64, len(columns))}
				byKey[key] = r
				rows = append(rows, r)
			}
			if s.Reduced != nil {
				r.values[cols[s.Label]] = *s.Reduced
			}
		}
		out["columns"] = columns
		var wrows []map[string]any
		for _, r := range rows {
			w := map[string]any{"values": r.values}
			if len(r.group) > 0 {
				w["group"] = r.group
			}
			wrows = append(wrows, w)
		}
		out["rows"] = wrows
	default:
		var ss []map[string]any
		for _, s := range res.Series {
			e := map[string]any{"label": s.Label}
			if len(s.Labels) > 0 {
				e["group"] = s.Labels
			}
			if s.Reduced != nil && *s.Reduced != 0 {
				e["value"] = *s.Reduced
			}
			if ps := points(s.Points); len(ps) > 0 {
				e["points"] = ps
			}
			ss = append(ss, e)
		}
		out["series"] = ss
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func groupKey(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func aggWire(a obsquery.Agg, q float64) (string, float64) {
	switch a {
	case obsquery.AggAvg:
		return "avg", 0
	case obsquery.AggSum:
		return "sum", 0
	case obsquery.AggMin:
		return "min", 0
	case obsquery.AggMax:
		return "max", 0
	case obsquery.AggCount:
		return "count", 0
	case obsquery.AggIncrease:
		return "increase", 0
	case obsquery.AggRate:
		return "rate", 0
	case obsquery.AggLast:
		return "last", 0
	case obsquery.AggP50:
		return "quantile", 0.5
	case obsquery.AggP95:
		return "quantile", 0.95
	case obsquery.AggP99:
		return "quantile", 0.99
	}
	return "quantile", q
}

func wireSeriesMetas(in []obsquery.SeriesMeta) []map[string]any {
	var meta []map[string]any
	for _, m := range in {
		e := map[string]any{"label": m.Label, "metricType": wireMetricType[m.Info.Type]}
		if m.Metric != "" {
			e["metric"] = m.Metric
		}
		if m.Info.Monotonic {
			e["monotonic"] = true
		}
		switch m.Info.Temporality {
		case obsquery.TemporalityDelta:
			e["temporality"] = "METRIC_TEMPORALITY_DELTA"
		case obsquery.TemporalityCumulative:
			e["temporality"] = "METRIC_TEMPORALITY_CUMULATIVE"
		}
		agg, q := aggWire(m.Agg, m.Quantile)
		e["aggregation"] = agg
		if q != 0 {
			e["quantile"] = q
		}
		if m.ValueUnit != "" {
			e["valueUnit"] = m.ValueUnit
		}
		meta = append(meta, e)
	}
	return meta
}
