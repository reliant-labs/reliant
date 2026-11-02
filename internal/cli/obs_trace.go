package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsTrace = "forge.obs.trace/v1"

var obsTraceSchema = obsout.Schema{
	Name: schemaObsTrace,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.from", "scope.to",
		"query_mode", "trace_id", "start", "release", "release.version", "release.promotion_id", "release.promoted_at", "release.unsettled", "duration_ms", "spans_total", "spans_shown", "capped", "orphans",
		"errors", "errors.span_id", "errors.service", "errors.name", "errors.message", "errors.exception",
		"tree", "tree.span_id", "tree.service", "tree.name", "tree.kind", "tree.status", "tree.offset_ms", "tree.duration_ms",
		"tree.count", "tree.min_ms", "tree.p50_ms", "tree.p95_ms", "tree.max_ms", "tree.error", "tree.critical", "tree.hidden",
		"tree.message", "tree.key_attributes.*", "tree.uninstrumented_ms", "tree.gap_from_ms", "tree.gap_to_ms",
		"tree.attributes.*", "tree.children",
		"logs", "logs.time", "logs.severity", "logs.service", "logs.body", "logs.span_id", "logs.attributes.*", "logs_truncated",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsTraceNodeDoc struct {
	SpanID     string  `json:"span_id"`
	Service    string  `json:"service"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind,omitempty"`
	Status     string  `json:"status,omitempty"`
	OffsetMs   float64 `json:"offset_ms"`
	DurationMs float64 `json:"duration_ms"`
	Count      int     `json:"count,omitempty"`
	MinMs      float64 `json:"min_ms,omitempty"`
	P50Ms      float64 `json:"p50_ms,omitempty"`
	P95Ms      float64 `json:"p95_ms,omitempty"`
	MaxMs      float64 `json:"max_ms,omitempty"`
	Error      bool    `json:"error,omitempty"`
	Critical   bool    `json:"critical,omitempty"`
	Hidden     int     `json:"hidden,omitempty"`
	// Message is why a failed span failed (exception, else status message).
	Message string `json:"message,omitempty"`
	// KeyAttributes are the few attributes that say what the span did and
	// how it ended (obsbackend.SpanKeyAttributes): rpc / http outcome,
	// error.type, db statement summary, rows read, ClickHouse query_id.
	KeyAttributes map[string]string `json:"key_attributes,omitempty"`
	// UninstrumentedMs is the time no child span covers, when it is where
	// the time went (obsbackend.IsGap); GapFromMs..GapToMs is its largest
	// stretch, from the span's start.
	UninstrumentedMs float64            `json:"uninstrumented_ms,omitempty"`
	GapFromMs        float64            `json:"gap_from_ms,omitempty"`
	GapToMs          float64            `json:"gap_to_ms,omitempty"`
	Attributes       map[string]string  `json:"attributes,omitempty"`
	Children         []*obsTraceNodeDoc `json:"children,omitempty"`
}

type obsTraceErrorDoc struct {
	SpanID    string `json:"span_id"`
	Service   string `json:"service"`
	Name      string `json:"name"`
	Message   string `json:"message,omitempty"`
	Exception string `json:"exception,omitempty"`
}

type obsTraceLogDoc struct {
	Time     time.Time `json:"time"`
	Severity string    `json:"severity"`
	Service  string    `json:"service"`
	Body     string    `json:"body"`
	SpanID   string    `json:"span_id,omitempty"`
	// Attributes are the record's own (error, cause, code, ...), without the
	// log shipper's file and stream tags.
	Attributes map[string]string `json:"attributes,omitempty"`
}

type obsTraceDoc struct {
	obsout.Head
	Scope     obsout.Scope `json:"scope"`
	QueryMode string       `json:"query_mode"`
	TraceID   string       `json:"trace_id"`
	Start     time.Time    `json:"start"`
	// Release is the release live in the env when the trace started.
	Release       *liveRelease           `json:"release,omitempty"`
	DurationMs    float64                `json:"duration_ms"`
	SpansTotal    int                    `json:"spans_total"`
	SpansShown    int                    `json:"spans_shown"`
	Capped        bool                   `json:"capped,omitempty"`
	Orphans       int                    `json:"orphans,omitempty"`
	Errors        []obsTraceErrorDoc     `json:"errors,omitempty"`
	Tree          []*obsTraceNodeDoc     `json:"tree"`
	Logs          []obsTraceLogDoc       `json:"logs,omitempty"`
	LogsTruncated bool                   `json:"logs_truncated,omitempty"`
	Statements    []obsbackend.Statement `json:"statements,omitempty"`
	Suggested     []string               `json:"suggested,omitempty"`
}

type obsTraceOpts struct {
	obsCommon
	full bool
	logs int
}

func newObsTraceCmd() *cobra.Command {
	var o obsTraceOpts
	cmd := &cobra.Command{
		Use:   "trace <trace-id> [env]",
		Short: "One trace as a tree, with its errors and logs",
		Long: `Show one trace: its span tree, the failing spans with their exception, and the
logs that carry its id. [env] defaults to dev.

Each span line carries what says how it ended: the rpc / http outcome,
error.type, a database statement's summary, the rows a ClickHouse statement
read and its query_id (join system.query_log on it). A log that failed or
warned is shown under its span with its error and cause. A span whose children
cover little of its time gets an "uninstrumented" line: the time went to code
that emitted no span, which is where to look next.

The tree is capped for an agent's context: depth 8, 100 rendered spans, runs of
identical siblings collapsed ("×37 SELECT jobs … p50 2ms p95 9ms"); failing
spans, their ancestors and the critical path (the child that ends last at each
level, marked *) are always expanded. --full lifts the caps.

  ` + Name() + ` obs trace 0af7651916cd43dd8448eb211c80319c dev
  ` + Name() + ` obs trace 0af7651916cd43dd8448eb211c80319c dev --full --json

` + obsTraceSchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsTrace(cmd.Context(), defaultObsDeps(cmd), args[0], obsEnvArg(args, 1), o)
		},
	}
	o.register(cmd, 72*time.Hour, 0)
	cmd.Flags().BoolVar(&o.full, "full", false, "lift the depth, span and collapse caps")
	cmd.Flags().IntVar(&o.logs, "logs", 20, "most correlated log lines to include (0 = none)")
	_ = cmd.Flags().MarkHidden("limit")
	return cmd
}

func runObsTrace(ctx context.Context, d obsDeps, traceID, env string, o obsTraceOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsTraceSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsTrace, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsTrace, err) }
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "trace")
	if err != nil {
		return fail(err)
	}
	res, err := b.Trace(ctx, obsbackend.TraceRequest{Narrow: n, TraceID: traceID, LogLimit: o.logs})
	if err != nil {
		return fail(err)
	}
	doc := obsTraceDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, TraceID: res.TraceID, Tree: []*obsTraceNodeDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
		doc.Stamp(schemaObsTrace, nil)
		if done, err := o.out.Emit(d.Stdout, obsTraceSchema, doc, fields); done || err != nil {
			return err
		}
		explainText(d.Stdout, res.Statements)
		return nil
	}
	tree := obsbackend.BuildTree(res.Spans, o.full)
	if len(res.Spans) > 0 {
		doc.Start = res.Spans[0].Start
		for _, s := range res.Spans {
			if s.Start.Before(doc.Start) {
				doc.Start = s.Start
			}
		}
	}
	if !doc.Start.IsZero() {
		doc.Release = stampRelease(ctx, d.Release, d.Stderr, env, doc.Start)
	}
	doc.DurationMs = durMs(tree.Duration)
	doc.SpansTotal, doc.SpansShown, doc.Capped, doc.Orphans = tree.Spans, tree.Shown, tree.Capped || res.SpansTruncated, tree.Orphans
	for _, r := range tree.Roots {
		doc.Tree = append(doc.Tree, nodeDoc(r))
	}
	for _, s := range obsbackend.ErrorSpans(res.Spans) {
		doc.Errors = append(doc.Errors, obsTraceErrorDoc{SpanID: s.SpanID, Service: s.Service, Name: s.Name, Message: s.StatusMessage, Exception: exceptionOf(s)})
	}
	for _, l := range res.Logs {
		doc.Logs = append(doc.Logs, obsTraceLogDoc{Time: l.Time, Severity: l.Severity, Service: l.Service, Body: l.Body, SpanID: l.SpanID,
			Attributes: obsbackend.LogAttributes(l.Attributes)})
	}
	doc.LogsTruncated = res.LogsTruncated
	doc.Suggested = traceSuggestions(env, res)
	doc.Stamp(schemaObsTrace, nil)
	if done, err := o.out.Emit(d.Stdout, obsTraceSchema, doc, fields); done || err != nil {
		return err
	}
	renderTrace(d.Stdout, doc, tree, res, o.full)
	return nil
}

func durMs(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func exceptionOf(s obsbackend.Span) string {
	for _, e := range s.Events {
		if e.Name != "exception" {
			continue
		}
		t, m := e.Attributes["exception.type"], e.Attributes["exception.message"]
		switch {
		case t != "" && m != "":
			return t + ": " + m
		case m != "":
			return m
		}
		return t
	}
	return ""
}

func nodeDoc(n *obsbackend.TreeNode) *obsTraceNodeDoc {
	d := &obsTraceNodeDoc{
		SpanID: n.Span.SpanID, Service: n.Span.Service, Name: n.Span.Name, Kind: n.Span.Kind, Status: n.Span.StatusCode,
		OffsetMs: durMs(n.Offset), DurationMs: float64(n.Span.DurationNs) / 1e6, Error: n.Error, Critical: n.Critical, Hidden: n.Hidden,
	}
	if n.Count > 1 {
		d.Count, d.MinMs, d.P50Ms, d.P95Ms, d.MaxMs = n.Count, durMs(n.Min), durMs(n.P50), durMs(n.P95), durMs(n.Max)
		d.SpanID = ""
	} else {
		for _, kv := range obsbackend.SpanKeyAttributes(n.Span) {
			if d.KeyAttributes == nil {
				d.KeyAttributes = map[string]string{}
			}
			d.KeyAttributes[kv.Key] = kv.Value
		}
		if n.Error {
			d.Attributes = n.Span.Attributes
			d.Message = obsbackend.SpanMessage(n.Span)
		}
		if n.Uninstrumented > 0 {
			d.UninstrumentedMs, d.GapFromMs, d.GapToMs = durMs(n.Uninstrumented), durMs(n.Gap[0]), durMs(n.Gap[1])
		}
	}
	for _, c := range n.Children {
		d.Children = append(d.Children, nodeDoc(c))
	}
	return d
}

func traceSuggestions(env string, res obsbackend.TraceResult) []string {
	var cmds []string
	cmds = append(cmds, obsout.Command("forge", "obs", "logs", env, "--trace", res.TraceID))
	if len(res.Spans) > 0 {
		cmds = append(cmds, obsout.Command("forge", "obs", "trace", res.TraceID, env, "--full"))
	}
	return obsout.Suggest(cmds...)
}

func renderTrace(w io.Writer, doc obsTraceDoc, tree obsbackend.Tree, res obsbackend.TraceResult, full bool) {
	fmt.Fprintf(w, "trace %s  %s  %d spans", doc.TraceID, ms(doc.DurationMs), doc.SpansTotal)
	if doc.Orphans > 0 {
		fmt.Fprintf(w, "  (%d with a parent that was not exported)", doc.Orphans)
	}
	fmt.Fprintln(w)
	p := treePrinter{w: w, logs: spanLogs(res.Logs)}
	for _, r := range tree.Roots {
		p.node(r, "", true)
	}
	if tree.Capped || res.SpansTruncated {
		more := ""
		if res.SpansTruncated {
			more = fmt.Sprintf("; the trace has more than %d spans", obsquery.MaxSpansPerTrace)
		}
		fmt.Fprintf(w, "\n(capped at depth %d / %d spans%s; --full shows everything)\n", obsbackend.MaxTreeDepth, obsbackend.MaxTreeSpans, more)
	}
	if len(doc.Errors) > 0 {
		fmt.Fprintln(w, "\nerrors:")
		for _, e := range doc.Errors {
			line := e.Service + " " + e.Name
			if e.Exception != "" {
				line += ": " + e.Exception
			} else if e.Message != "" {
				line += ": " + e.Message
			}
			fmt.Fprintf(w, "  %s  %s\n", e.SpanID, line)
		}
	}
	if len(res.Logs) > 0 {
		fmt.Fprintln(w, "\nlogs:")
		renderTraceLogs(w, res.Logs)
		if res.LogsTruncated {
			fmt.Fprintf(w, "(more: forge obs logs --trace %s)\n", doc.TraceID)
		}
	}
}

// treePrinter writes the tree, with each span's telling logs under it.
type treePrinter struct {
	w    io.Writer
	logs map[string][]obsbackend.LogRecord
}

// spanLogs indexes the logs worth showing under their span: the ones that
// warn or fail, or that carry an error, whatever their level. A request's
// "rpc failed" is often logged at INFO (a canceled request is the caller's
// choice), and its error and cause are the answer the tree is read for.
func spanLogs(logs []obsbackend.LogRecord) map[string][]obsbackend.LogRecord {
	out := map[string][]obsbackend.LogRecord{}
	for _, l := range logs {
		if l.SpanID == "" {
			continue
		}
		if l.SeverityNumber >= 13 || severityAtLeastWarn(l.Severity) || len(obsbackend.LogReason(l.Attributes)) > 0 {
			out[l.SpanID] = append(out[l.SpanID], l)
		}
	}
	return out
}

func severityAtLeastWarn(s string) bool {
	switch strings.ToUpper(s) {
	case "WARN", "WARNING", "ERROR", "FATAL", "CRITICAL":
		return true
	}
	return false
}

func (p treePrinter) node(n *obsbackend.TreeNode, indent string, root bool) {
	w := p.w
	mark := " "
	if n.Critical {
		mark = "*"
	}
	label := fmt.Sprintf("%s %s", n.Span.Service, n.Span.Name)
	switch {
	case n.Count > 1:
		label = fmt.Sprintf("×%d %s  min %s p50 %s p95 %s max %s", n.Count, label, ms(durMs(n.Min)), ms(durMs(n.P50)), ms(durMs(n.P95)), ms(durMs(n.Max)))
	default:
		label = fmt.Sprintf("%s  %s", label, ms(float64(n.Span.DurationNs)/1e6))
		if kv := obsbackend.SpanKeyAttributes(n.Span); len(kv) > 0 {
			label += "  " + obsbackend.FormatKVs(kv)
		}
	}
	if n.Error {
		label += "  ERROR"
		if m := obsbackend.SpanMessage(n.Span); m != "" {
			label += " " + m
		}
	}
	prefix := indent
	if !root {
		prefix += "└─ "
	}
	fmt.Fprintf(w, "%s%s %s\n", mark, prefix, label)
	child := indent
	if !root {
		child += "   "
	}
	if n.Count <= 1 {
		for _, l := range p.logs[n.Span.SpanID] {
			line := fmt.Sprintf("log %s %s", l.Severity, l.Body)
			if r := obsbackend.LogReason(l.Attributes); len(r) > 0 {
				line += ": " + obsbackend.FormatKVs(r)
			}
			fmt.Fprintf(w, "  %s↳ %s\n", child, line)
		}
	}
	for _, c := range n.Children {
		p.node(c, child, false)
	}
	if n.Hidden > 0 {
		fmt.Fprintf(w, "  %s└─ … %d more spans\n", child, n.Hidden)
	}
	if n.Uninstrumented > 0 {
		dur := time.Duration(n.Span.DurationNs)
		where := fmt.Sprintf("largest gap +%s → +%s", ms(durMs(n.Gap[0])), ms(durMs(n.Gap[1])))
		if len(n.Children) == 0 && n.Hidden == 0 {
			where = "no child spans"
		} else if n.Gap[1] == dur {
			where += ", after the last child"
		}
		fmt.Fprintf(w, "  %s└─ ⋯ uninstrumented %s of %s (%.0f%%): %s; the time went to code that emitted no span\n",
			child, ms(durMs(n.Uninstrumented)), ms(durMs(dur)), 100*float64(n.Uninstrumented)/float64(dur), where)
	}
}

// renderTraceLogs is renderLogs with each record's error, cause and code after
// its body.
func renderTraceLogs(w io.Writer, logs []obsbackend.LogRecord) {
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
