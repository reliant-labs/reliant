package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsQuery = "forge.obs.query/v1"

var obsQuerySchema = obsout.Schema{
	Name: schemaObsQuery,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query_mode", "columns", "columns.name", "columns.type", "rows",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsQueryDoc struct {
	obsout.Head
	Scope     obsout.Scope        `json:"scope"`
	QueryMode string              `json:"query_mode"`
	Columns   []obsbackend.Column `json:"columns"`
	// Rows are arrays in column order, as ClickHouse's JSONCompact: compact,
	// and not an object per row repeating every column name.
	Rows       [][]any                `json:"rows"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsQueryOpts struct {
	obsCommon
	sql, sqlFile, panel string
	params              []string
	step                time.Duration
}

func newObsQueryCmd() *cobra.Command {
	var o obsQueryOpts
	cmd := &cobra.Command{
		Use:   "query [env]",
		Short: "Anything else: raw SQL or a dashboard panel, with the scope bound",
		Long: `Run one read-only SELECT (--sql) or one dashboard panel (--panel FILE) against the
env's telemetry. [env] defaults to dev. This is the escape hatch beside the
structured verbs: when logs / traces / endpoints / metrics cannot say it, say it
in SQL, with the scope already bound.

The statement may use these parameters; forge binds them from the flags, and no
value is ever spliced into the SQL:

  {from:DateTime64(9)} {to:DateTime64(9)}   the range (--since / --from / --to)
  {step:UInt32}                              a bucket width in seconds (--step)
  {services:Array(String)}                   the --service values; [] = all
  {envs:Array(String)}  {env_ids:Array(String)}   the env being read

  ` + Name() + ` obs query dev --sql "SELECT toStartOfMinute(Timestamp) AS t, count() AS n
      FROM otel_logs WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
      AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))
      GROUP BY t ORDER BY t"
  ` + Name() + ` obs query dev --sql-file slow.sql --param threshold=500
  ` + Name() + ` obs query dev --panel p95-by-route.json

Start from a structured verb's SQL instead of a blank page:
  ` + Name() + ` obs endpoints dev --explain          # prints the compiled statement and its parameters

The statement runs through the same guard as an alert rule (one SELECT; no
writes, DDL, SETTINGS, FORMAT, system tables or table functions), read-only,
with a 30s timeout and a row cap (--limit). A refused statement exits 1 with
the reason.

--panel FILE is one dashboard panel as JSON (the panels[] element of a
dashboards/*.json; kinds line, number, table, bar, heatmap, sql) flattened to a
table. Panels over the hosted platform's own tables (issues, sessions, funnel)
need the control plane.

Raw SQL is counted: the JSON document says "query_mode": "raw", and with
FORGE_OBS_DEBUG=1 the statement's shape (literals removed) is logged to stderr.

` + obsQuerySchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsQuery(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, time.Hour, 200)
	f := cmd.Flags()
	f.StringVar(&o.sql, "sql", "", "one read-only SELECT, with the scope parameters above")
	f.StringVar(&o.sqlFile, "sql-file", "", "read the statement from this file (- for stdin)")
	f.StringVar(&o.panel, "panel", "", "a dashboard panel JSON file")
	f.StringArrayVar(&o.params, "param", nil, "bind your own {name:Type} parameter, name=value (repeatable)")
	f.DurationVar(&o.step, "step", 0, "the {step} parameter / a panel's bucket width (default: about 120 points)")
	return cmd
}

func runObsQuery(ctx context.Context, d obsDeps, env string, o obsQueryOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsQuerySchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsQuery, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsQuery, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	req := obsbackend.QueryRequest{Narrow: n, StepSeconds: int64(o.step / time.Second), Limit: o.out.Limit}
	mode := QueryModeRaw
	switch sources(o) {
	case 0:
		return fail(obsbackend.Wrong("give --sql, --sql-file or --panel").With(obsbackend.Cmd("forge", "obs", "query", "--help")))
	case 1:
	default:
		return fail(obsbackend.Wrong("--sql, --sql-file and --panel are three sources for one query; give one"))
	}
	switch {
	case o.panel != "":
		p, err := readPanelFile(o.panel)
		if err != nil {
			return fail(err)
		}
		req.Panel = &p
		if p.Kind != "sql" {
			mode = QueryModeStructured
		}
	default:
		sql, err := readSQLSource(o, os.Stdin)
		if err != nil {
			return fail(err)
		}
		req.SQL = sql
	}
	if req.Params, err = parseParams(o.params); err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "query")
	if err != nil {
		return fail(err)
	}
	res, err := b.Query(ctx, req)
	if mode == QueryModeRaw && !n.Explain {
		sql := req.SQL
		if req.Panel != nil {
			sql = req.Panel.SQL
		}
		logRawQuery(d.Stderr, env, "cli", sql)
	}
	if err != nil {
		return fail(err)
	}
	doc := obsQueryDoc{Scope: obsScope(b.Info(), n), QueryMode: mode, Columns: res.Columns, Rows: res.Rows}
	if doc.Columns == nil {
		doc.Columns, doc.Rows = []obsbackend.Column{}, [][]any{}
	}
	if n.Explain {
		doc.Statements = res.Statements
	}
	doc.Page = obsout.Page{Truncated: res.Truncated}
	if res.Truncated {
		doc.Suggested = obsout.Suggest(obsout.Command("forge", "obs", "query", env, "--limit", fmt.Sprint(min(o.out.Limit*5, obsquery.MaxSQLRows))))
	}
	doc.Stamp(schemaObsQuery, nil)
	if done, err := o.out.Emit(d.Stdout, obsQuerySchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	renderQueryTable(d.Stdout, res)
	return nil
}

func sources(o obsQueryOpts) int {
	n := 0
	for _, s := range []string{o.sql, o.sqlFile, o.panel} {
		if s != "" {
			n++
		}
	}
	return n
}

func readSQLSource(o obsQueryOpts, stdin io.Reader) (string, error) {
	if o.sql != "" {
		return o.sql, nil
	}
	var raw []byte
	var err error
	if o.sqlFile == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(o.sqlFile)
	}
	if err != nil {
		return "", obsbackend.Wrong("read --sql-file: %v", err)
	}
	sql := strings.TrimSpace(string(raw))
	if sql == "" {
		return "", obsbackend.Wrong("--sql-file %s is empty", o.sqlFile)
	}
	return sql, nil
}

func parseParams(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range in {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, obsbackend.Wrong("--param %q: use name=value", p)
		}
		if !obsquery.IsIdentifier(k) {
			return nil, obsbackend.Wrong("--param %q: %q is not a parameter name", p, k)
		}
		out[k] = v
	}
	return out, nil
}

func renderQueryTable(w io.Writer, res obsbackend.QueryResult) {
	if len(res.Columns) == 0 {
		fmt.Fprintln(w, "(no rows)")
		return
	}
	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = c.Name
	}
	rows := make([][]string, len(res.Rows))
	for i, r := range res.Rows {
		rows[i] = make([]string, len(r))
		for j, v := range r {
			rows[i][j] = obsCellText(v)
		}
	}
	obsout.Table(w, header, rows, 80)
	if res.Truncated {
		fmt.Fprintf(w, "\n(truncated at %d rows; raise --limit)\n", len(res.Rows))
	}
}

func obsCellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// ── panel files ─────────────────────────────────────────────────────────

// readPanelFile decodes one dashboard panel (dashspec.Panel JSON) strictly: an
// unknown field is an error, not a silently ignored typo.
func readPanelFile(path string) (obsquery.Panel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return obsquery.Panel{}, obsbackend.Wrong("read --panel: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p dashspec.Panel
	if err := dec.Decode(&p); err != nil {
		return obsquery.Panel{}, obsbackend.Wrong("--panel %s: %v", path, err)
	}
	return panelOf(p), nil
}

func predsOf(in []dashspec.Predicate) []obsquery.PanelPredicate {
	out := make([]obsquery.PanelPredicate, len(in))
	for i, p := range in {
		out[i] = obsquery.PanelPredicate{Field: p.Field, Op: string(p.Op), Value: p.Value, Values: p.Values}
	}
	return out
}

// panelOf is the dashboard panel in the compiler's vocabulary.
func panelOf(p dashspec.Panel) obsquery.Panel {
	out := obsquery.Panel{
		ID: p.ID, Kind: string(p.Kind), Signal: string(p.Signal), Ratio: p.Ratio, Formula: p.Formula, Limit: p.Limit,
		SQL: p.SQL, Where: predsOf(p.Where), GroupBy: p.GroupBy, HasError: p.HasError,
	}
	if p.Funnel != nil {
		out.Funnel = &obsquery.PanelFunnel{Steps: p.Funnel.Steps, Window: p.Funnel.Window, By: string(p.Funnel.By)}
	}
	for _, s := range p.Series {
		out.Series = append(out.Series, obsquery.PanelSeries{
			Label: s.Label, Agg: string(s.Agg), Quantile: s.Quantile, Field: s.Field, MetricType: s.MetricType, Where: predsOf(s.Where),
		})
	}
	return out
}
