package obsquery

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// Panel limits.
const (
	MaxPanelSeries     = 10
	MaxPanelPredicates = 20
	MaxPanelValues     = 50
	MaxPanelGroupBy    = 3
	MaxTableRows       = 200
	// DefaultBarRows is how many groups a bar panel with no limit draws.
	DefaultBarRows = dashspec.DefaultBarLimit
	// SparklinePoints is about how many points an ungrouped number panel's
	// sparkline has.
	SparklinePoints = 30
	// MaxHeatmapCells caps the cells one heatmap panel returns: TargetPoints
	// time buckets by about 25 octaves of value at four buckets an octave.
	MaxHeatmapCells  = 12_000
	maxPanelValueLen = 1024
	maxPanelFieldLen = 160
	maxRegexLen      = 256
)

// PanelPredicate is one condition of a dashboard panel (forge obsdash.Predicate).
type PanelPredicate struct {
	Field  string
	Op     string // eq neq in not_in prefix match exists gte
	Value  string
	Values []string
}

// PanelSeries is one aggregate of a panel (forge obsdash.Series).
type PanelSeries struct {
	Label      string
	Agg        string // count count_distinct sum avg max quantile last
	Quantile   float64
	Field      string // a semantic field; the metric NAME for metrics panels
	MetricType string // "sum" | "gauge" | "histogram" (metrics panels)
	Where      []PanelPredicate
}

// Panel is one dashboard tile (forge obsdash.Panel), decoupled from the wire type.
type Panel struct {
	ID      string
	Kind    string // number | line | table | bar | heatmap | sql | funnel | issues | sessions  (search is not executable here)
	Signal  string // traces | logs | metrics; browser kinds: traces or empty; sql: optional
	Series  []PanelSeries
	Ratio   bool
	Where   []PanelPredicate
	GroupBy []string
	// Formula plots one expression over the series (forge dashspec.ParseFormula)
	// instead of the series, on line and number panels.
	Formula string
	// Limit keeps the top Limit groups of a table or bar panel; zero is the
	// kind's default (RowCap).
	Limit int
	// SQL is a sql panel's statement (see sql_panel.go).
	SQL string
	// Funnel is the definition of a funnel panel; HasError narrows a sessions
	// panel to sessions that hit an error. See panel_browser.go.
	Funnel   *PanelFunnel
	HasError bool
}

// PanelMaxRange is the maximum range for a panel of the given signal, or
// ErrInvalid for an unknown signal.
func PanelMaxRange(signal string) (time.Duration, error) {
	switch signal {
	case "traces":
		return MaxRangeTraces, nil
	case "logs":
		return MaxRangeLogs, nil
	case "metrics":
		return MaxRangeMetrics, nil
	}
	return 0, invalid("panel signal must be traces, logs or metrics, got %q", signal)
}

// RowCap is how many groups a table or bar panel keeps: its Limit, else the
// kind's default.
func (p Panel) RowCap() int {
	switch {
	case p.Limit > 0:
		return p.Limit
	case p.Kind == "bar":
		return DefaultBarRows
	}
	return MaxTableRows
}

// ParsedFormula is the panel's Formula, parsed against its series; nil when
// the panel has none.
func (p Panel) ParsedFormula() (*dashspec.Formula, error) {
	if p.Formula == "" {
		return nil, nil
	}
	f, err := dashspec.ParseFormula(p.Formula, len(p.Series))
	if err != nil {
		return nil, invalid("formula: %v", err)
	}
	return f, nil
}

// Validate checks everything about a panel that does not need the scope.
func (p Panel) Validate() error {
	if IsBrowserKind(p.Kind) {
		return p.validateBrowser()
	}
	if p.Kind == "sql" {
		return p.validateSQL()
	}
	if p.SQL != "" {
		return invalid("only a sql panel takes sql")
	}
	if _, err := PanelMaxRange(p.Signal); err != nil {
		return err
	}
	if p.Funnel != nil || p.HasError {
		return invalid("funnel and has_error belong to funnel and sessions panels")
	}
	switch p.Kind {
	case "line", "number", "table", "bar", "heatmap":
	case "search":
		return invalid("search panels are read with SearchLogs or ListTraces, not QueryPanel")
	default:
		return invalid("panel kind must be line, number, table, bar, heatmap, sql, funnel, issues or sessions, got %q", p.Kind)
	}
	if p.Limit != 0 && p.Kind != "table" && p.Kind != "bar" {
		return invalid("only a table or bar panel takes a limit")
	}
	if p.Limit < 0 || p.Limit > MaxTableRows {
		return invalid("limit must be 1 to %d", MaxTableRows)
	}
	if p.Formula != "" {
		if p.Kind != "line" && p.Kind != "number" {
			return invalid("a formula applies to line and number panels")
		}
		if p.Ratio {
			return invalid("a panel plots a formula or a ratio, not both")
		}
	}
	if p.Kind == "bar" && len(p.GroupBy) == 0 {
		return invalid("a bar panel needs a group_by")
	}
	if p.Kind == "heatmap" {
		if err := p.validateHeatmap(); err != nil {
			return err
		}
	}
	if len(p.Series) == 0 || len(p.Series) > MaxPanelSeries {
		return invalid("a panel needs 1-%d series", MaxPanelSeries)
	}
	if len(p.GroupBy) > MaxPanelGroupBy {
		return invalid("at most %d group_by fields", MaxPanelGroupBy)
	}
	if p.Ratio && len(p.Series) != 2 {
		return invalid("a ratio panel needs exactly 2 series")
	}
	if p.Ratio && p.Kind != "line" && p.Kind != "number" {
		return invalid("a ratio applies to line and number panels")
	}
	if len(p.Where) > MaxPanelPredicates {
		return invalid("at most %d panel predicates", MaxPanelPredicates)
	}
	for _, s := range p.Series {
		if len(s.Where) > MaxPanelPredicates {
			return invalid("at most %d series predicates", MaxPanelPredicates)
		}
		switch s.Agg {
		case "count", "count_distinct", "sum", "avg", "max", "quantile", "last":
		case "rate", "increase", "min":
			if p.Signal != "metrics" {
				return invalid("aggregation %q is defined for metrics only", s.Agg)
			}
		default:
			return invalid("unknown aggregation %q", s.Agg)
		}
		if s.Agg == "quantile" && !(s.Quantile > 0 && s.Quantile <= 1) {
			return invalid("quantile must be in (0, 1]")
		}
		if s.Agg != "count" && s.Field == "" {
			return invalid("series %q: aggregation %s needs a field", s.Label, s.Agg)
		}
		if len(s.Field) > maxPanelFieldLen {
			return invalid("series field is too long")
		}
	}
	if _, err := p.ParsedFormula(); err != nil {
		return err
	}
	return nil
}

// validateHeatmap: one count series, bucketed by its field, over traces or logs.
func (p Panel) validateHeatmap() error {
	if p.Signal == "metrics" {
		return invalid("a heatmap buckets individual events: its signal is traces or logs")
	}
	if len(p.Series) != 1 || p.Series[0].Agg != "count" || p.Series[0].Field == "" {
		return invalid("a heatmap panel takes one series: agg count, with the numeric value to bucket in field")
	}
	if len(p.GroupBy) > 0 || p.Ratio {
		return invalid("a heatmap panel takes no group_by or ratio")
	}
	return nil
}

// ── Field resolution ────────────────────────────────────────────────────

type fieldRef struct {
	expr    string // a String (or, if num, Float64) expression
	num     bool
	mapCol  string // set for attribute-backed fields: for `exists`
	keyExpr string
	special string // "sev" | "kind" | "status"
}

func attrKey(field, prefix string) (string, error) {
	k := strings.TrimPrefix(field, prefix)
	if k == "" || len(k) > 128 {
		return "", invalid("field %q needs an attribute key", field)
	}
	return k, nil
}

func (b *builder) mapRef(col, key string) fieldRef {
	k := b.str(key)
	return fieldRef{expr: col + "[" + k + "]", mapCol: col, keyExpr: k}
}

func (b *builder) resolve(signal, field string) (fieldRef, error) {
	if table, ok := strings.CutPrefix(signal, browserSig); ok {
		return b.resolveBrowser(table, field)
	}
	switch {
	case field == "service.name":
		return fieldRef{expr: "ServiceName"}, nil
	case field == "deployment.environment.name":
		return b.mapRef("ResourceAttributes", field), nil
	case strings.HasPrefix(field, "resource.attr."):
		k, err := attrKey(field, "resource.attr.")
		if err != nil {
			return fieldRef{}, err
		}
		return b.mapRef("ResourceAttributes", k), nil
	}
	switch signal {
	case "traces":
		switch {
		case field == "span.name":
			return fieldRef{expr: "SpanName"}, nil
		case field == "span.kind":
			return fieldRef{expr: "lower(replaceOne(SpanKind, 'SPAN_KIND_', ''))", special: "kind"}, nil
		case field == "span.status":
			return fieldRef{expr: "lower(replaceOne(StatusCode, 'STATUS_CODE_', ''))", special: "status"}, nil
		case field == "span.status_message":
			return fieldRef{expr: "StatusMessage"}, nil
		case field == "span.duration":
			return fieldRef{expr: "(Duration / 1000000)", num: true}, nil
		case strings.HasPrefix(field, "span.attr."):
			k, err := attrKey(field, "span.attr.")
			if err != nil {
				return fieldRef{}, err
			}
			return b.mapRef("SpanAttributes", k), nil
		}
	case "logs":
		switch {
		case field == "log.severity":
			return fieldRef{expr: "upper(SeverityText)", special: "sev"}, nil
		case field == "log.body":
			return fieldRef{expr: "Body"}, nil
		case strings.HasPrefix(field, "log.attr."):
			k, err := attrKey(field, "log.attr.")
			if err != nil {
				return fieldRef{}, err
			}
			return b.mapRef("LogAttributes", k), nil
		}
	case "metrics":
		if strings.HasPrefix(field, "metric.attr.") {
			k, err := attrKey(field, "metric.attr.")
			if err != nil {
				return fieldRef{}, err
			}
			return b.mapRef("Attributes", k), nil
		}
	}
	return fieldRef{}, invalid("unknown field %q for a %s panel", field, signal)
}

func (r fieldRef) numeric() string {
	if r.num {
		return r.expr
	}
	return "toFloat64OrNull(" + r.expr + ")"
}

func (b *builder) strList(vals []string, lower bool) (string, error) {
	if len(vals) == 0 || len(vals) > MaxPanelValues {
		return "", invalid("an in/not_in predicate needs 1-%d values", MaxPanelValues)
	}
	ps := make([]string, len(vals))
	for i, v := range vals {
		if len(v) > maxPanelValueLen {
			return "", invalid("predicate value is too long")
		}
		if lower {
			v = strings.ToLower(v)
		}
		ps[i] = b.str(v)
	}
	return strings.Join(ps, ", "), nil
}

// cond compiles one predicate over a signal's columns.
func (b *builder) cond(signal string, p PanelPredicate) (string, error) {
	ref, err := b.resolve(signal, p.Field)
	if err != nil {
		return "", err
	}
	if len(p.Value) > maxPanelValueLen {
		return "", invalid("predicate value is too long")
	}
	if ref.special != "" {
		return b.specialCond(ref, p)
	}

	switch p.Op {
	case "exists":
		if ref.mapCol != "" {
			return "mapContains(" + ref.mapCol + ", " + ref.keyExpr + ")", nil
		}
		if ref.num {
			return "1", nil
		}
		return ref.expr + " != ''", nil
	case "gte":
		f, err := strconv.ParseFloat(p.Value, 64)
		if err != nil {
			return "", invalid("gte needs a number, got %q", p.Value)
		}
		return ref.numeric() + " >= " + b.f64(f), nil
	}
	if ref.num {
		f, err := strconv.ParseFloat(p.Value, 64)
		if err != nil || (p.Op != "eq" && p.Op != "neq") {
			return "", invalid("%s supports eq, neq and gte with a number", p.Field)
		}
		op := "="
		if p.Op == "neq" {
			op = "!="
		}
		return ref.expr + " " + op + " " + b.f64(f), nil
	}
	switch p.Op {
	case "eq":
		return ref.expr + " = " + b.str(p.Value), nil
	case "neq":
		return ref.expr + " != " + b.str(p.Value), nil
	case "in", "not_in":
		l, err := b.strList(p.Values, false)
		if err != nil {
			return "", err
		}
		neg := ""
		if p.Op == "not_in" {
			neg = "NOT "
		}
		return ref.expr + " " + neg + "IN (" + l + ")", nil
	case "prefix":
		return "startsWith(" + ref.expr + ", " + b.str(p.Value) + ")", nil
	case "match":
		if len(p.Value) > maxRegexLen {
			return "", invalid("regular expression is longer than %d characters", maxRegexLen)
		}
		return "match(" + ref.expr + ", " + b.str(p.Value) + ")", nil
	}
	return "", invalid("unknown predicate operator %q", p.Op)
}

func (b *builder) conds(signal string, ps []PanelPredicate) ([]string, error) {
	var out []string
	for _, p := range ps {
		c, err := b.cond(signal, p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ── Traces / logs panels ────────────────────────────────────────────────

// PanelQuery is a QueryPanel request for a traces or logs panel.
type PanelQuery struct {
	Scope   Scope
	Panel   Panel
	Filters []PanelPredicate
	// StepSeconds is the resolved bucket width for line panels.
	StepSeconds int64
}

// Shape describes the columns of the compiled statement.
type PanelShape struct {
	Bucketed bool // first column is the bucket (line panels, sparklines)
	// Total: after the bucket, a UInt8 that is 1 on the row aggregating the
	// whole range (an ungrouped number panel's value; the others are its
	// sparkline).
	Total  bool
	Groups int // length of the labels array
	Series int // trailing Float64 columns
	// Rows is how many rows a table or bar panel keeps; the statement returns
	// one more when there were more.
	Rows int
	// Heatmap: the columns are bucket, value bucket (Int32), count.
	Heatmap bool
}

// Heatmap value buckets are quarter powers of two: bucket i holds values in
// [2^(i/4), 2^((i+1)/4)). HeatmapZeroBucket holds zero and negative values.
const HeatmapZeroBucket = math.MinInt32

// HeatmapBounds is the value range of heatmap bucket i.
func HeatmapBounds(i int32) (low, high float64) {
	if i == HeatmapZeroBucket {
		return 0, 0
	}
	return math.Exp2(float64(i) / 4), math.Exp2(float64(i+1) / 4)
}

// SparklineStep is the bucket width of an ungrouped number panel's sparkline
// over a range.
func SparklineStep(rng time.Duration) int64 {
	s := int64(math.Ceil(rng.Seconds() / SparklinePoints))
	if s < 1 {
		return 1
	}
	return s
}

func (q PanelQuery) aggExpr(b *builder, s PanelSeries) (string, error) {
	sc, err := b.conds(q.Panel.Signal, s.Where)
	if err != nil {
		return "", err
	}
	c := "1"
	if len(sc) > 0 {
		c = strings.Join(sc, " AND ")
	}
	var ref fieldRef
	if s.Agg != "count" {
		if ref, err = b.resolve(q.Panel.Signal, s.Field); err != nil {
			return "", err
		}
	}
	var e string
	// A traces panel counts REAL spans: each stored one is weighted by how many
	// it stands for (weight.go). Logs are not sampled.
	if q.Panel.Signal == "traces" {
		w := spanWeightExpr
		switch s.Agg {
		case "count":
			e = "sumIf(" + w + ", " + c + ")"
		case "count_distinct":
			e = "uniqExactIf(toString(" + ref.expr + "), " + c + ")"
		case "sum":
			e = "sumIf(" + ref.numeric() + " * " + w + ", " + c + ")"
		case "avg":
			nn := c + " AND isNotNull(" + ref.numeric() + ")"
			e = "sumIf(" + ref.numeric() + " * " + w + ", " + nn + ") / nullIf(sumIf(" + w + ", " + nn + "), 0)"
		case "max":
			e = "maxIf(" + ref.numeric() + ", " + c + ")"
		case "quantile":
			e = "quantileExactWeightedIf(" + strconv.FormatFloat(s.Quantile, 'f', -1, 64) + ")(" + ref.numeric() + ", " + spanWeightInt + ", " + c + ")"
		case "last":
			e = "argMaxIf(" + ref.numeric() + ", Timestamp, " + c + ")"
		}
		return "ifNull(toFloat64(" + e + "), 0)", nil
	}
	switch s.Agg {
	case "count":
		e = "countIf(" + c + ")"
	case "count_distinct":
		e = "uniqExactIf(toString(" + ref.expr + "), " + c + ")"
	case "sum":
		e = "sumIf(" + ref.numeric() + ", " + c + ")"
	case "avg":
		e = "avgIf(" + ref.numeric() + ", " + c + ")"
	case "max":
		e = "maxIf(" + ref.numeric() + ", " + c + ")"
	case "quantile":
		e = "quantileIf(" + strconv.FormatFloat(s.Quantile, 'f', -1, 64) + ")(" + ref.numeric() + ", " + c + ")"
	case "last":
		e = "argMaxIf(" + ref.numeric() + ", Timestamp, " + c + ")"
	}
	return "ifNull(toFloat64(" + e + "), 0)", nil
}

// Compile builds the statement for a traces or logs panel.
//
//	line:    bucket, labels (Array(String)), v0..vN
//	number:  labels, v0..vN; ungrouped with StepSeconds set (a sparkline):
//	         bucket, total (UInt8), labels, v0..vN, the total row last
//	table:   labels, v0..vN   (heaviest first, at most RowCap+1 rows)
//	bar:     as table
//	heatmap: bucket, vb (Int32 value bucket), n (Float64)
func (q PanelQuery) Compile() (Stmt, PanelShape, error) {
	var shape PanelShape
	if err := q.Panel.Validate(); err != nil {
		return Stmt{}, shape, err
	}
	if IsBrowserKind(q.Panel.Kind) {
		return q.compileBrowser()
	}
	if q.Panel.Signal == "metrics" {
		return Stmt{}, shape, invalid("internal: metrics panels are compiled per series")
	}
	max, _ := PanelMaxRange(q.Panel.Signal)
	if err := q.Scope.validate(max); err != nil {
		return Stmt{}, shape, err
	}
	if len(q.Filters) > MaxPanelPredicates {
		return Stmt{}, shape, invalid("at most %d filters", MaxPanelPredicates)
	}
	b := newBuilder(q.Scope)
	table := "otel_traces"
	if q.Panel.Signal == "logs" {
		table = "otel_logs"
	}
	sig, _ := SignalOf(q.Panel.Signal)
	where := []string{b.rangeCond("Timestamp", q.Scope)}
	sc, err := b.scopeConds(sig, q.Scope)
	if err != nil {
		return Stmt{}, shape, err
	}
	where = append(where, sc...)
	pc, err := b.conds(q.Panel.Signal, q.Panel.Where)
	if err != nil {
		return Stmt{}, shape, err
	}
	fc, err := b.conds(q.Panel.Signal, q.Filters)
	if err != nil {
		return Stmt{}, shape, err
	}
	where = append(append(where, pc...), fc...)

	if q.Panel.Kind == "heatmap" {
		return q.compileHeatmap(b, table, where)
	}

	var gSel, gNames []string
	for i, g := range q.Panel.GroupBy {
		expr, err := b.panelGroupExpr(sig, q.Panel.Signal, g)
		if err != nil {
			return Stmt{}, shape, err
		}
		n := "g" + strconv.Itoa(i)
		gSel = append(gSel, "toString("+expr+") AS "+n)
		gNames = append(gNames, n)
	}
	labels := "CAST([], 'Array(String)')"
	if len(gNames) > 0 {
		labels = "[" + strings.Join(gNames, ", ") + "]"
	}
	var aggs []string
	for i, s := range q.Panel.Series {
		a, err := q.aggExpr(b, s)
		if err != nil {
			return Stmt{}, shape, err
		}
		aggs = append(aggs, a+" AS v"+strconv.Itoa(i))
	}
	shape.Groups, shape.Series = len(gNames), len(aggs)
	from := "FROM " + b.table(q.Scope, table) + "\n" + whereClause(where)
	groupBy := strings.Join(gNames, ", ")

	var sql string
	switch {
	case q.Panel.Kind == "line":
		if q.StepSeconds < 1 {
			return Stmt{}, shape, invalid("step must be at least 1 second")
		}
		if int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/q.StepSeconds > MaxPointsPerSeries {
			return Stmt{}, shape, invalid("step %ds would return more than %d points per series", q.StepSeconds, MaxPointsPerSeries)
		}
		shape.Bucketed = true
		bucket := "toStartOfInterval(toDateTime(Timestamp), toIntervalSecond(" + b.u32(uint32(q.StepSeconds)) + "))"
		sel := append([]string{bucket + " AS bucket"}, gSel...)
		gb := "bucket"
		if groupBy != "" {
			gb += ", " + groupBy
		}
		sql = "SELECT bucket, " + labels + " AS labels, " + strings.Join(aggs, ", ") + " FROM (\n" +
			"  SELECT " + strings.Join(sel, ", ") + ", " + rawCols(q.Panel) + "\n  " + from + "\n) GROUP BY " + gb +
			" ORDER BY " + gb + " LIMIT " + strconv.Itoa(MaxMetricRows)
	case q.Panel.Kind == "number" && len(gNames) == 0 && q.StepSeconds > 0:
		if sql, err = q.sparklineSQL(b, labels, aggs, from); err != nil {
			return Stmt{}, shape, err
		}
		shape.Bucketed, shape.Total = true, true
	case q.Panel.Kind == "number" && len(gNames) == 0:
		sql = "SELECT " + labels + " AS labels, " + strings.Join(aggs, ", ") + "\n" + from
	default: // table, bar, or a grouped number
		limit := MaxTableRows
		if q.Panel.Kind == "table" || q.Panel.Kind == "bar" {
			limit = q.Panel.RowCap()
		}
		shape.Rows = limit
		sel := append(append([]string{}, gSel...), "*")
		sql = "SELECT " + labels + " AS labels, " + strings.Join(aggs, ", ") + " FROM (\n" +
			"  SELECT " + strings.Join(sel, ", ") + "\n  " + from + "\n) GROUP BY " + groupBy +
			" ORDER BY v0 DESC LIMIT " + strconv.Itoa(limit+1)
	}
	return b.stmt(sql), shape, nil
}

// sparklineSQL is an ungrouped number panel's value and its sparkline in one
// statement: ROLLUP adds the row over the whole range (grouping(bucket) = 1),
// whose aggregates are exactly the ungrouped number's, quantiles included.
func (q PanelQuery) sparklineSQL(b *builder, labels string, aggs []string, from string) (string, error) {
	if int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/q.StepSeconds > MaxPointsPerSeries {
		return "", invalid("step %ds would return more than %d points per series", q.StepSeconds, MaxPointsPerSeries)
	}
	bucket := "toStartOfInterval(toDateTime(Timestamp), toIntervalSecond(" + b.u32(uint32(q.StepSeconds)) + "))"
	return "SELECT bucket, toUInt8(grouping(bucket)) AS total, " + labels + " AS labels, " + strings.Join(aggs, ", ") + " FROM (\n" +
		"  SELECT " + bucket + " AS bucket, " + rawCols(q.Panel) + "\n  " + from + "\n) GROUP BY ROLLUP(bucket)" +
		" ORDER BY total, bucket LIMIT " + strconv.Itoa(MaxPointsPerSeries+2), nil
}

// compileHeatmap counts the panel's events per (time bucket, value bucket).
// Value buckets are quarter powers of two (HeatmapBounds); a value that is not
// a finite number is left out, zero and below land in HeatmapZeroBucket. A
// traces heatmap counts real spans, each weighted like every traces count.
func (q PanelQuery) compileHeatmap(b *builder, table string, where []string) (Stmt, PanelShape, error) {
	shape := PanelShape{Bucketed: true, Heatmap: true}
	if q.StepSeconds < 1 {
		return Stmt{}, shape, invalid("step must be at least 1 second")
	}
	if int64(q.Scope.End.Sub(q.Scope.Start).Seconds())/q.StepSeconds > MaxPointsPerSeries {
		return Stmt{}, shape, invalid("step %ds would return more than %d points per series", q.StepSeconds, MaxPointsPerSeries)
	}
	s := q.Panel.Series[0]
	ref, err := b.resolve(q.Panel.Signal, s.Field)
	if err != nil {
		return Stmt{}, shape, err
	}
	sc, err := b.conds(q.Panel.Signal, s.Where)
	if err != nil {
		return Stmt{}, shape, err
	}
	where = append(where, sc...)
	w := "1.0"
	if q.Panel.Signal == "traces" {
		w = spanWeightExpr
	}
	bucket := "toStartOfInterval(toDateTime(Timestamp), toIntervalSecond(" + b.u32(uint32(q.StepSeconds)) + "))"
	sql := "SELECT bucket, if(v > 0, toInt32(floor(log2(v) * 4)), toInt32(" + strconv.Itoa(HeatmapZeroBucket) + ")) AS vb, " +
		"toFloat64(sum(w)) AS n FROM (\n" +
		"  SELECT " + bucket + " AS bucket, toFloat64(" + ref.numeric() + ") AS v, " + w + " AS w\n" +
		"  FROM " + b.table(q.Scope, table) + "\n" + whereClause(where) + "\n" +
		") WHERE isNotNull(v) AND isFinite(v) GROUP BY bucket, vb ORDER BY bucket, vb LIMIT " + strconv.Itoa(MaxHeatmapCells+1)
	return b.stmt(sql), shape, nil
}

func whereClause(conds []string) string { return where(conds) }

// panelGroupExpr resolves a panel group-by: the panel field vocabulary first
// (span.name, log.severity, resource.attr.<k>, ...), then the allowlisted
// attribute keys (reliant.env.id, k8s.workload.name, ...).
func (b *builder) panelGroupExpr(sig Signal, signal, field string) (string, error) {
	if Allowed(field) {
		e, _, err := b.groupExpr(sig, field)
		return e, err
	}
	ref, err := b.resolve(signal, field)
	if err != nil {
		return "", err
	}
	return ref.expr, nil
}

// rawCols lists the columns the outer aggregation reads, so the inner SELECT
// stays narrow for line panels.
func rawCols(p Panel) string {
	if p.Signal == "logs" {
		return "Timestamp, SeverityText, Body, ServiceName, LogAttributes, ResourceAttributes"
	}
	return "Timestamp, SpanName, SpanKind, StatusCode, StatusMessage, Duration, ServiceName, SpanAttributes, ResourceAttributes, TraceState"
}

// specialCond compiles predicates on the normalized fields (severity, span
// kind, span status), whose stored spellings vary.
func (b *builder) specialCond(ref fieldRef, p PanelPredicate) (string, error) {
	switch ref.special {
	case "sev":
		vals := p.Values
		if p.Op == "eq" || p.Op == "neq" || p.Op == "gte" {
			vals = []string{p.Value}
		}
		if p.Op == "gte" {
			c, err := b.severityCond(">="+p.Value, 0)
			return c, err
		}
		var spellings []string
		for _, v := range vals {
			lvl, ok := severityLevel(v)
			if !ok {
				return "", invalid("unknown severity %q", v)
			}
			spellings = append(spellings, severityNames[lvl]...)
		}
		in, err := b.strList(spellings, false)
		if err != nil {
			return "", err
		}
		switch p.Op {
		case "eq", "in":
			return ref.expr + " IN (" + in + ")", nil
		case "neq", "not_in":
			return ref.expr + " NOT IN (" + in + ")", nil
		}
		return "", invalid("log.severity supports eq, neq, in, not_in and gte")
	case "kind", "status":
		lower := strings.ToLower
		switch p.Op {
		case "eq":
			return ref.expr + " = " + b.str(lower(p.Value)), nil
		case "neq":
			return ref.expr + " != " + b.str(lower(p.Value)), nil
		case "in", "not_in":
			l, err := b.strList(p.Values, true)
			if err != nil {
				return "", err
			}
			neg := ""
			if p.Op == "not_in" {
				neg = "NOT "
			}
			return ref.expr + " " + neg + "IN (" + l + ")", nil
		}
		return "", invalid("%s supports eq, neq, in and not_in", p.Field)
	}
	return "", invalid("unsupported predicate on %s", p.Field)
}
