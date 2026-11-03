package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsTraces = "forge.obs.traces/v1"

var obsTracesSchema = obsout.Schema{
	Name: schemaObsTraces,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "summary", "summary.traces", "summary.error_traces", "summary.p50_ms", "summary.p95_ms", "summary.p99_ms",
		"sorted_from",
		"traces", "traces.trace_id", "traces.start", "traces.duration_ms", "traces.root_service", "traces.root_name",
		"traces.root_duration_ms", "traces.spans", "traces.errors", "traces.services",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsTraceRowDoc struct {
	TraceID        string    `json:"trace_id"`
	Start          time.Time `json:"start"`
	DurationMs     float64   `json:"duration_ms"`
	RootService    string    `json:"root_service"`
	RootName       string    `json:"root_name"`
	RootDurationMs float64   `json:"root_duration_ms"`
	Spans          int       `json:"spans"`
	Errors         int       `json:"errors,omitempty"`
	Services       []string  `json:"services,omitempty"`
}

type obsREDDoc struct {
	Traces      uint64  `json:"traces"`
	ErrorTraces uint64  `json:"error_traces"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
}

type obsTracesDoc struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Summary    *obsREDDoc             `json:"summary,omitempty"`
	SortedFrom int                    `json:"sorted_from,omitempty"`
	Traces     []obsTraceRowDoc       `json:"traces"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsTracesOpts struct {
	obsCommon
	slow        bool
	minDuration time.Duration
	errors      bool
	route       string
	pageToken   string
}

func newObsTracesCmd() *cobra.Command {
	var o obsTracesOpts
	cmd := &cobra.Command{
		Use:   "traces [env]",
		Short: "Find traces: the slowest, the failing, by route",
		Long: `List the env's traces, newest first, with a rate / errors / duration summary over
every trace the filters match. [env] defaults to dev.

  ` + Name() + ` obs traces dev --errors                      # traces with an error span
  ` + Name() + ` obs traces dev --slow --min-duration 500ms   # slowest first
  ` + Name() + ` obs traces dev --route /v1/jobs --service api
  ` + Name() + ` obs traces dev --errors --explain            # the compiled SQL

--slow orders by duration within the newest 500 matching traces (the compiler
pages newest-first only); sorted_from says how many were sorted.

` + obsTracesSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsTraces(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, time.Hour, obsquery.DefaultTraceLimit)
	f := cmd.Flags()
	f.BoolVar(&o.slow, "slow", false, "slowest first")
	f.DurationVar(&o.minDuration, "min-duration", 0, "only traces at least this long, e.g. 500ms")
	f.BoolVar(&o.errors, "errors", false, "only traces with an error span")
	f.StringVar(&o.route, "route", "", "only traces whose root span name contains this")
	f.StringVar(&o.pageToken, "page-token", "", "resume from a next_page_token")
	return cmd
}

func runObsTraces(ctx context.Context, d obsDeps, env string, o obsTracesOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsTracesSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsTraces, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsTraces, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	pageToken, err := resumePage(&n, o.pageToken)
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "traces")
	if err != nil {
		return fail(err)
	}
	res, err := b.Traces(ctx, obsbackend.TracesRequest{
		Narrow: n, ErrorsOnly: o.errors, MinDurationMs: float64(o.minDuration) / float64(time.Millisecond),
		Route: o.route, Slow: o.slow, Limit: o.out.Limit, PageToken: pageToken,
	})
	if err != nil {
		return fail(err)
	}
	doc := obsTracesDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, SortedFrom: res.SortedFrom, Traces: []obsTraceRowDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	if s := res.Summary; s != nil {
		doc.Summary = &obsREDDoc{Traces: s.Traces, ErrorTraces: s.ErrorTraces, P50Ms: s.P50Ms, P95Ms: s.P95Ms, P99Ms: s.P99Ms}
	}
	for _, t := range res.Traces {
		doc.Traces = append(doc.Traces, obsTraceRowDoc{
			TraceID: t.TraceID, Start: t.Start, DurationMs: t.DurationMs, RootService: t.RootService, RootName: t.RootName,
			RootDurationMs: t.RootDurationMs, Spans: t.Spans, Errors: t.Errors, Services: t.Services,
		})
	}
	res.NextPageToken = wrapPageToken(n.Range, res.NextPageToken)
	doc.Page = obsout.Page{Truncated: res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = tracesSuggestions(env, res)
	doc.Stamp(schemaObsTraces, nil)
	if done, err := o.out.Emit(d.Stdout, obsTracesSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	if s := res.Summary; s != nil {
		fmt.Fprintf(d.Stdout, "%d traces, %d with errors; p50 %s  p95 %s  p99 %s\n\n", s.Traces, s.ErrorTraces, ms(s.P50Ms), ms(s.P95Ms), ms(s.P99Ms))
	}
	rows := make([][]string, len(res.Traces))
	for i, t := range res.Traces {
		errs := ""
		if t.Errors > 0 {
			errs = strconv.Itoa(t.Errors) + " err"
		}
		rows[i] = []string{t.TraceID, t.Start.UTC().Format("15:04:05"), ms(t.DurationMs), t.RootService, t.RootName, strconv.Itoa(t.Spans) + " spans", errs}
	}
	obsout.Table(d.Stdout, []string{"TRACE", "START", "DURATION", "SERVICE", "ROOT", "SPANS", "ERRORS"}, rows, 60)
	if res.NextPageToken != "" {
		fmt.Fprintf(d.Stdout, "\n(more: --page-token %s)\n", res.NextPageToken)
	}
	return nil
}

func tracesSuggestions(env string, res obsbackend.TracesResult) []string {
	var cmds []string
	if len(res.Traces) > 0 {
		pick := res.Traces[0]
		for _, t := range res.Traces {
			if t.Errors > 0 {
				pick = t
				break
			}
		}
		cmds = append(cmds, obsout.Command("forge", "obs", "trace", pick.TraceID, env),
			obsout.Command("forge", "obs", "logs", env, "--trace", pick.TraceID))
	}
	return obsout.Suggest(cmds...)
}

func ms(v float64) string {
	switch {
	case v >= 1000:
		return strconv.FormatFloat(v/1000, 'f', 2, 64) + "s"
	case v >= 10:
		return strconv.FormatFloat(v, 'f', 0, 64) + "ms"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + "ms"
}
