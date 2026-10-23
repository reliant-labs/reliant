package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

const schemaObsEndpoints = "forge.obs.endpoints/v1"

var obsEndpointsSchema = obsout.Schema{
	Name: schemaObsEndpoints,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "sort",
		"endpoints", "endpoints.service", "endpoints.name", "endpoints.requests", "endpoints.errors", "endpoints.error_ratio",
		"endpoints.rate_per_second", "endpoints.p50_ms", "endpoints.p95_ms", "endpoints.p99_ms",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsEndpointDoc struct {
	Service       string  `json:"service"`
	Name          string  `json:"name"`
	Requests      float64 `json:"requests"`
	Errors        float64 `json:"errors,omitempty"`
	ErrorRatio    float64 `json:"error_ratio"`
	RatePerSecond float64 `json:"rate_per_second"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
}

type obsEndpointsDoc struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Sort       string                 `json:"sort"`
	Endpoints  []obsEndpointDoc       `json:"endpoints"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsEndpointsOpts struct {
	obsCommon
	sort string
	top  int
}

func newObsEndpointsCmd() *cobra.Command {
	var o obsEndpointsOpts
	cmd := &cobra.Command{
		Use:   "endpoints [env]",
		Short: "The slowest or most failing endpoints",
		Long: `Rank the env's server endpoints (server spans by service and span name) by
traffic, p95 latency, error ratio or rate: request count, errors, p50/p95/p99.
[env] defaults to dev. Counts are weighted for tail sampling.

  ` + Name() + ` obs endpoints dev --sort p95 --top 5
  ` + Name() + ` obs endpoints dev --sort errors --service api --since 6h

The table is the busiest 200 endpoints; --sort re-ranks within them.

` + obsEndpointsSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsEndpoints(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, time.Hour, 0)
	cmd.Flags().StringVar(&o.sort, "sort", "requests", "requests | p95 | errors | rate")
	cmd.Flags().IntVar(&o.top, "top", 10, "most endpoints to return")
	_ = cmd.Flags().MarkHidden("limit")
	return cmd
}

func runObsEndpoints(ctx context.Context, d obsDeps, env string, o obsEndpointsOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsEndpointsSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsEndpoints, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsEndpoints, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "endpoints")
	if err != nil {
		return fail(err)
	}
	res, err := b.Endpoints(ctx, obsbackend.EndpointsRequest{Narrow: n, Sort: o.sort, Top: o.top})
	if err != nil {
		return fail(err)
	}
	doc := obsEndpointsDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Sort: o.sort, Endpoints: []obsEndpointDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, e := range res.Endpoints {
		doc.Endpoints = append(doc.Endpoints, obsEndpointDoc{
			Service: e.Service, Name: e.Name, Requests: e.Requests, Errors: e.Errors, ErrorRatio: e.ErrorRatio,
			RatePerSecond: e.RatePerSecond, P50Ms: e.P50Ms, P95Ms: e.P95Ms, P99Ms: e.P99Ms,
		})
	}
	doc.Page = obsout.Page{Truncated: res.Omitted > 0, Omitted: res.Omitted}
	if len(res.Endpoints) > 0 {
		top := res.Endpoints[0]
		doc.Suggested = obsout.Suggest(
			obsout.Command("forge", "obs", "traces", env, "--service", top.Service, "--route", top.Name, "--slow"),
			obsout.Command("forge", "obs", "traces", env, "--service", top.Service, "--route", top.Name, "--errors"),
		)
	}
	doc.Stamp(schemaObsEndpoints, nil)
	if done, err := o.out.Emit(d.Stdout, obsEndpointsSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	rows := make([][]string, len(res.Endpoints))
	for i, e := range res.Endpoints {
		rows[i] = []string{e.Service, e.Name, fmt.Sprintf("%.0f", e.Requests), strconv.FormatFloat(e.ErrorRatio*100, 'f', 1, 64) + "%",
			strconv.FormatFloat(e.RatePerSecond, 'f', 2, 64) + "/s", ms(e.P50Ms), ms(e.P95Ms), ms(e.P99Ms)}
	}
	obsout.Table(d.Stdout, []string{"SERVICE", "ENDPOINT", "REQUESTS", "ERRORS", "RATE", "P50", "P95", "P99"}, rows, 60)
	if res.Omitted > 0 {
		fmt.Fprintf(d.Stdout, "\n(%d more; --top to show them)\n", res.Omitted)
	}
	return nil
}
