package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// newObsMetricsGroupCmd is `forge obs metrics`: list and query what the
// stack collected, plus `declared`, which prints what a service's registry
// declares.
func newObsMetricsGroupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "List and query metrics; print the declared schema",
	}
	cmd.AddCommand(newObsMetricsListCmd(), newObsMetricsQueryCmd(), newObsMetricsDescribeCmd(), newObsMetricsExemplarsCmd(), newObsMetricsDeclaredCmd())
	return cmdutil.StrictGroup(cmd)
}

// ── metrics list ────────────────────────────────────────────────────────

const schemaObsMetricsList = "forge.obs.metrics_list/v1"

var obsMetricsListSchema = obsout.Schema{
	Name: schemaObsMetricsList,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode",
		"metrics", "metrics.name", "metrics.type", "metrics.unit", "metrics.description",
		"metrics.monotonic", "metrics.temporality", "metrics.services", "metrics.service_count", "metrics.last_seen",
		"metrics.series_estimate", "metrics.queryable", "metrics.builtin", "metrics.declared", "metrics.seen", "metrics.stability",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsMetricInfoDoc struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Unit        string `json:"unit,omitempty"`
	Description string `json:"description,omitempty"`
	// Monotonic and Temporality read a sum (and a histogram's temporality).
	Monotonic   bool     `json:"monotonic,omitempty"`
	Temporality string   `json:"temporality,omitempty"`
	Services    []string `json:"services,omitempty"`
	// ServiceCount is how many services emit it (Services lists up to 10).
	ServiceCount   int        `json:"service_count,omitempty"`
	LastSeen       *time.Time `json:"last_seen,omitempty"`
	SeriesEstimate int64      `json:"series_estimate,omitempty"`
	// Queryable is false for a kind the evaluator cannot run yet.
	Queryable bool `json:"queryable"`
	Builtin   bool `json:"builtin,omitempty"`
	// Declared: the release's metrics schema lists it. Seen: it has data in
	// the window; declared and not seen is a typo or a metric never called.
	Declared  bool   `json:"declared,omitempty"`
	Seen      bool   `json:"seen"`
	Stability string `json:"stability,omitempty"`
}

func metricInfoDoc(m obsbackend.MetricInfo) obsMetricInfoDoc {
	d := obsMetricInfoDoc{
		Name: m.Name, Type: m.Type, Unit: m.Unit, Description: m.Description, Monotonic: m.Monotonic,
		Temporality: m.Temporality, Services: m.Services, ServiceCount: m.ServiceCount, SeriesEstimate: m.SeriesEstimate,
		Queryable: m.Queryable, Builtin: m.Builtin, Declared: m.Declared, Seen: m.Seen, Stability: m.Stability,
	}
	if !m.LastSeen.IsZero() {
		t := m.LastSeen.UTC()
		d.LastSeen = &t
	}
	return d
}

type obsMetricsListDoc struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Metrics    []obsMetricInfoDoc     `json:"metrics"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsMetricsListOpts struct {
	obsCommon
	contains string
}

func newObsMetricsListCmd() *cobra.Command {
	var o obsMetricsListOpts
	cmd := &cobra.Command{
		Use:   "list [env]",
		Short: "The metrics collected in a window",
		Long: `List the metrics the env's telemetry carries in the window: type (and, for a
counter, whether it is monotonic; for a sum or histogram, its temporality), unit,
the services that emit it and an estimate of its series. A metric stored as more
than one kind is listed once per kind. [env] defaults to dev.

Locally, the metrics the project declares (bin/observability/metrics-schema.json,
written by forge build) are merged in: "declared" says the schema lists it, and
"seen": false is a declared metric with no data in the window.

  ` + Name() + ` obs metrics list dev --contains rpc
  ` + Name() + ` obs metrics list dev --service api --json --fields metrics.name

` + obsMetricsListSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsMetricsList(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, time.Hour, obsquery.ListMetricsDefault)
	cmd.Flags().StringVar(&o.contains, "contains", "", "only metrics whose name contains this (case-insensitive)")
	return cmd
}

func runObsMetricsList(ctx context.Context, d obsDeps, env string, o obsMetricsListOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsMetricsListSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsMetricsList, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsMetricsList, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "metrics list")
	if err != nil {
		return fail(err)
	}
	res, err := b.MetricsList(ctx, obsbackend.MetricsListRequest{Narrow: n, NameContains: o.contains, Limit: o.out.Limit})
	if err != nil {
		return fail(err)
	}
	doc := obsMetricsListDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Metrics: []obsMetricInfoDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, m := range res.Metrics {
		doc.Metrics = append(doc.Metrics, metricInfoDoc(m))
	}
	doc.Page = obsout.Page{Truncated: res.Truncated}
	if first, ok := firstSeenMetric(res.Metrics); ok {
		doc.Suggested = obsout.Suggest(
			obsout.Command("forge", "obs", "metrics", "describe", env, first.Name),
			obsout.Command("forge", "obs", "metrics", "query", env, first.Name))
	}
	doc.Stamp(schemaObsMetricsList, nil)
	if done, err := o.out.Emit(d.Stdout, obsMetricsListSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	rows := make([][]string, len(res.Metrics))
	for i, m := range res.Metrics {
		rows[i] = []string{m.Name, metricKindText(m), m.Unit, strconv.FormatInt(m.SeriesEstimate, 10), metricSeenText(m), m.Description}
	}
	obsout.Table(d.Stdout, []string{"METRIC", "TYPE", "UNIT", "SERIES", "LAST SEEN", "DESCRIPTION"}, rows, 60)
	if res.Truncated {
		fmt.Fprintf(d.Stdout, "\n(more than %d; narrow with --contains or raise --limit)\n", o.out.Limit)
	}
	return nil
}

// ── metrics query ───────────────────────────────────────────────────────

const schemaObsMetricsQuery = "forge.obs.metrics_query/v2"

var obsMetricsQuerySchema = obsout.Schema{
	Name: schemaObsMetricsQuery,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "panel", "step_seconds",
		"meta", "meta.label", "meta.metric", "meta.type", "meta.monotonic", "meta.temporality", "meta.agg", "meta.quantile", "meta.value_unit",
		"series", "series.label", "series.labels.*", "series.reduced",
		"series.points", "series.points.time", "series.points.value", "series.points.partial", "series.points.null",
		"series_total", "warnings",
		"compare", "compare.offset_seconds", "compare.against", "compare.from", "compare.to", "compare_series",
		"compare_series.label", "compare_series.labels.*", "compare_series.reduced",
		"compare_series.points", "compare_series.points.time", "compare_series.points.value",
		"compare_series.points.partial", "compare_series.points.null",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsPointDoc struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
	// Partial: the bucket was still filling when it was read. Null: no value
	// (a gap, not a zero); Value is 0 and must be ignored.
	Partial bool `json:"partial,omitempty"`
	Null    bool `json:"null,omitempty"`
}

type obsSeriesDoc struct {
	// Label is the panel series this is a group of (A, B, ...), or "formula"
	// or "ratio".
	Label  string            `json:"label"`
	Labels map[string]string `json:"labels,omitempty"`
	// Reduced is the value over the whole range (number, table and bar
	// panels); absent for a line, and null when the reduction has no value.
	Reduced *float64      `json:"reduced,omitempty"`
	Points  []obsPointDoc `json:"points"`
}

type obsMetaDoc struct {
	Label       string  `json:"label"`
	Metric      string  `json:"metric"`
	Type        string  `json:"type,omitempty"`
	Monotonic   bool    `json:"monotonic,omitempty"`
	Temporality string  `json:"temporality,omitempty"`
	Agg         string  `json:"agg"`
	Quantile    float64 `json:"quantile,omitempty"`
	ValueUnit   string  `json:"value_unit,omitempty"`
}

type obsCompareDoc struct {
	OffsetSeconds int64     `json:"offset_seconds"`
	Against       string    `json:"against"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
}

type obsMetricsQueryDoc struct {
	obsout.Head
	Scope       obsout.Scope   `json:"scope"`
	QueryMode   string         `json:"query_mode"`
	Panel       dashspec.Panel `json:"panel"`
	StepSeconds int64          `json:"step_seconds,omitempty"`
	Meta        []obsMetaDoc   `json:"meta"`
	Series      []obsSeriesDoc `json:"series"`
	SeriesTotal int            `json:"series_total"`
	Warnings    []string       `json:"warnings"`
	// Compare and CompareSeries are present only with --compare: the same
	// panel over the range shifted back, its points moved forward to overlay.
	Compare       *obsCompareDoc         `json:"compare,omitempty"`
	CompareSeries []obsSeriesDoc         `json:"compare_series,omitempty"`
	Statements    []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsMetricsQueryOpts struct {
	obsCommon
	metricsPanelFlags
	step     time.Duration
	compare  string
	panelOut string
	limitSet bool
}

func newObsMetricsQueryCmd() *cobra.Command {
	var o obsMetricsQueryOpts
	cmd := &cobra.Command{
		Use:   "query [env] <metric>",
		Short: "A metrics panel: one or more metrics over time, as the console's explorer runs it",
		Long: `Run the query the metrics explorer builds: a dashboard panel over metrics. The
positional metric is series A; --query adds more. Locally and on a hosted env the
same evaluator runs it, so the same panel over the same data returns the same
numbers.

  ` + Name() + ` obs metrics query dev rpc.server.duration --agg p95 --group-by rpc.method
  ` + Name() + ` obs metrics query dev rpc.server.calls --since 6h --group-by service.name
  ` + Name() + ` obs metrics query prod rpc.server.calls --agg rate --where A:status=error \
      --query B=rpc.server.calls:rate --formula 'A / B * 100' --kind number --compare 24h
  ` + Name() + ` obs metrics query dev process.runtime.go.mem.heap_alloc --explain

The aggregation of a series defaults to its kind's: rate for a counter, sum for an
up-down counter, avg for a gauge, p95 for a histogram. --agg is one of
` + obsbackend.AggNames + `.

  --query NAME=metric[:agg[:q]]   one more series (repeatable); NAME is its label (B, C, ...)
  --where NAME:key=value          narrow series NAME alone; key!=value excludes (repeatable)
  --filter key=value              narrow every series (repeatable); key!=value excludes
  --group-by KEY                  split by an attribute (repeatable, at most 3)
  --formula EXPR                  plot an expression of the series: 'B / A * 100'
  --kind line|number|table|bar    the shape: a line is a time series; number, table and
                                  bar reduce each series over the whole range
  --top N                         a table or bar keeps the top N groups
  --compare DURATION|prev-release also read the same panel over the range shifted back by
                                  DURATION, or ending when the env's live release was promoted
  --panel-out FILE|-              write the dashboard panel these flags build and run nothing;
                                  ` + "`" + Name() + ` obs query --panel FILE` + "`" + ` runs it, and a dashboard holds it

A key is an attribute of the metric (rpc.method), or service.name, or a
resource.attr.<key> / metric.attr.<key> spelling. A formula names series by
position: A is the first, B the second. A missing point counts as 0 in a formula
over counters and is a gap over anything else.

` + obsMetricsQuerySchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, metric := "dev", args[0]
			if len(args) == 2 {
				env, metric = args[0], args[1]
			}
			o.limitSet = cmd.Flags().Changed("limit")
			return runObsMetricsQuery(cmd.Context(), defaultObsDeps(cmd), env, metric, o)
		},
	}
	o.register(cmd, time.Hour, obsquery.MaxMetricSeries)
	f := cmd.Flags()
	f.StringVar(&o.agg, "agg", "", "aggregation of series A: "+obsbackend.AggNames+" (default: by the metric's kind)")
	f.Float64Var(&o.quantile, "quantile", 0, "the level of --agg quantile, in (0, 1]")
	f.StringArrayVar(&o.queries, "query", nil, "one more series NAME=metric[:agg[:q]] (repeatable)")
	f.StringArrayVar(&o.where, "where", nil, "narrow one series NAME:key=value or NAME:key!=value (repeatable)")
	f.StringArrayVar(&o.groupBy, "group-by", nil, "split by this attribute key (repeatable, at most 3)")
	f.StringVar(&o.formula, "formula", "", "plot an expression of the series, e.g. 'B / A * 100'")
	f.StringVar(&o.kind, "kind", "", "line (default), number, table or bar")
	f.IntVar(&o.top, "top", 0, "a table or bar keeps the top N groups")
	f.StringVar(&o.compare, "compare", "", "also read the range shifted back by this duration, or prev-release")
	f.StringVar(&o.panelOut, "panel-out", "", "write the dashboard panel built from the flags to FILE (- is stdout) and run nothing")
	f.DurationVar(&o.step, "step", 0, "bucket width (default: about 120 points per series, never below the export interval)")
	cmd.Flag("limit").Usage = "most series to return per panel series"
	cmd.Flag("filter").Usage = "narrow every series: key=value or key!=value (repeatable)"
	return cmd
}

// compareOffset resolves --compare: a duration, or the time since the env's
// live release was promoted (the window just before it, the same length).
func compareOffset(ctx context.Context, d obsDeps, env, flag string, n obsbackend.Narrow) (time.Duration, error) {
	if flag == "" {
		return 0, nil
	}
	if flag != "prev-release" {
		off, err := parseCompareDuration(flag)
		if err != nil {
			return 0, err
		}
		return off, nil
	}
	rel, err := d.Release(ctx, env, n.Range.To)
	if err != nil {
		return 0, obsbackend.Undetermined("the release live in %s is unknown: %v", env, err)
	}
	if rel == nil {
		return 0, obsbackend.Wrong("--compare prev-release: no release was promoted into %s, so there is no before to compare with", env).
			With(obsout.Command("forge", "env", "releases", env))
	}
	off := n.Range.To.Sub(rel.PromotedAt)
	if off <= 0 {
		return 0, obsbackend.Wrong("--compare prev-release: %s was promoted at %s, after the end of the range", rel.Version, rel.PromotedAt.Format(time.RFC3339))
	}
	return off, nil
}

func parseCompareDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, obsbackend.Wrong("--compare %q: use a positive duration (24h, 168h) or prev-release", s)
	}
	return d, nil
}

// resolveSeriesKinds gives a series of p its metric's kind and, where the flags
// left it out, the kind's default aggregation, from the backend's catalog. Only
// a series with no aggregation needs it to run (the evaluator looks the kind up
// itself, over the whole range; the catalog reads the last 24 hours of it); all
// asks for every series, which a written panel needs: a dashboard's metrics
// series names its kind.
func resolveSeriesKinds(ctx context.Context, b obsbackend.Backend, n obsbackend.Narrow, p *obsquery.Panel, all bool) error {
	n.Explain = false
	kinds := map[string]obsbackend.MetricInfo{}
	for i := range p.Series {
		s := &p.Series[i]
		if !all && s.Agg != "" {
			continue
		}
		info, ok := kinds[s.Field]
		if !ok {
			res, err := b.MetricsList(ctx, obsbackend.MetricsListRequest{Narrow: n, NameContains: s.Field, Limit: obsquery.ListMetricsMax})
			if err != nil {
				return err
			}
			var found bool
			for _, m := range res.Metrics {
				if m.Name != s.Field || !m.Seen {
					continue
				}
				if !found || (!info.Queryable && m.Queryable) {
					info, found = m, true
				}
			}
			if !found {
				return obsbackend.Wrong("no metric %q with data in the last 24 hours of the range (the catalog's window); name its aggregation with --agg to read older data", s.Field).
					With(obsout.Command("forge", "obs", "metrics", "list", "--contains", s.Field))
			}
			if !info.Queryable {
				return obsbackend.Wrong("metric %q is stored as %s, which forge cannot query yet", s.Field, info.Type)
			}
			kinds[s.Field] = info
		}
		s.MetricType = info.Type
		if s.Agg == "" {
			s.Agg, s.Quantile = defaultAgg(info)
		}
	}
	return nil
}

// defaultAgg is a kind's aggregation when none is named (DESIGN §3.2).
func defaultAgg(m obsbackend.MetricInfo) (string, float64) {
	switch {
	case m.Type == "histogram" || m.Type == "exp_histogram":
		return "quantile", 0.95
	case m.Type == "sum" && m.Monotonic:
		return "rate", 0
	case m.Type == "sum":
		return "sum", 0
	}
	return "avg", 0
}

// assumeKinds fills what --explain cannot look up: it runs nothing, so a
// series with no kind is assumed from its aggregation (the statements say so).
func assumeKinds(p *obsquery.Panel) []obsbackend.Statement {
	var notes []obsbackend.Statement
	for i := range p.Series {
		s := &p.Series[i]
		if s.MetricType != "" && s.Agg != "" {
			continue
		}
		kind := "gauge"
		switch s.Agg {
		case "quantile":
			kind = "histogram"
		case "rate", "increase", "":
			kind = "sum"
		}
		if s.MetricType == "" {
			s.MetricType = kind
		}
		if s.Agg == "" {
			s.Agg = "rate"
		}
		notes = append(notes, obsbackend.Statement{Purpose: fmt.Sprintf("note: --explain does not look %s up; assuming %s %s", s.Field, s.MetricType, s.Agg)})
	}
	return notes
}

func runObsMetricsQuery(ctx context.Context, d obsDeps, env, metric string, o obsMetricsQueryOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsMetricsQuerySchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsMetricsQuery, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsMetricsQuery, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	n.Now = d.Now()
	// --filter narrows the panel, so a saved panel carries it; the scope keeps
	// only the env and --service.
	n.Filters = nil
	panel, err := buildMetricsPanel(metric, o.metricsPanelFlags, o.filters)
	if err != nil {
		return fail(err)
	}
	offset, err := compareOffsetOffline(o.compare)
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "metrics query")
	if err != nil {
		return fail(err)
	}
	var notes []obsbackend.Statement
	switch {
	case o.panelOut != "" || (!n.Explain && panelNeedsDefaults(panel)):
		if err := resolveSeriesKinds(ctx, b, n, &panel, o.panelOut != ""); err != nil {
			return fail(err)
		}
	case n.Explain:
		notes = assumeKinds(&panel)
	}
	if o.panelOut != "" {
		if err := panel.Validate(); err != nil {
			return fail(obsbackend.Wrong("%s", strings.TrimPrefix(err.Error(), obsquery.ErrInvalid.Error()+": ")))
		}
		return fail2(writePanel(o.panelOut, d.Stdout, panel), fail)
	}
	if o.compare == "prev-release" {
		if offset, err = compareOffset(ctx, d, env, o.compare, n); err != nil {
			return fail(err)
		}
	}
	req := obsbackend.MetricsQueryRequest{Narrow: n, Panel: panel, StepSeconds: int64(o.step / time.Second), Compare: offset}
	if o.limitSet || panel.Kind == "line" || panel.Kind == "number" {
		req.MaxSeries = o.out.Limit
	}
	res, err := b.MetricsQuery(ctx, req)
	if err != nil {
		return fail(err)
	}
	for _, m := range res.Meta {
		for i := range panel.Series {
			if panel.Series[i].Label == m.Label && panel.Series[i].MetricType == "" {
				panel.Series[i].MetricType = m.Type
			}
		}
	}
	doc := obsMetricsQueryDoc{
		Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Panel: dashPanel(panel), StepSeconds: res.StepSeconds,
		Meta: []obsMetaDoc{}, Series: []obsSeriesDoc{}, SeriesTotal: res.SeriesTotal, Warnings: res.Warnings,
	}
	if doc.Warnings == nil {
		doc.Warnings = []string{}
	}
	if n.Explain {
		doc.Statements = append(append([]obsbackend.Statement{}, notes...), res.Statements...)
	}
	for _, m := range res.Meta {
		doc.Meta = append(doc.Meta, obsMetaDoc{Label: m.Label, Metric: m.Metric, Type: m.Type, Monotonic: m.Monotonic, Temporality: m.Temporality, Agg: m.Agg, Quantile: m.Quantile, ValueUnit: m.ValueUnit})
	}
	doc.Series = seriesDocs(res.Series)
	if c := res.Compare; c != nil {
		doc.Compare = &obsCompareDoc{OffsetSeconds: int64(c.Offset / time.Second), Against: compareLabel(o.compare, c.Offset), From: c.From, To: c.To}
		doc.CompareSeries = seriesDocs(c.Series)
	}
	omitted := max(res.SeriesTotal-maxLabelCount(res.Series), 0)
	doc.Page = obsout.Page{Truncated: res.Truncated, Omitted: omitted}
	doc.Suggested = obsout.Suggest(metricsQuerySuggestions(env, metric, res)...)
	doc.Stamp(schemaObsMetricsQuery, nil)
	if done, err := o.out.Emit(d.Stdout, obsMetricsQuerySchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, doc.Statements)
		return nil
	}
	renderMetricsQuery(d.Stdout, panel, res, doc)
	return nil
}

func fail2(err error, fail func(error) error) error {
	if err != nil {
		return fail(err)
	}
	return nil
}

// compareOffsetOffline resolves the part of --compare that needs no lookup: a
// bad value is refused before the backend is opened.
func compareOffsetOffline(flag string) (time.Duration, error) {
	if flag == "" || flag == "prev-release" {
		return 0, nil
	}
	return parseCompareDuration(flag)
}

func panelNeedsDefaults(p obsquery.Panel) bool {
	for _, s := range p.Series {
		if s.Agg == "" {
			return true
		}
	}
	return false
}

func compareLabel(flag string, off time.Duration) string {
	if flag == "prev-release" {
		return "prev-release"
	}
	return off.String()
}

func seriesDocs(in []obsbackend.MetricSeries) []obsSeriesDoc {
	out := make([]obsSeriesDoc, 0, len(in))
	for _, s := range in {
		sd := obsSeriesDoc{Label: s.Label, Labels: s.Labels, Reduced: s.Reduced, Points: make([]obsPointDoc, len(s.Points))}
		for i, p := range s.Points {
			sd.Points[i] = obsPointDoc{Time: p.Time, Value: p.Value, Partial: p.Partial, Null: p.Null}
		}
		out = append(out, sd)
	}
	return out
}

// maxLabelCount is the most groups any one panel series returned.
func maxLabelCount(s []obsbackend.MetricSeries) int {
	per, most := map[string]int{}, 0
	for _, x := range s {
		per[x.Label]++
		most = max(most, per[x.Label])
	}
	return most
}

// metricsQuerySuggestions: the next commands worth running, ranked.
func metricsQuerySuggestions(env, metric string, res obsbackend.MetricsQueryResult) []string {
	out := []string{obsout.Command("forge", "obs", "metrics", "describe", env, metric)}
	if res.Truncated {
		out = append([]string{obsout.Command("forge", "obs", "metrics", "query", env, metric, "--limit", fmt.Sprint(obsquery.MaxTableRows))}, out...)
	}
	return out
}

func firstSeenMetric(ms []obsbackend.MetricInfo) (obsbackend.MetricInfo, bool) {
	for _, m := range ms {
		if m.Seen && m.Queryable {
			return m, true
		}
	}
	return obsbackend.MetricInfo{}, false
}

func metricKindText(m obsbackend.MetricInfo) string {
	switch {
	case m.Type == "sum" && m.Monotonic:
		return "counter"
	case m.Type == "sum":
		return "up-down"
	}
	return m.Type
}

func metricSeenText(m obsbackend.MetricInfo) string {
	if !m.Seen {
		return "not seen"
	}
	if m.LastSeen.IsZero() {
		return ""
	}
	return m.LastSeen.UTC().Format("15:04:05")
}

func renderMetricsQuery(w io.Writer, p obsquery.Panel, res obsbackend.MetricsQueryResult, doc obsMetricsQueryDoc) {
	for _, m := range res.Meta {
		agg := m.Agg
		if m.Agg == "quantile" {
			agg = "p" + strconv.FormatFloat(m.Quantile*100, 'g', -1, 64)
		}
		fmt.Fprintf(w, "%s  %s  %s %s", m.Label, m.Metric, m.Type, agg)
		if m.ValueUnit != "" {
			fmt.Fprintf(w, "  unit %s", m.ValueUnit)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%s panel, step %ds\n", p.Kind, res.StepSeconds)
	reduced := p.Kind != "line"
	header := []string{"SERIES", "GROUP", "POINTS", "LAST", "MIN", "MAX"}
	if reduced {
		header = []string{"SERIES", "GROUP", "VALUE"}
	}
	table := func(series []obsbackend.MetricSeries) [][]string {
		rows := make([][]string, 0, len(series))
		for _, s := range series {
			if reduced {
				v := "-"
				if s.Reduced != nil {
					v = num(*s.Reduced)
				}
				rows = append(rows, []string{s.Label, labelText(s.Labels), v})
				continue
			}
			last, lo, hi, n := "-", 0.0, 0.0, 0
			for _, pt := range s.Points {
				if pt.Null {
					continue
				}
				if n == 0 || pt.Value < lo {
					lo = pt.Value
				}
				if n == 0 || pt.Value > hi {
					hi = pt.Value
				}
				last = num(pt.Value)
				n++
			}
			rows = append(rows, []string{s.Label, labelText(s.Labels), strconv.Itoa(len(s.Points)), last, num(lo), num(hi)})
		}
		return rows
	}
	obsout.Table(w, header, table(res.Series), 70)
	if c := res.Compare; c != nil {
		fmt.Fprintf(w, "\ncompared with %s .. %s (%s earlier)\n", c.From.Format(time.RFC3339), c.To.Format(time.RFC3339), c.Offset)
		obsout.Table(w, header, table(c.Series), 70)
	}
	for _, wn := range res.Warnings {
		fmt.Fprintf(w, "\nwarning: %s\n", wn)
	}
	if res.Truncated {
		fmt.Fprintf(w, "\n(showing %d of %d series; raise --limit or narrow with --where / --filter)\n", maxLabelCount(res.Series), res.SeriesTotal)
	}
}

func labelText(m map[string]string) string {
	if len(m) == 0 {
		return "(all)"
	}
	return fmt.Sprint(m)
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }
