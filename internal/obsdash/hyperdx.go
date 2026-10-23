package obsdash

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// PinnedHyperDX is the HyperDX / ClickStack version the rendered JSON is
// written and validated against. The provisioned-dashboard schema
// (DashboardWithoutIdSchema in @hyperdx/common-utils) is not a versioned
// public contract, so a bump of the ClickStack image is a bump of this
// constant AND a re-run of the schema gate (see hyperdx_schema_test.go).
const PinnedHyperDX = "2.40.0"

// Source names. The ClickStack local image pre-creates exactly these three
// sources (and Sessions); a tile names one of them in `config.source`.
//
// The provisioner stores `source` verbatim, but the HyperDX UI resolves a
// tile's source by mongo id, and ids are random per database. So a rendered
// dashboard carries NAMES, and whatever delivers it resolves them: the local
// compose entrypoint rewrites names to ids before it provisions, and a hosted
// deliverer does the same through GET /api/v2/sources. Keeping names in the
// file keeps it identical across every database.
const (
	SourceLogs    = "Logs"
	SourceTraces  = "Traces"
	SourceMetrics = "Metrics"
)

// HyperDXDashboard is a dashboard in the shape the HyperDX provisioner and
// the v2 dashboards API accept (DashboardWithoutIdSchema).
type HyperDXDashboard struct {
	Name    string          `json:"name"`
	Tags    []string        `json:"tags"`
	Tiles   []HyperDXTile   `json:"tiles"`
	Filters []HyperDXFilter `json:"filters,omitempty"`
}

// HyperDXTile is one chart on the 24-column grid.
type HyperDXTile struct {
	ID     string       `json:"id"`
	X      int          `json:"x"`
	Y      int          `json:"y"`
	W      int          `json:"w"`
	H      int          `json:"h"`
	Config HyperDXChart `json:"config"`
}

// HyperDXChart is a builder-style saved chart config.
type HyperDXChart struct {
	Name          string            `json:"name"`
	Source        string            `json:"source"`
	DisplayType   string            `json:"displayType"`
	Select        any               `json:"select"` // []HyperDXSelect, or a column string for a search tile
	Where         string            `json:"where"`
	WhereLanguage string            `json:"whereLanguage"`
	GroupBy       string            `json:"groupBy,omitempty"`
	Granularity   string            `json:"granularity,omitempty"`
	SeriesReturn  string            `json:"seriesReturnType,omitempty"`
	NumberFormat  *HyperDXNumFormat `json:"numberFormat,omitempty"`
	Align         *bool             `json:"alignDateRangeToGranularity,omitempty"`
}

// HyperDXSelect is one aggregate of a chart.
type HyperDXSelect struct {
	AggFn         string            `json:"aggFn"`
	Level         float64           `json:"level,omitempty"`
	ValueExpr     string            `json:"valueExpression"`
	AggCondition  string            `json:"aggCondition,omitempty"`
	AggCondLang   string            `json:"aggConditionLanguage,omitempty"`
	Alias         string            `json:"alias,omitempty"`
	MetricName    string            `json:"metricName,omitempty"`
	MetricType    string            `json:"metricType,omitempty"`
	NumberFormatP *HyperDXNumFormat `json:"numberFormat,omitempty"`
}

// HyperDXNumFormat mirrors NumberFormatSchema.
type HyperDXNumFormat struct {
	Output            string `json:"output"`
	Mantissa          *int   `json:"mantissa,omitempty"`
	ThousandSeparated bool   `json:"thousandSeparated,omitempty"`
	Unit              string `json:"unit,omitempty"`
}

// HyperDXFilter is a QUERY_EXPRESSION dashboard filter: a dropdown whose
// selection is added to every tile's WHERE.
type HyperDXFilter struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	Expression    string `json:"expression"`
	Source        string `json:"source"`
	WhereLanguage string `json:"whereLanguage"`
}

// HyperDXSavedSearch is the body of POST /api/v2/saved-searches style
// creation; the file provisioner has no saved-search path, so it is rendered
// for the API deliverer and listed in the spec.
type HyperDXSavedSearch struct {
	Name          string   `json:"name"`
	Source        string   `json:"source"`
	Select        string   `json:"select"`
	Where         string   `json:"where"`
	WhereLanguage string   `json:"whereLanguage"`
	OrderBy       string   `json:"orderBy"`
	Tags          []string `json:"tags"`
}

// Rendered is one output file: a name relative to the dashboards directory
// and its bytes.
type Rendered struct {
	File    string
	Content []byte
}

// DashboardName is the HyperDX dashboard name for d: the provisioner upserts
// by name, so it must be stable and unique across the project.
func DashboardName(d Dashboard) string { return d.Title }

// RenderHyperDX renders every dashboard of sp as one provisioner file each,
// named "<project>-<dashboard id>_gen.json".
func RenderHyperDX(sp Spec) ([]Rendered, error) {
	out := make([]Rendered, 0, len(sp.Dashboards))
	for _, d := range sp.Dashboards {
		hd, err := renderDashboard(d)
		if err != nil {
			return nil, fmt.Errorf("dashboard %q: %w", d.ID, err)
		}
		b, err := json.MarshalIndent(hd, "", "  ")
		if err != nil {
			return nil, err
		}
		out = append(out, Rendered{File: DashboardFile(sp.Project, d), Content: append(b, '\n')})
	}
	return out, nil
}

// DashboardFile is the file name of d inside the dashboards directory.
func DashboardFile(project string, d Dashboard) string {
	return project + "-" + d.ID + "_gen.json"
}

// RenderHyperDXSavedSearches renders the spec's saved searches in the shape
// of the HyperDX saved-search API.
func RenderHyperDXSavedSearches(sp Spec) ([]HyperDXSavedSearch, error) {
	var out []HyperDXSavedSearch
	for _, s := range sp.SavedSearches {
		where, err := predicatesSQL(s.Signal, s.Where)
		if err != nil {
			return nil, fmt.Errorf("saved search %q: %w", s.ID, err)
		}
		cols, err := columnsSQL(s.Signal, s.Columns)
		if err != nil {
			return nil, err
		}
		out = append(out, HyperDXSavedSearch{
			Name: sp.Project + ": " + s.Title, Source: sourceFor(s.Signal), Select: cols,
			Where: where, WhereLanguage: "sql", OrderBy: "Timestamp DESC", Tags: []string{"forge", "generated"},
		})
	}
	return out, nil
}

func sourceFor(s Signal) string {
	switch s {
	case Logs:
		return SourceLogs
	case Metrics:
		return SourceMetrics
	default:
		return SourceTraces
	}
}

func renderDashboard(d Dashboard) (HyperDXDashboard, error) {
	hd := HyperDXDashboard{Name: DashboardName(d), Tags: []string{"forge", "generated"}, Tiles: []HyperDXTile{}}
	for _, f := range d.Filters {
		sig := f.Signal
		if sig == "" {
			sig = Traces
		}
		expr, err := fieldSQL(sig, f.Field)
		if err != nil {
			return hd, err
		}
		hd.Filters = append(hd.Filters, HyperDXFilter{
			ID: "filter-" + f.ID, Type: "QUERY_EXPRESSION", Name: f.Label,
			Expression: expr, Source: sourceFor(sig), WhereLanguage: "sql",
		})
	}
	x, y, rowH := 0, 0, 0
	seen := map[string]bool{}
	for _, p := range d.Panels {
		if seen[p.ID] {
			return hd, fmt.Errorf("duplicate panel id %q", p.ID)
		}
		seen[p.ID] = true
		if !hyperDXDraws(p) {
			continue
		}
		w, h := p.Width, p.Height
		if w <= 0 || w > gridColumns {
			w = gridColumns
		}
		if h <= 0 {
			h = 6
		}
		if x+w > gridColumns {
			x, y, rowH = 0, y+rowH, 0
		}
		cfg, err := renderChart(p)
		if err != nil {
			return hd, fmt.Errorf("panel %q: %w", p.ID, err)
		}
		hd.Tiles = append(hd.Tiles, HyperDXTile{ID: "t-" + p.ID, X: x, Y: y, W: w, H: h, Config: cfg})
		x += w
		if h > rowH {
			rowH = h
		}
	}
	return hd, nil
}

const gridColumns = dashspec.GridColumns

// hyperDXDraws reports whether HyperDX can draw p. It cannot draw the
// console's own kinds (its UI already plays replay and searches spans), and it
// has no equivalent of a top-N bar over groups, a value-bucketed heatmap of a
// field, a raw SQL panel or arbitrary arithmetic across series: those panels
// are the console's, and a HyperDX dashboard leaves them out rather than draw
// something that means something else.
func hyperDXDraws(p Panel) bool {
	switch {
	case IsConsoleKind(p.Kind), p.Formula != "":
		return false
	case p.Kind == KindBar, p.Kind == KindHeatmap, p.Kind == KindSQL:
		return false
	}
	return true
}

func renderChart(p Panel) (HyperDXChart, error) {
	where, err := predicatesSQL(p.Signal, p.Where)
	if err != nil {
		return HyperDXChart{}, err
	}
	c := HyperDXChart{Name: p.Title, Source: sourceFor(p.Signal), Where: where, WhereLanguage: "sql", NumberFormat: numberFormat(p.Unit)}

	switch p.Kind {
	case KindSearch:
		cols, err := columnsSQL(p.Signal, p.Columns)
		if err != nil {
			return c, err
		}
		c.DisplayType, c.Select = "search", cols
		c.NumberFormat = nil
		return c, nil
	case KindNumber:
		c.DisplayType = "number"
	case KindLine:
		c.DisplayType = "line"
		c.Granularity = "auto"
	case KindTable:
		c.DisplayType = "table"
		c.NumberFormat = nil
	default:
		return c, fmt.Errorf("unknown panel kind %q", p.Kind)
	}
	if len(p.Series) == 0 {
		return c, fmt.Errorf("panel has no series")
	}
	if p.Ratio {
		if len(p.Series) != 2 || p.Kind != KindLine {
			return c, fmt.Errorf("a ratio panel is a line with exactly two series")
		}
		c.SeriesReturn = "ratio"
	}
	sel := make([]HyperDXSelect, 0, len(p.Series))
	for _, s := range p.Series {
		hs, err := renderSeries(p.Signal, s)
		if err != nil {
			return c, err
		}
		sel = append(sel, hs)
	}
	c.Select = sel
	if len(p.GroupBy) > 0 {
		exprs := make([]string, 0, len(p.GroupBy))
		for _, g := range p.GroupBy {
			e, err := fieldSQL(p.Signal, g)
			if err != nil {
				return c, err
			}
			exprs = append(exprs, e)
		}
		c.GroupBy = strings.Join(exprs, ", ")
	}
	return c, nil
}

func numberFormat(u Unit) *HyperDXNumFormat {
	zero, one := 0, 1
	switch u {
	case UnitCount:
		return &HyperDXNumFormat{Output: "number", Mantissa: &zero, ThousandSeparated: true}
	case UnitMillis:
		return &HyperDXNumFormat{Output: "number", Mantissa: &one, ThousandSeparated: true, Unit: "ms"}
	case UnitSeconds:
		return &HyperDXNumFormat{Output: "number", Mantissa: &one, ThousandSeparated: true, Unit: "s"}
	case UnitBytes:
		return &HyperDXNumFormat{Output: "byte", Mantissa: &one}
	case UnitPercent:
		return &HyperDXNumFormat{Output: "percent", Mantissa: &one}
	}
	return nil
}

func renderSeries(sig Signal, s Series) (HyperDXSelect, error) {
	cond, err := predicatesSQL(sig, s.Where)
	if err != nil {
		return HyperDXSelect{}, err
	}
	hs := HyperDXSelect{Alias: s.Label, AggCondition: cond}
	if cond != "" {
		hs.AggCondLang = "sql"
	}
	if sig == Metrics {
		if s.Field == "" {
			return hs, fmt.Errorf("metric series %q has no metric name", s.Label)
		}
		hs.MetricName, hs.MetricType = s.Field, s.MetricType
		switch s.Agg {
		case AggSum:
			hs.AggFn = "sum"
		case AggAvg:
			hs.AggFn = "avg"
		case AggMax:
			hs.AggFn = "max"
		case AggLast:
			hs.AggFn = "last_value"
		case AggQuantile:
			if s.MetricType != "histogram" {
				return hs, fmt.Errorf("metric quantile %q needs metricType histogram, got %q", s.Label, s.MetricType)
			}
			hs.AggFn, hs.Level = "quantile", s.Quantile
		default:
			return hs, fmt.Errorf("metric aggregate %q is not supported", s.Agg)
		}
		return hs, nil
	}
	switch s.Agg {
	case AggCount:
		hs.AggFn = "count"
	case AggCountDistinct:
		hs.AggFn = "count_distinct"
	case AggQuantile:
		hs.AggFn, hs.Level = "quantile", s.Quantile
	case AggAvg:
		hs.AggFn = "avg"
	case AggMax:
		hs.AggFn = "max"
	case AggSum:
		hs.AggFn = "sum"
	default:
		return hs, fmt.Errorf("aggregate %q is not supported", s.Agg)
	}
	if s.Field != "" {
		e, err := fieldSQL(sig, s.Field)
		if err != nil {
			return hs, err
		}
		if s.Agg == AggQuantile || s.Agg == AggAvg || s.Agg == AggMax || s.Agg == AggSum {
			if !isNumericField(s.Field) {
				e = "toFloat64OrZero(" + e + ")"
			}
		}
		hs.ValueExpr = e
	}
	return hs, nil
}

func isNumericField(f string) bool { return f == "span.duration" }

// fieldSQL maps a semantic field to the ClickStack column expression for sig.
func fieldSQL(sig Signal, f string) (string, error) {
	// The vocabulary is dashspec's: a field it refuses is refused here with
	// the same error, so the validator and this renderer cannot disagree.
	if err := dashspec.CheckField(sig, f); err != nil {
		return "", err
	}
	switch {
	case f == FieldService:
		return "ServiceName", nil
	case f == FieldEnv:
		return "ResourceAttributes['deployment.environment.name']", nil
	case strings.HasPrefix(f, "resource.attr."):
		return "ResourceAttributes['" + sqlEscape(strings.TrimPrefix(f, "resource.attr.")) + "']", nil
	}
	switch sig {
	case Traces:
		switch f {
		case "timestamp":
			return "Timestamp", nil
		case "span.name":
			return "SpanName", nil
		case "span.kind":
			return "SpanKind", nil
		case "span.status":
			return "StatusCode", nil
		case "span.status_message":
			return "StatusMessage", nil
		case "span.duration":
			return "Duration/1e6", nil
		}
		if strings.HasPrefix(f, "span.attr.") {
			return "SpanAttributes['" + sqlEscape(strings.TrimPrefix(f, "span.attr.")) + "']", nil
		}
	case Logs:
		switch f {
		case "timestamp":
			return "Timestamp", nil
		case "log.severity":
			return "SeverityText", nil
		case "log.body":
			return "Body", nil
		}
		if strings.HasPrefix(f, "log.attr.") {
			return "LogAttributes['" + sqlEscape(strings.TrimPrefix(f, "log.attr.")) + "']", nil
		}
	case Metrics:
		if strings.HasPrefix(f, "metric.attr.") {
			return "Attributes['" + sqlEscape(strings.TrimPrefix(f, "metric.attr.")) + "']", nil
		}
	}
	return "", fmt.Errorf("field %q is not valid on %s", f, sig)
}

func columnsSQL(sig Signal, cols []string) (string, error) {
	if len(cols) == 0 {
		return "", nil
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		e, err := fieldSQL(sig, c)
		if err != nil {
			return "", err
		}
		out = append(out, e)
	}
	return strings.Join(out, ", "), nil
}

func sqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func quote(s string) string { return "'" + sqlEscape(s) + "'" }

func quoteAll(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = quote(v)
	}
	return strings.Join(q, ", ")
}

// predicatesSQL ANDs preds into one ClickHouse boolean expression.
func predicatesSQL(sig Signal, preds []Predicate) (string, error) {
	parts := make([]string, 0, len(preds))
	for _, p := range preds {
		s, err := predicateSQL(sig, p)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " AND "), nil
}

func predicateSQL(sig Signal, p Predicate) (string, error) {
	e, err := fieldSQL(sig, p.Field)
	if err != nil {
		return "", err
	}
	// Enumerated fields compare case-insensitively across exporters: the
	// ClickHouse exporter writes 'Server' / 'Error', other writers
	// SPAN_KIND_SERVER or lower case.
	switch p.Field {
	case "span.kind":
		if p.Op == OpEq {
			return fmt.Sprintf("%s IN ('%s', 'SPAN_KIND_%s')", e, strings.Title(p.Value), strings.ToUpper(p.Value)), nil //nolint:staticcheck
		}
	case "span.status", "log.severity":
		switch p.Op {
		case OpEq:
			return fmt.Sprintf("lower(%s) = %s", e, quote(strings.ToLower(p.Value))), nil
		case OpNeq:
			return fmt.Sprintf("lower(%s) != %s", e, quote(strings.ToLower(p.Value))), nil
		}
	}
	switch p.Op {
	case OpEq:
		return fmt.Sprintf("%s = %s", e, quote(p.Value)), nil
	case OpNeq:
		return fmt.Sprintf("%s != %s", e, quote(p.Value)), nil
	case OpIn:
		return fmt.Sprintf("%s IN (%s)", e, quoteAll(p.Values)), nil
	case OpNotIn:
		return fmt.Sprintf("%s NOT IN (%s)", e, quoteAll(p.Values)), nil
	case OpPrefix:
		return fmt.Sprintf("startsWith(%s, %s)", e, quote(p.Value)), nil
	case OpMatch:
		return fmt.Sprintf("match(%s, %s)", e, quote(p.Value)), nil
	case OpExists:
		return fmt.Sprintf("%s != ''", e), nil
	case OpGTE:
		return fmt.Sprintf("toFloat64OrZero(%s) >= %s", e, p.Value), nil
	}
	return "", fmt.Errorf("operator %q is not supported", p.Op)
}

// SortedFiles returns rs ordered by file name.
func SortedFiles(rs []Rendered) []Rendered {
	out := append([]Rendered(nil), rs...)
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}
