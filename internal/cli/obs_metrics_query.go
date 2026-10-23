package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
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
	cmd.AddCommand(newObsMetricsListCmd(), newObsMetricsQueryCmd(), newObsMetricsDeclaredCmd())
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
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsMetricInfoDoc struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Unit        string `json:"unit,omitempty"`
	Description string `json:"description,omitempty"`
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
		Long: `List the metric names the env's telemetry carries in the window, with type and
unit. [env] defaults to dev.

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
		doc.Metrics = append(doc.Metrics, obsMetricInfoDoc{Name: m.Name, Type: m.Type, Unit: m.Unit, Description: m.Description})
	}
	doc.Page = obsout.Page{Truncated: res.Truncated}
	if len(res.Metrics) > 0 {
		doc.Suggested = obsout.Suggest(obsout.Command("forge", "obs", "metrics", "query", env, res.Metrics[0].Name))
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
		rows[i] = []string{m.Name, m.Type, m.Unit, m.Description}
	}
	obsout.Table(d.Stdout, []string{"METRIC", "TYPE", "UNIT", "DESCRIPTION"}, rows, 70)
	if res.Truncated {
		fmt.Fprintf(d.Stdout, "\n(more than %d; narrow with --contains or raise --limit)\n", o.out.Limit)
	}
	return nil
}

// ── metrics query ───────────────────────────────────────────────────────

const schemaObsMetricsQuery = "forge.obs.metrics_query/v1"

var obsMetricsQuerySchema = obsout.Schema{
	Name: schemaObsMetricsQuery,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "metric", "type", "unit", "agg", "step_seconds",
		"series", "series.labels.*", "series.points", "series.points.time", "series.points.value",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsPointDoc struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

type obsSeriesDoc struct {
	Labels map[string]string `json:"labels,omitempty"`
	Points []obsPointDoc     `json:"points"`
}

type obsMetricsQueryDoc struct {
	obsout.Head
	Scope       obsout.Scope           `json:"scope"`
	QueryMode   string                 `json:"query_mode"`
	Metric      string                 `json:"metric"`
	Type        string                 `json:"type,omitempty"`
	Unit        string                 `json:"unit,omitempty"`
	Agg         string                 `json:"agg,omitempty"`
	StepSeconds int64                  `json:"step_seconds,omitempty"`
	Series      []obsSeriesDoc         `json:"series"`
	Statements  []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsMetricsQueryOpts struct {
	obsCommon
	agg     string
	groupBy []string
	step    time.Duration
}

func newObsMetricsQueryCmd() *cobra.Command {
	var o obsMetricsQueryOpts
	cmd := &cobra.Command{
		Use:   "query [env] <metric>",
		Short: "One metric as time series",
		Long: `Read one metric as time series. The metric's type picks the table; --agg picks
the aggregation (default: avg for gauges, rate for sums, avg for histograms).

  ` + Name() + ` obs metrics query dev rpc.server.duration --agg p95 --group-by rpc.method
  ` + Name() + ` obs metrics query dev rpc.server.calls --agg rate --group-by service.name --since 6h
  ` + Name() + ` obs metrics query dev process.runtime.go.mem.heap_alloc --explain

--agg is one of ` + obsbackend.AggNames + `. --group-by takes the allowlisted
attribute keys (` + "`forge obs query --help`" + ` lists them), the metric's own
attributes, or metric.attr.<key> / resource.attr.<key>. Percentiles apply to
histograms only.

` + obsMetricsQuerySchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, metric := "dev", args[0]
			if len(args) == 2 {
				env, metric = args[0], args[1]
			}
			return runObsMetricsQuery(cmd.Context(), defaultObsDeps(cmd), env, metric, o)
		},
	}
	o.register(cmd, time.Hour, obsquery.MaxMetricSeries)
	f := cmd.Flags()
	f.StringVar(&o.agg, "agg", "", "aggregation: "+obsbackend.AggNames)
	f.StringArrayVar(&o.groupBy, "group-by", nil, "split by this attribute key (repeatable, at most 3)")
	f.DurationVar(&o.step, "step", 0, "bucket width (default: about 120 points per series)")
	cmd.Flag("limit").Usage = "most series to return"
	return cmd
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
	b, _, err := o.open(ctx, d, env, "metrics query")
	if err != nil {
		return fail(err)
	}
	res, err := b.MetricsQuery(ctx, obsbackend.MetricsQueryRequest{
		Narrow: n, Name: metric, Agg: o.agg, GroupBy: o.groupBy, StepSeconds: int64(o.step / time.Second), MaxSeries: o.out.Limit,
	})
	if err != nil {
		return fail(err)
	}
	doc := obsMetricsQueryDoc{
		Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Metric: metric,
		Type: res.Type, Unit: res.Unit, Agg: res.Agg, StepSeconds: res.StepSeconds, Series: []obsSeriesDoc{},
	}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, s := range res.Series {
		sd := obsSeriesDoc{Labels: s.Labels, Points: make([]obsPointDoc, len(s.Points))}
		for i, p := range s.Points {
			sd.Points[i] = obsPointDoc{Time: p.Time, Value: p.Value}
		}
		doc.Series = append(doc.Series, sd)
	}
	doc.Page = obsout.Page{Truncated: res.SeriesOmitted > 0 || res.Truncated, Omitted: res.SeriesOmitted}
	doc.Stamp(schemaObsMetricsQuery, nil)
	if done, err := o.out.Emit(d.Stdout, obsMetricsQuerySchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	fmt.Fprintf(d.Stdout, "%s  %s %s  step %ds", metric, res.Type, res.Agg, res.StepSeconds)
	if res.Unit != "" {
		fmt.Fprintf(d.Stdout, "  unit %s", res.Unit)
	}
	fmt.Fprintln(d.Stdout)
	rows := make([][]string, 0, len(res.Series))
	for _, s := range res.Series {
		last, lo, hi := 0.0, 0.0, 0.0
		for i, p := range s.Points {
			if i == 0 || p.Value < lo {
				lo = p.Value
			}
			if i == 0 || p.Value > hi {
				hi = p.Value
			}
			last = p.Value
		}
		rows = append(rows, []string{labelText(s.Labels), strconv.Itoa(len(s.Points)), num(last), num(lo), num(hi)})
	}
	obsout.Table(d.Stdout, []string{"SERIES", "POINTS", "LAST", "MIN", "MAX"}, rows, 70)
	if res.SeriesOmitted > 0 {
		fmt.Fprintf(d.Stdout, "\n(%d more series; raise --limit or narrow with --filter)\n", res.SeriesOmitted)
	}
	return nil
}

func labelText(m map[string]string) string {
	if len(m) == 0 {
		return "(all)"
	}
	return fmt.Sprint(m)
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }
