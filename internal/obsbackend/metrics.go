package obsbackend

// What both backends share for metrics: the evaluator's result in the shape the
// verbs print, the compare read, the flattened table of `forge obs query
// --panel`, and the catalog entry. The orchestration itself (lookup, compile,
// run, fold, cap, combine) lives once, in obsquery.EvalMetricPanel.

import (
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// AggNames lists the --agg values, for help text.
const AggNames = "rate, increase, sum, avg, min, max, last, count, quantile (p50, p95 and p99 are the quantiles 0.5, 0.95 and 0.99)"

// AggOf is the dashspec aggregation an evaluator aggregation resolves to, and
// its quantile level (0 unless the aggregation is a quantile).
func AggOf(a obsquery.Agg, quantile float64) (string, float64) {
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
	case obsquery.AggQuantile:
		return "quantile", quantile
	}
	return "", 0
}

// MetricTypeName is "gauge", "sum", "histogram", "exp_histogram" or "summary";
// "" for an unknown kind.
func MetricTypeName(t obsquery.MetricType) string {
	switch t {
	case obsquery.MetricGauge, obsquery.MetricSum, obsquery.MetricHistogram, obsquery.MetricExpHistogram, obsquery.MetricSummary:
		return t.String()
	}
	return ""
}

func temporalityName(t obsquery.Temporality) string {
	switch t {
	case obsquery.TemporalityDelta, obsquery.TemporalityCumulative:
		return t.String()
	}
	return ""
}

// metricsResult is the evaluator's result as the backend's: the same fields,
// with the resolved aggregation spelled as dashspec does.
func metricsResult(pr obsquery.MetricPanelResult) MetricsQueryResult {
	out := MetricsQueryResult{
		SeriesTotal: pr.SeriesTotal, Truncated: pr.Truncated, StepSeconds: pr.StepSeconds,
		Warnings: pr.Warnings,
	}
	for _, m := range pr.Meta {
		agg, q := AggOf(m.Agg, m.Quantile)
		out.Meta = append(out.Meta, MetricMeta{
			Label: m.Label, Metric: m.Metric, Type: MetricTypeName(m.Info.Type), Monotonic: m.Info.Monotonic,
			Temporality: temporalityName(m.Info.Temporality), Agg: agg, Quantile: q, ValueUnit: m.ValueUnit,
		})
	}
	for _, s := range pr.Series {
		ms := MetricSeries{Label: s.Label, Labels: s.Labels, Reduced: s.Reduced}
		for _, p := range s.Points {
			ms.Points = append(ms.Points, MetricPoint{Time: p.Time.UTC(), Value: p.Value, Null: p.Null, Partial: p.Partial})
		}
		out.Series = append(out.Series, ms)
	}
	for _, st := range pr.Statements {
		out.Statements = append(out.Statements, stmt(st.Purpose, st.Stmt))
	}
	return out
}

// metricsWithCompare runs the panel and, when asked, once more over the range
// shifted back by req.Compare, its points moved forward by the same amount so
// the two overlay. Both reads are the same evaluator over the same panel.
func metricsWithCompare(req MetricsQueryRequest, run func(MetricsQueryRequest) (MetricsQueryResult, error)) (MetricsQueryResult, error) {
	if req.Compare < 0 {
		return MetricsQueryResult{}, Wrong("a compare offset must be positive")
	}
	cur := req
	cur.Compare = 0
	res, err := run(cur)
	if err != nil || req.Compare == 0 {
		return res, err
	}
	prev := cur
	prev.Range = Range{From: req.Range.From.Add(-req.Compare), To: req.Range.To.Add(-req.Compare)}
	if !req.Now.IsZero() {
		prev.Now = req.Now.Add(-req.Compare)
	}
	pr, err := run(prev)
	if err != nil {
		return res, err
	}
	for _, st := range pr.Statements {
		st.Purpose += " (compare)"
		res.Statements = append(res.Statements, st)
	}
	if req.Explain {
		return res, nil
	}
	cmp := &MetricsCompare{Offset: req.Compare, From: prev.Range.From, To: prev.Range.To, SeriesTotal: pr.SeriesTotal}
	for _, s := range pr.Series {
		shifted := s
		shifted.Points = make([]MetricPoint, len(s.Points))
		for i, p := range s.Points {
			p.Time = p.Time.Add(req.Compare)
			shifted.Points[i] = p
		}
		cmp.Series = append(cmp.Series, shifted)
	}
	res.Compare = cmp
	return res, nil
}

// capMetricSeries keeps the first max series of each label (they arrive
// heaviest first) and reports whether any was cut: the control plane has no
// per-request series cap, so a lowered --limit is applied on this side.
func capMetricSeries(in []MetricSeries, max int) ([]MetricSeries, bool) {
	if max <= 0 {
		return in, false
	}
	per := map[string]int{}
	out := in[:0:0]
	cut := false
	for _, s := range in {
		if per[s.Label] >= max {
			cut = true
			continue
		}
		per[s.Label]++
		out = append(out, s)
	}
	return out, cut
}

// flattenMetrics turns a metrics panel's result into the table `forge obs
// query --panel` prints: a line is one row per point (series, one column per
// group key, time, value); a number, table or bar is one row per group with
// its reduction. A gap is an empty cell.
func flattenMetrics(r MetricsQueryResult) ([]Column, [][]any) {
	keys := map[string]bool{}
	for _, s := range r.Series {
		for k := range s.Labels {
			keys[k] = true
		}
	}
	groups := make([]string, 0, len(keys))
	for k := range keys {
		groups = append(groups, k)
	}
	sort.Strings(groups)
	reduced := false
	for _, s := range r.Series {
		if s.Reduced != nil || len(s.Points) == 0 {
			reduced = true
		}
	}
	cols := []Column{{Name: "series", Type: "String"}}
	for _, g := range groups {
		cols = append(cols, Column{Name: g, Type: "String"})
	}
	if !reduced {
		cols = append(cols, Column{Name: "time", Type: "DateTime"})
	}
	cols = append(cols, Column{Name: "value", Type: "Float64"})
	var rows [][]any
	for _, s := range r.Series {
		prefix := []any{s.Label}
		for _, g := range groups {
			prefix = append(prefix, s.Labels[g])
		}
		cell := func(v float64, null bool) any {
			if null {
				return nil
			}
			return finite(v)
		}
		switch {
		case reduced:
			var v any
			if s.Reduced != nil {
				v = finite(*s.Reduced)
			}
			rows = append(rows, append(append([]any{}, prefix...), v))
		default:
			for _, p := range s.Points {
				rows = append(rows, append(append([]any{}, prefix...), p.Time.UTC().Format(time.RFC3339), cell(p.Value, p.Null)))
			}
		}
	}
	return cols, rows
}

func catalogInfos(in []obsquery.CatalogEntry) []MetricInfo {
	out := make([]MetricInfo, len(in))
	for i, e := range in {
		out[i] = catalogInfo(e)
	}
	return out
}

func catalogInfo(e obsquery.CatalogEntry) MetricInfo {
	mi := MetricInfo{
		Name: e.Name, Unit: e.Unit, Description: e.Description, Type: e.Kind.String(), Monotonic: e.Monotonic,
		Temporality: temporalityName(e.Temporality), Services: e.Services, ServiceCount: e.ServiceCount,
		LastSeen: e.LastSeen, SeriesEstimate: e.SeriesEstimate, Queryable: e.Queryable, Builtin: e.Builtin,
		Declared: e.Declared, Seen: e.Seen, Stability: e.Stability,
	}
	for _, a := range e.DeclaredAttributes {
		mi.DeclaredAttributes = append(mi.DeclaredAttributes, MetricAttributeSpec{Key: a.Key, Values: a.Values})
	}
	return mi
}
