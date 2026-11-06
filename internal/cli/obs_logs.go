package cli

import (
	"context"
	"fmt"
	"io"
	"os/signal"
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
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to", "scope.services",
		"query", "query_mode",
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
	Scope      obsout.Scope           `json:"scope"`
	Query      string                 `json:"query,omitempty"`
	QueryMode  string                 `json:"query_mode"`
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
}

func newObsLogsCmd() *cobra.Command {
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

` + obsLogsSchema.Help(),
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			return runObsLogs(cmd.Context(), defaultObsDeps(cmd), env, query, o)
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
	doc := obsLogsDoc{Scope: obsScope(b.Info(), n), Query: query, QueryMode: QueryModeStructured, Logs: []obsLogRecordDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, r := range res.Logs {
		doc.Logs = append(doc.Logs, logDoc(r))
	}
	doc.Page = obsout.Page{Truncated: res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = logsSuggestions(env, o, res)
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
	return nil
}

func renderLogs(w io.Writer, logs []obsbackend.LogRecord) {
	rows := make([][]string, len(logs))
	for i, l := range logs {
		rows[i] = []string{l.Time.UTC().Format("15:04:05.000"), l.Severity, l.Service, l.Body}
	}
	obsout.Table(w, []string{"TIME", "SEVERITY", "SERVICE", "BODY"}, rows, 160)
}

// logsSuggestions offers the next commands: the first trace id seen leads to
// its tree; errors narrow; an empty window widens.
func logsSuggestions(env string, o obsLogsOpts, res obsbackend.LogsResult) []string {
	var cmds []string
	for _, l := range res.Logs {
		if l.TraceID != "" && o.trace == "" {
			cmds = append(cmds, obsout.Command("forge", "obs", "trace", l.TraceID, env))
			break
		}
	}
	if len(res.Logs) == 0 && o.since < 24*time.Hour {
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
