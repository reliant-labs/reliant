package cli

// The metrics explorer's query is a dashspec panel (`signal: metrics`). These
// flags build that panel, so `forge obs metrics query` and `forge obs query
// --panel` are two spellings of one question, and the console's "copy as CLI"
// emits exactly this grammar:
//
//	forge obs metrics query [env] <metric>              series A (the positional metric)
//	  --agg AGG [--quantile Q]                          A's aggregation (default per metric kind)
//	  --query NAME=metric[:agg[:q]]                     one more series (repeatable)
//	  --where NAME:key=value | NAME:key!=value          narrow series NAME alone (repeatable)
//	  --filter key=value | key!=value                   narrow every series
//	  --group-by KEY                                    split by an attribute (repeatable, at most 3)
//	  --formula 'B / A * 100'                           plot an expression of the series
//	  --kind line|number|table|bar                      the panel's shape
//	  --top N                                           a table or bar keeps the top N groups
//	  --compare DURATION|prev-release                   read the same panel over an earlier range too
//	  --panel-out FILE|-                                write the panel instead of running it

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// metricsPanelFlags are the flags that build the panel.
type metricsPanelFlags struct {
	agg      string
	quantile float64
	queries  []string
	where    []string
	formula  string
	kind     string
	top      int
	groupBy  []string
}

// seriesLabelRe is what a --query NAME may be: the label a formula never
// names (it names a series by its position, A, B, ...), so NAME is only a
// handle for --where and the output.
var seriesLabelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

// metricSpecAggs are the aggregations a --query spec may name; quantile takes
// a level, and p50, p95 and p99 are shorthand for 0.5, 0.95 and 0.99.
var metricSpecAggs = map[string]bool{
	"rate": true, "increase": true, "sum": true, "avg": true, "min": true, "max": true,
	"last": true, "count": true, "quantile": true,
}

// parseAggSpec reads one aggregation word (and level) into the dashspec agg
// and quantile. An empty word is "the metric's default", spelled agg "".
func parseAggSpec(word, level string) (agg string, q float64, err error) {
	switch word {
	case "":
		if level != "" {
			return "", 0, obsbackend.Wrong("a quantile level needs the quantile aggregation (--agg quantile --quantile %s)", level)
		}
		return "", 0, nil
	case "p50", "p95", "p99":
		if level != "" {
			return "", 0, obsbackend.Wrong("%s is already a level; drop the quantile %s", word, level)
		}
		n, _ := strconv.ParseFloat(strings.TrimPrefix(word, "p"), 64)
		return "quantile", n / 100, nil
	}
	if !metricSpecAggs[word] {
		return "", 0, obsbackend.Wrong("aggregation %q: use %s", word, obsbackend.AggNames)
	}
	if word != "quantile" {
		if level != "" {
			return "", 0, obsbackend.Wrong("a quantile level goes with the quantile aggregation, not %s", word)
		}
		return word, 0, nil
	}
	if level == "" {
		return "", 0, obsbackend.Wrong("the quantile aggregation needs a level in (0, 1]: --quantile 0.95, or --query NAME=metric:quantile:0.95")
	}
	q, perr := strconv.ParseFloat(level, 64)
	if perr != nil || !(q > 0 && q <= 1) {
		return "", 0, obsbackend.Wrong("quantile %q is not a number in (0, 1]", level)
	}
	return "quantile", q, nil
}

// parsePredicate reads key=value or key!=value into a dashspec predicate; the
// key is a panel field (metric.attr.<k>, service.name, resource.attr.<k>) or,
// like --filter, a bare attribute key, which is read as the metric's own.
func parsePredicate(s string) (obsquery.PanelPredicate, error) {
	f, err := obsbackend.ParseFilter(s)
	if err != nil {
		return obsquery.PanelPredicate{}, err
	}
	op := "eq"
	if f.Not {
		op = "neq"
	}
	return obsquery.PanelPredicate{Field: panelField(f.Key), Op: op, Value: f.Value}, nil
}

// panelField maps a --where or --filter key to the panel field vocabulary. A
// key the panel already spells (service.name, deployment.environment.name,
// metric.attr.*, resource.attr.*) is kept, so a panel read back from the
// console's copy-as-CLI round-trips; a bare resource key (k8s.pod.name,
// service.version) is read off the resource; any other is the metric's own
// attribute. --group-by keys are passed through as typed: the evaluator reads
// an allowlisted or declared key from the metric's attributes first.
func panelField(key string) string {
	switch {
	case key == dashspec.FieldService, key == dashspec.FieldEnv,
		strings.HasPrefix(key, "metric.attr."), strings.HasPrefix(key, "resource.attr."):
		return key
	case key == "k8s.container.last_terminated_reason":
		return "metric.attr." + key
	}
	for _, prefix := range []string{"service.", "reliant.", "k8s.", "container.image."} {
		if strings.HasPrefix(key, prefix) && obsquery.Allowed(key) {
			return "resource.attr." + key
		}
	}
	return "metric.attr." + key
}

// buildMetricsPanel is the panel the flags describe. metric is series A.
func buildMetricsPanel(metric string, f metricsPanelFlags, filters []string) (obsquery.Panel, error) {
	agg, q, err := parseAggSpec(f.agg, quantileArg(f))
	if err != nil {
		return obsquery.Panel{}, err
	}
	p := obsquery.Panel{ID: "explore", Kind: "line", Signal: "metrics", GroupBy: f.groupBy, Formula: f.formula, Limit: f.top}
	if f.kind != "" {
		switch f.kind {
		case "line", "number", "table", "bar":
			p.Kind = f.kind
		default:
			return p, obsbackend.Wrong("--kind %q: use line, number, table or bar", f.kind)
		}
	}
	p.Series = append(p.Series, obsquery.PanelSeries{Label: dashspec.SeriesRef(0), Field: metric, Agg: agg, Quantile: q})
	byName := map[string]int{dashspec.SeriesRef(0): 0}
	for _, spec := range f.queries {
		name, rest, ok := strings.Cut(spec, "=")
		if !ok || name == "" || rest == "" {
			return p, obsbackend.Wrong("--query %q: use NAME=metric[:agg[:q]]", spec)
		}
		if !seriesLabelRe.MatchString(name) {
			return p, obsbackend.Wrong("--query %q: NAME is a letter then letters, digits, '.', '_' or '-'", spec)
		}
		if _, dup := byName[name]; dup {
			return p, obsbackend.Wrong("--query %q: the series name %s is taken (the positional metric is A)", spec, name)
		}
		parts := strings.Split(rest, ":")
		if len(parts) > 3 {
			return p, obsbackend.Wrong("--query %q: use NAME=metric[:agg[:q]]", spec)
		}
		var word, level string
		if len(parts) > 1 {
			word = parts[1]
		}
		if len(parts) > 2 {
			level = parts[2]
		}
		sa, sq, err := parseAggSpec(word, level)
		if err != nil {
			return p, fmt.Errorf("--query %q: %w", spec, err)
		}
		byName[name] = len(p.Series)
		p.Series = append(p.Series, obsquery.PanelSeries{Label: name, Field: parts[0], Agg: sa, Quantile: sq})
	}
	for _, w := range f.where {
		name, pred, ok := strings.Cut(w, ":")
		if !ok || name == "" || pred == "" {
			return p, obsbackend.Wrong("--where %q: use NAME:key=value or NAME:key!=value", w)
		}
		i, ok := byName[name]
		if !ok {
			return p, obsbackend.Wrong("--where %q: no series named %s (the series are %s)", w, name, strings.Join(seriesNames(p), ", "))
		}
		pp, err := parsePredicate(pred)
		if err != nil {
			return p, fmt.Errorf("--where %q: %w", w, err)
		}
		p.Series[i].Where = append(p.Series[i].Where, pp)
	}
	for _, fl := range filters {
		pp, err := parsePredicate(fl)
		if err != nil {
			return p, err
		}
		p.Where = append(p.Where, pp)
	}
	return p, nil
}

func quantileArg(f metricsPanelFlags) string {
	if f.quantile == 0 {
		return ""
	}
	return strconv.FormatFloat(f.quantile, 'g', -1, 64)
}

func seriesNames(p obsquery.Panel) []string {
	out := make([]string, len(p.Series))
	for i, s := range p.Series {
		out[i] = s.Label
	}
	return out
}

// panelSeriesType fills each series' metricType from the backend's catalog is
// not possible offline, so the written panel names it only when known; a
// dashspec metrics series needs it, so --panel-out looks the kind up first
// (see resolveSeriesTypes).
//
// dashPanel is p as the dashspec document a dashboard file holds.
func dashPanel(p obsquery.Panel) dashspec.Panel {
	out := dashspec.Panel{
		ID: p.ID, Title: "Explore", Kind: dashspec.PanelKind(p.Kind), Signal: dashspec.Signal(p.Signal),
		Ratio: p.Ratio, Formula: p.Formula, Limit: p.Limit, GroupBy: p.GroupBy,
	}
	for _, w := range p.Where {
		out.Where = append(out.Where, dashspec.Predicate{Field: w.Field, Op: dashspec.Op(w.Op), Value: w.Value, Values: w.Values})
	}
	for _, s := range p.Series {
		ds := dashspec.Series{Label: s.Label, Agg: dashspec.Agg(s.Agg), Quantile: s.Quantile, Field: s.Field, MetricType: s.MetricType}
		for _, w := range s.Where {
			ds.Where = append(ds.Where, dashspec.Predicate{Field: w.Field, Op: dashspec.Op(w.Op), Value: w.Value, Values: w.Values})
		}
		out.Series = append(out.Series, ds)
	}
	return out
}

// writePanel writes p as one dashspec panel, indented, to dest (- is stdout).
func writePanel(dest string, stdout io.Writer, p obsquery.Panel) error {
	raw, err := json.MarshalIndent(dashPanel(p), "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if dest == "-" {
		_, err = stdout.Write(raw)
		return err
	}
	if err := atomicfile.WriteFile(dest, raw, 0o644); err != nil {
		return obsbackend.Wrong("write --panel-out: %v", err)
	}
	return nil
}
