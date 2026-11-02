package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsMetric = "forge.obs.metric/v1"

var obsMetricSchema = obsout.Schema{
	Name: schemaObsMetric,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "metric",
		"metric.name", "metric.type", "metric.unit", "metric.description", "metric.monotonic", "metric.temporality",
		"metric.services", "metric.service_count", "metric.last_seen", "metric.series_estimate", "metric.queryable",
		"metric.builtin", "metric.declared", "metric.seen", "metric.stability",
		"interval_seconds", "mixed_temporality",
		"declared", "declared.stability", "declared.description", "declared.attributes", "declared.attributes.key", "declared.attributes.values",
		"attributes", "attributes.key", "attributes.resource", "attributes.distinct_estimate", "attributes.enumerated", "attributes.high_cardinality",
		"suggested_breakdowns",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsDeclaredAttrDoc struct {
	Key    string   `json:"key"`
	Values []string `json:"values,omitempty"`
}

// obsDeclaredDoc is what the release's metrics schema says about the metric.
type obsDeclaredDoc struct {
	Stability   string               `json:"stability,omitempty"`
	Description string               `json:"description,omitempty"`
	Attributes  []obsDeclaredAttrDoc `json:"attributes"`
}

type obsAttributeDoc struct {
	Key              string   `json:"key"`
	Resource         bool     `json:"resource,omitempty"`
	DistinctEstimate int64    `json:"distinct_estimate"`
	Enumerated       []string `json:"enumerated,omitempty"`
	// HighCardinality: grouping by the key could exceed the series cap.
	HighCardinality bool `json:"high_cardinality,omitempty"`
}

type obsMetricDoc struct {
	obsout.Head
	Scope            obsout.Scope           `json:"scope"`
	QueryMode        string                 `json:"query_mode"`
	Metric           obsMetricInfoDoc       `json:"metric"`
	IntervalSeconds  int64                  `json:"interval_seconds"`
	MixedTemporality bool                   `json:"mixed_temporality,omitempty"`
	Declared         *obsDeclaredDoc        `json:"declared,omitempty"`
	Attributes       []obsAttributeDoc      `json:"attributes"`
	Breakdowns       []string               `json:"suggested_breakdowns"`
	Statements       []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsMetricsDescribeOpts struct{ obsCommon }

func newObsMetricsDescribeCmd() *cobra.Command {
	var o obsMetricsDescribeOpts
	cmd := &cobra.Command{
		Use:   "describe [env] <metric>",
		Short: "One metric: its kind, export interval, attributes and the breakdowns worth trying",
		Long: `What the explorer's side pane shows for one metric: its type, unit, monotonicity
and temporality; the export interval (the floor of a useful --step); every
attribute key with an estimate of how many values it takes, and the declared
closed value set where the schema lists one; the keys worth grouping by; and
what the project's declared schema says about it. The attribute scan reads at
most the last 24 hours of the range. [env] defaults to dev.

  ` + Name() + ` obs metrics describe dev rpc.server.duration
  ` + Name() + ` obs metrics describe prod rpc.server.duration --json --fields attributes

` + obsMetricSchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, metric := "dev", args[0]
			if len(args) == 2 {
				env, metric = args[0], args[1]
			}
			return runObsMetricsDescribe(cmd.Context(), defaultObsDeps(cmd), env, metric, o)
		},
	}
	o.register(cmd, time.Hour, obsquery.MaxDeclaredKeys)
	return cmd
}

func runObsMetricsDescribe(ctx context.Context, d obsDeps, env, metric string, o obsMetricsDescribeOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsMetricSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsMetric, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsMetric, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "metrics describe")
	if err != nil {
		return fail(err)
	}
	res, err := b.MetricsDescribe(ctx, obsbackend.MetricsDescribeRequest{Narrow: n, Name: metric})
	if err != nil {
		return fail(err)
	}
	doc := obsMetricDoc{
		Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Metric: metricInfoDoc(res.Info),
		IntervalSeconds: res.IntervalSeconds, MixedTemporality: res.MixedTemporality,
		Attributes: []obsAttributeDoc{}, Breakdowns: append([]string{}, res.SuggestedBreakdowns...),
	}
	if n.Explain {
		doc.Statements = res.Statements
	}
	if res.Info.Declared {
		dd := &obsDeclaredDoc{Stability: res.Info.Stability, Description: res.Info.Description, Attributes: []obsDeclaredAttrDoc{}}
		for _, a := range res.Info.DeclaredAttributes {
			dd.Attributes = append(dd.Attributes, obsDeclaredAttrDoc{Key: a.Key, Values: a.Values})
		}
		doc.Declared = dd
	}
	attrs, page := obsout.Bound(res.Attributes, o.out.Limit, "")
	doc.Page = page
	for _, a := range attrs {
		doc.Attributes = append(doc.Attributes, obsAttributeDoc{Key: a.Key, Resource: a.Resource, DistinctEstimate: a.DistinctEstimate, Enumerated: a.Enumerated, HighCardinality: a.HighCardinality})
	}
	doc.Suggested = obsout.Suggest(describeSuggestions(env, res)...)
	doc.Stamp(schemaObsMetric, nil)
	if done, err := o.out.Emit(d.Stdout, obsMetricSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	renderMetricDescribe(d, res, doc)
	return nil
}

// describeSuggestions: the queries that follow from what the metric is. Only
// commands that would run: a metric the evaluator cannot query offers none.
func describeSuggestions(env string, res obsbackend.MetricsDescribeResult) []string {
	m := res.Info
	if !m.Seen || !m.Queryable {
		return nil
	}
	base := []string{"forge", "obs", "metrics", "query", env, m.Name}
	var out []string
	if len(res.SuggestedBreakdowns) > 0 {
		out = append(out, obsout.Command(append(append([]string{}, base...), "--group-by", res.SuggestedBreakdowns[0])...))
	}
	out = append(out, obsout.Command(base...))
	if m.Type == "histogram" {
		out = append(out, obsout.Command("forge", "obs", "metrics", "exemplars", env, m.Name, "--since", "1h"))
	}
	return out
}

func renderMetricDescribe(d obsDeps, res obsbackend.MetricsDescribeResult, doc obsMetricDoc) {
	w := d.Stdout
	m := res.Info
	fmt.Fprintf(w, "%s  %s", m.Name, metricKindText(m))
	if m.Temporality != "" {
		fmt.Fprintf(w, "  %s", m.Temporality)
	}
	if m.Unit != "" {
		fmt.Fprintf(w, "  unit %s", m.Unit)
	}
	fmt.Fprintln(w)
	if m.Description != "" {
		fmt.Fprintln(w, m.Description)
	}
	if !m.Queryable {
		fmt.Fprintf(w, "stored as %s, which forge cannot query yet\n", m.Type)
	}
	if !m.Seen {
		fmt.Fprintln(w, "declared, but no data in the window")
	}
	if len(m.Services) > 0 {
		more := ""
		if m.ServiceCount > len(m.Services) {
			more = fmt.Sprintf(" (+%d)", m.ServiceCount-len(m.Services))
		}
		fmt.Fprintf(w, "services: %s%s\n", strings.Join(m.Services, ", "), more)
	}
	if m.SeriesEstimate > 0 || res.IntervalSeconds > 0 {
		fmt.Fprintf(w, "about %d series, exported every %ds\n", m.SeriesEstimate, res.IntervalSeconds)
	}
	if res.MixedTemporality {
		fmt.Fprintln(w, "warning: its series report both delta and cumulative temporality")
	}
	if m.Declared {
		fmt.Fprintf(w, "declared: %s\n", m.Stability)
	}
	if len(res.Attributes) > 0 {
		fmt.Fprintln(w)
		rows := make([][]string, 0, len(res.Attributes))
		for _, a := range res.Attributes {
			kind := "metric"
			if a.Resource {
				kind = "resource"
			}
			note := ""
			switch {
			case len(a.Enumerated) > 0:
				note = "enumerated: " + strings.Join(a.Enumerated, ", ")
			case a.HighCardinality:
				note = "high cardinality"
			}
			rows = append(rows, []string{a.Key, kind, strconv.FormatInt(a.DistinctEstimate, 10), note})
		}
		obsout.Table(w, []string{"ATTRIBUTE", "KIND", "VALUES", "NOTE"}, rows, 60)
	}
	if len(res.SuggestedBreakdowns) > 0 {
		fmt.Fprintf(w, "\nbreak down by: %s\n", strings.Join(res.SuggestedBreakdowns, ", "))
	}
}
