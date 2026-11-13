package cli

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsLogs = "forge.obs.logs/v1"

var obsLogsSchema = obsout.Schema{
	Name: schemaObsLogs,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services", "scope.instance", "scope.instance.id", "scope.instance.label", "scope.instance.user",
		"query", "query_mode", "trace_range",
		"logs", "logs.time", "logs.severity", "logs.service", "logs.body", "logs.trace_id", "logs.span_id", "logs.attributes.*",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsLogRecordDoc struct {
	Time       time.Time         `json:"time"`
	Severity   string            `json:"severity"`
	Service    string            `json:"service"`
	Body       string            `json:"body"`
	TraceID    string            `json:"trace_id,omitempty"`
	SpanID     string            `json:"span_id,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

func logDoc(r obsbackend.LogRecord) obsLogRecordDoc {
	return obsLogRecordDoc{Time: r.Time, Severity: r.Severity, Service: r.Service, Body: r.Body, TraceID: r.TraceID, SpanID: r.SpanID, Attributes: r.Attributes}
}

type obsLogsDoc struct {
	obsout.Head
	Scope     obsout.Scope `json:"scope"`
	Query     string       `json:"query,omitempty"`
	QueryMode string       `json:"query_mode"`
	// TraceRange says where a --trace read's range came from when no
	// --since / --from / --to named one: "spans" (the trace's own spans,
	// padded by traceLogPad) or "wide" (no span of it was found, so the
	// widest range the logs allow, searched by the trace id).
	TraceRange string                 `json:"trace_range,omitempty"`
	Logs       []obsLogRecordDoc      `json:"logs"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsLogsOpts struct {
	obsCommon
	severities []string
	trace      string
	oldest     bool
	pageToken  string
	follow     bool
	interval   time.Duration
	// rangeSet is whether --since, --from or --to was given: a --trace read
	// then keeps that range instead of the trace's own.
	rangeSet bool
}

func newObsLogsCmd() *cobra.Command { return newObsLogsCmdWith(defaultObsDeps) }

// newObsLogsCmdWith is newObsLogsCmd reading the world through deps: the
// CLI passes defaultObsDeps, the hosted MCP tools their own (mcp_hosted.go).
func newObsLogsCmdWith(deps obsDepsFunc) *cobra.Command {
	var o obsLogsOpts
	cmd := &cobra.Command{
		Use:   "logs [env] [query]",
		Short: "Search and tail logs",
		Long: `Search the env's logs. [env] defaults to dev. The optional query is the log query
language (the same one the control plane serves):

  service:api severity:error "connection refused"     words AND together
  body:timeout OR body:deadline                        OR / NOT / -term / (groups)
  trace_id:0af7651916cd43dd8448eb211c80319c            one trace
  attr.http.route:/v1/jobs  res.k8s.pod.name:api-0     log / resource attributes

  ` + Name() + ` obs logs dev 'service:api' --severity error --since 1h
  ` + Name() + ` obs logs dev --trace 0af7651916cd43dd8448eb211c80319c
  ` + Name() + ` obs logs dev 'body:panic' --follow          # tail, polling every --interval (2s)
  ` + Name() + ` obs logs dev 'service:api' --explain        # the compiled SQL

Newest first; --oldest reverses. --follow polls forward from now and, with
--json, writes NDJSON: one log record per line, ending with an envelope line.

--trace reads the range from the trace itself: its spans' first start to last
end, a minute either side, however long ago it ran (spans are looked up over
the last 72h). A trace with no span found is searched by its id over the
widest range the logs allow (7d). --since / --from / --to override both.
Each line shows its error, cause and code after the message.

` + obsLogsSchema.Help(),
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			o.rangeSet = f.Changed("since") || f.Changed("from") || f.Changed("to")
			env, query := "dev", ""
			switch len(args) {
			case 1:
				// One positional is the query unless it names a declared env;
				// the common case is `obs logs 'service:api'`.
				if looksLikeQuery(args[0]) {
					query = args[0]
				} else {
					env = args[0]
				}
			case 2:
				env, query = args[0], args[1]
			}
			return runObsLogs(cmd.Context(), deps(cmd), env, query, o)
		},
	}
	o.register(cmd, 15*time.Minute, obsquery.DefaultLogLimit)
	f := cmd.Flags()
	f.StringArrayVar(&o.severities, "severity", nil, "only these severities: trace, debug, info, warn, error, fatal (repeatable)")
	f.StringVar(&o.trace, "trace", "", "only the logs of this trace id")
	f.BoolVar(&o.oldest, "oldest", false, "oldest first")
	f.StringVar(&o.pageToken, "page-token", "", "resume from a next_page_token")
	f.BoolVar(&o.follow, "follow", false, "keep polling for new logs (NDJSON with --json); stop with ^C")
	f.DurationVar(&o.interval, "interval", 2*time.Second, "with --follow, how often to poll")
	return cmd
}

// looksLikeQuery distinguishes `obs logs service:api` from `obs logs staging`:
// an env name is a bare identifier, anything else (a field:, a quote, a space,
// a paren) is a query.
func looksLikeQuery(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return true
		}
	}
	return false
}

func runObsLogs(ctx context.Context, d obsDeps, env, query string, o obsLogsOpts) (retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsLogsSchema)
	if err != nil {
		return obsFail(d, o.out.JSON && !o.follow, schemaObsLogs, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON && !o.follow, schemaObsLogs, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	pageToken, err := resumePage(&n, o.pageToken)
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "logs")
	if err != nil {
		return fail(err)
	}
	traceRange := ""
	if o.trace != "" && !o.rangeSet && o.pageToken == "" && !o.follow && !n.Explain {
		if n.Range, traceRange, err = traceLogRange(ctx, d, b, n.Range, o.trace); err != nil {
			return fail(err)
		}
	}
	req := obsbackend.LogsRequest{
		Narrow: n, Query: query, Severities: o.severities, TraceID: o.trace,
		Oldest: o.oldest, Limit: o.out.Limit, PageToken: pageToken,
	}
	if o.follow {
		return followLogs(ctx, d, b, req, o, fields)
	}
	res, err := b.Logs(ctx, req)
	if err != nil {
		return fail(err)
	}
	res.NextPageToken = wrapPageToken(n.Range, res.NextPageToken)
	doc := obsLogsDoc{Scope: obsScope(b.Info(), n), Query: query, QueryMode: QueryModeStructured, TraceRange: traceRange, Logs: []obsLogRecordDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, r := range res.Logs {
		doc.Logs = append(doc.Logs, logDoc(r))
	}
	doc.Page = obsout.Page{Truncated: res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = o.inst.carrySuggested(logsSuggestions(env, o, res, traceRange != ""))
	doc.Stamp(schemaObsLogs, nil)
	if done, err := o.out.Emit(d.Stdout, obsLogsSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	renderLogs(d.Stdout, res.Logs)
	if res.NextPageToken != "" {
		fmt.Fprintf(d.Stdout, "\n(more: --page-token %s)\n", res.NextPageToken)
	}
	if traceRange != "" {
		fmt.Fprintf(d.Stdout, "\n(%s)\n", traceRangeNote(o.trace, traceRange, n.Range, len(res.Logs)))
	}
	return nil
}

// Where `obs logs --trace` looks for a trace's spans, and how far either side
// of them it reads its logs: a record is written a little before its span
// starts or after it ends (the "rpc failed" line after the handler returns).
const (
	traceLogLookback = 72 * time.Hour
	traceLogPad      = time.Minute
)

// traceExtentReader reads when a trace ran. Both backends implement it.
type traceExtentReader interface {
	TraceExtent(ctx context.Context, req obsbackend.TraceRequest) (obsbackend.TraceExtent, error)
}

// traceLogRange is the range `obs logs --trace` reads when none was named: the
// trace's own, from its spans, so the logs of yesterday's trace are found
// without guessing --since. With no span of it found (sampled out, never
// exported, or past the span lookback) it is the widest range the logs allow,
// searched by the id. A backend that cannot say when a trace ran keeps def.
func traceLogRange(ctx context.Context, d obsDeps, b obsbackend.Backend, def obsbackend.Range, traceID string) (obsbackend.Range, string, error) {
	r, ok := b.(traceExtentReader)
	if !ok {
		return def, "", nil
	}
	if _, err := obsquery.NormalizeTraceID(traceID); err != nil {
		return def, "", obsbackend.Wrong("--trace %q: %s", traceID, strings.TrimPrefix(err.Error(), obsquery.ErrInvalid.Error()+": "))
	}
	now := d.Now().UTC()
	ext, err := r.TraceExtent(ctx, obsbackend.TraceRequest{Narrow: obsbackend.Narrow{Range: obsbackend.Range{From: now.Add(-traceLogLookback), To: now}}, TraceID: traceID})
	if err != nil {
		return def, "", err
	}
	if ext.Spans == 0 {
		return obsbackend.Range{From: now.Add(-obsquery.MaxRangeLogs), To: now}, "wide", nil
	}
	rng := obsbackend.Range{From: ext.Start.Add(-traceLogPad), To: ext.End.Add(traceLogPad)}
	if rng.To.Sub(rng.From) > obsquery.MaxRangeLogs {
		rng.From = rng.To.Add(-obsquery.MaxRangeLogs)
	}
	return rng, "spans", nil
}

// traceRangeNote says which range a --trace read chose, under its table.
func traceRangeNote(traceID, source string, r obsbackend.Range, n int) string {
	span := r.From.UTC().Format(time.RFC3339) + " → " + r.To.UTC().Format(time.RFC3339)
	what := fmt.Sprintf("%d log lines of trace %s", n, traceID)
	switch n {
	case 0:
		what = "no log line carries trace " + traceID
	case 1:
		what = "1 log line of trace " + traceID
	}
	if source == "wide" {
		return fmt.Sprintf("%s, %s: no span of it was found in the last %dh, so the widest range was searched by its id; --since / --from narrow it",
			what, span, int(traceLogLookback.Hours()))
	}
	return fmt.Sprintf("%s, %s: the trace's own range, from its spans, a minute either side; --since / --from override it", what, span)
}

// renderLogs writes the table, each record's error, cause and code after its
// body: an "rpc failed" line without its cause is half an answer.
func renderLogs(w io.Writer, logs []obsbackend.LogRecord) {
	rows := make([][]string, len(logs))
	for i, l := range logs {
		body := l.Body
		if r := obsbackend.LogReason(l.Attributes); len(r) > 0 {
			body += "  " + obsbackend.FormatKVs(r)
		}
		rows[i] = []string{l.Time.UTC().Format("15:04:05.000"), l.Severity, l.Service, body}
	}
	obsout.Table(w, []string{"TIME", "SEVERITY", "SERVICE", "BODY"}, rows, 200)
}

// logsSuggestions offers the next commands: the first trace id seen leads to
// its tree; errors narrow; an empty window widens.
func logsSuggestions(env string, o obsLogsOpts, res obsbackend.LogsResult, traceRanged bool) []string {
	var cmds []string
	for _, l := range res.Logs {
		if l.TraceID != "" && o.trace == "" {
			cmds = append(cmds, obsout.Command("forge", "obs", "trace", l.TraceID, env))
			break
		}
	}
	if len(res.Logs) == 0 && o.since < 24*time.Hour && !traceRanged {
		cmds = append(cmds, obsout.Command("forge", "obs", "logs", env, "--since", "24h"))
	}
	if res.NextPageToken != "" {
		cmds = append(cmds, obsout.Command("forge", "obs", "logs", env, "--page-token", res.NextPageToken))
	}
	return obsout.Suggest(cmds...)
}

// followLogs polls forward: each round asks for the records after the last one
// seen (oldest first), prints them, and sleeps. It ends on ^C with the
// envelope line.
func followLogs(ctx context.Context, d obsDeps, b obsbackend.Backend, req obsbackend.LogsRequest, o obsLogsOpts, fields []string) (retErr error) {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var stream *obsout.Stream
	if o.out.JSON {
		stream = obsout.NewStream(d.Stdout, obsLogsSchema, fields)
		defer func() {
			// ^C is how a follow ends: success, not a failed run.
			err := retErr
			if ctx.Err() != nil {
				err = nil
			}
			if serr := stream.End(err); serr != nil && retErr == nil {
				retErr = serr
			}
			if ctx.Err() != nil {
				retErr = nil
			}
		}()
	}
	req.Oldest = true
	req.Range.From = d.Now().UTC().Add(-o.since)
	var cursor string
	seen := map[uint64]bool{}
	for {
		// The window's end moves with the clock, and its start never falls
		// further behind than the compiler's maximum range.
		req.Range.To = d.Now().UTC().Add(time.Minute)
		if floor := req.Range.To.Add(-obsquery.MaxRangeLogs); req.Range.From.Before(floor) {
			req.Range.From = floor
		}
		req.PageToken = cursor
		res, err := b.Logs(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, r := range res.Logs {
			if seen[r.Hash] {
				continue
			}
			seen[r.Hash] = true
			if stream != nil {
				if err := stream.Record(logDoc(r)); err != nil {
					return err
				}
			} else {
				renderLogs(d.Stdout, []obsbackend.LogRecord{r})
			}
		}
		if res.NextPageToken != "" {
			cursor = res.NextPageToken // more waiting: drain without sleeping
			continue
		}
		if len(res.Logs) > 0 {
			// Resume strictly after the newest record: a fresh query from its
			// time, bounded by the seen-set against ties.
			last := res.Logs[len(res.Logs)-1]
			req.Range.From = last.Time
			cursor = ""
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(o.interval):
		}
	}
}
