package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsMetricExemplars = "forge.obs.metric_exemplars/v1"

var obsMetricExemplarsSchema = obsout.Schema{
	Name: schemaObsMetricExemplars,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "metric", "window", "window.from", "window.to",
		"exemplars", "exemplars.time", "exemplars.value", "exemplars.trace_id", "exemplars.span_id",
		"exemplars.trace_stored", "exemplars.attributes.*",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsExemplarDoc struct {
	Time    time.Time `json:"time"`
	Value   float64   `json:"value"`
	TraceID string    `json:"trace_id"`
	SpanID  string    `json:"span_id,omitempty"`
	// TraceStored: the trace is in the trace store. false means it was
	// sampled out, and `forge obs trace` would find nothing.
	TraceStored bool              `json:"trace_stored"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

type obsWindowDoc struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type obsMetricExemplarsDoc struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Metric     string                 `json:"metric"`
	Window     obsWindowDoc           `json:"window"`
	Exemplars  []obsExemplarDoc       `json:"exemplars"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsMetricsExemplarsOpts struct {
	obsCommon
	at       string
	atWindow time.Duration
	minValue float64
	where    []string
	group    []string
}

func newObsMetricsExemplarsCmd() *cobra.Command {
	var o obsMetricsExemplarsOpts
	cmd := &cobra.Command{
		Use:   "exemplars [env] <metric>",
		Short: "Sampled observations of a metric, each with the trace it came from",
		Long: `The exemplars of one metric: a sampled observation with the trace that produced
it, newest first, the ones whose trace is still stored before the ones sampled
out. Up to 20, over at most 7 days. [env] defaults to dev.

The exemplars' own timestamps must fall in a window: --at T (with --around, 2m by
default, either side) or --from and --to. Without them it is the whole range.
--min-value keeps the tail of a latency histogram.

  ` + Name() + ` obs metrics exemplars prod rpc.server.duration --at 2026-10-22T11:40:00Z --min-value 500
  ` + Name() + ` obs metrics exemplars dev rpc.server.duration --since 1h --where metric.attr.status=error

--where is key=value or key!=value over the metric's attributes (repeatable).
--group KEY=VALUE narrows to one series of a grouped panel: KEY is a --group-by
key of ` + "`" + Name() + ` obs metrics query` + "`" + ` and VALUE that series' label (repeatable, one per key).

  ` + Name() + ` obs metrics exemplars dev rpc.server.duration --group rpc.method=List --group service.name=api

A stored trace's next command is in "suggested":
  ` + Name() + ` obs trace <trace-id> <env>

` + obsMetricExemplarsSchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, metric := "dev", args[0]
			if len(args) == 2 {
				env, metric = args[0], args[1]
			}
			return runObsMetricsExemplars(cmd.Context(), defaultObsDeps(cmd), env, metric, o)
		},
	}
	o.register(cmd, time.Hour, obsquery.MaxExemplars)
	f := cmd.Flags()
	f.StringVar(&o.at, "at", "", "the moment to read exemplars around, RFC 3339 (the bucket a chart point is)")
	f.DurationVar(&o.atWindow, "around", 2*time.Minute, "with --at: how far either side of it")
	f.Float64Var(&o.minValue, "min-value", 0, "only exemplars at or above this value")
	f.StringArrayVar(&o.where, "where", nil, "narrow to series matching key=value or key!=value (repeatable)")
	f.StringArrayVar(&o.group, "group", nil, "narrow to one series group: a --group-by KEY=VALUE (repeatable, one per key)")
	return cmd
}

func runObsMetricsExemplars(ctx context.Context, d obsDeps, env, metric string, o obsMetricsExemplarsOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsMetricExemplarsSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsMetricExemplars, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsMetricExemplars, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	n.Now = d.Now()
	req := obsbackend.MetricsExemplarsRequest{Narrow: n, Name: metric, Limit: o.out.Limit}
	if o.minValue != 0 {
		v := o.minValue
		req.MinValue = &v
	}
	for _, w := range o.where {
		p, err := parsePredicate(w)
		if err != nil {
			return fail(fmt.Errorf("--where %q: %w", w, err))
		}
		req.Where = append(req.Where, p)
	}
	if req.Group, err = parseExemplarGroup(o.group); err != nil {
		return fail(err)
	}
	win := obsWindowDoc{From: n.Range.From, To: n.Range.To}
	if o.at != "" {
		if o.from != "" || o.to != "" {
			return fail(obsbackend.Wrong("--at and --from/--to both name the window; give one"))
		}
		at, perr := time.Parse(time.RFC3339, o.at)
		if perr != nil {
			return fail(obsbackend.Wrong("--at %q is not an RFC 3339 time (2026-10-22T10:00:00Z)", o.at))
		}
		if o.atWindow <= 0 {
			return fail(obsbackend.Wrong("--around must be positive"))
		}
		req.From, req.To = at.UTC().Add(-o.atWindow), at.UTC().Add(o.atWindow)
		win = obsWindowDoc{From: req.From, To: req.To}
		if limit := d.Now().UTC(); req.To.After(limit) {
			req.To, win.To = limit, limit
		}
	}
	if req.From.IsZero() {
		req.From, req.To = n.Range.From, n.Range.To
		win = obsWindowDoc{From: req.From, To: req.To}
	}
	// An exemplar is exported with the first data point after it was
	// recorded, so the scope reaches a few minutes past the window.
	req.Range = exemplarScope(req, d.Now())
	b, _, err := o.open(ctx, d, env, "metrics exemplars")
	if err != nil {
		return fail(err)
	}
	res, err := b.MetricsExemplars(ctx, req)
	if err != nil {
		return fail(err)
	}
	doc := obsMetricExemplarsDoc{
		Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Metric: metric, Window: win, Exemplars: []obsExemplarDoc{},
	}
	doc.Scope.From, doc.Scope.To = win.From, win.To
	if n.Explain {
		doc.Statements = res.Statements
	}
	var suggest []string
	for _, e := range res.Exemplars {
		doc.Exemplars = append(doc.Exemplars, obsExemplarDoc{Time: e.Time, Value: e.Value, TraceID: e.TraceID, SpanID: e.SpanID, TraceStored: e.TraceStored, Attributes: e.Attributes})
		if e.TraceStored && len(suggest) < obsout.MaxSuggested {
			suggest = append(suggest, obsout.Command("forge", "obs", "trace", e.TraceID, env))
		}
	}
	doc.Suggested = obsout.Suggest(suggest...)
	doc.Stamp(schemaObsMetricExemplars, nil)
	if done, err := o.out.Emit(d.Stdout, obsMetricExemplarsSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	if len(res.Exemplars) == 0 {
		fmt.Fprintln(d.Stdout, "(no exemplars with a trace in the window)")
		return nil
	}
	rows := make([][]string, len(res.Exemplars))
	for i, e := range res.Exemplars {
		stored := "sampled out"
		if e.TraceStored {
			stored = "stored"
		}
		rows[i] = []string{e.Time.UTC().Format("15:04:05"), num(e.Value), e.TraceID, stored}
	}
	obsout.Table(d.Stdout, []string{"TIME", "VALUE", "TRACE", "TRACE STORED"}, rows, 40)
	return nil
}

// parseExemplarGroup reads --group KEY=VALUE flags into the group a series of
// a grouped panel is: group-by key to label. VALUE may be empty (a series
// whose key is unset) and may hold '='; KEY may not repeat.
func parseExemplarGroup(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	group := make(map[string]string, len(flags))
	for _, g := range flags {
		k, v, ok := strings.Cut(g, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			return nil, obsbackend.Wrong("--group %q is not KEY=VALUE", g)
		}
		if _, dup := group[k]; dup {
			return nil, obsbackend.Wrong("--group names %s twice; a series has one value per key", k)
		}
		group[k] = v
	}
	return group, nil
}

// exemplarScope is the scope range of an exemplar read: the window, plus the
// lag until the exemplar's data point is exported, never into the future.
func exemplarScope(req obsbackend.MetricsExemplarsRequest, now time.Time) obsbackend.Range {
	from, to := req.From, req.To
	end := to.Add(5 * time.Minute)
	if end.After(now) {
		end = now
	}
	if end.Before(to) {
		end = to
	}
	return obsbackend.Range{From: from, To: end}
}
