package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsSession = "forge.obs.session/v1"

var obsSessionSchema = obsout.Schema{
	Name: schemaObsSession,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services",
		"query_mode",
		"session", "session.id", "session.service", "session.version", "session.user_id", "session.entry_path",
		"session.started", "session.ended", "session.duration_ms", "session.page_views", "session.interactions",
		"session.errors", "session.has_replay",
		"timeline", "timeline.time", "timeline.offset_ms", "timeline.kind", "timeline.name", "timeline.path", "timeline.method",
		"timeline.status", "timeline.selector", "timeline.element", "timeline.error_type", "timeline.message",
		"timeline.fingerprint", "timeline.issue_id", "timeline.trace_id", "timeline.span_id", "timeline.duration_ms",
		"timeline.props.*", "timeline.service",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// obsTimelineDoc is one line of the timeline. OffsetMs is the time since the
// session started.
type obsTimelineDoc struct {
	Time        time.Time         `json:"time"`
	OffsetMs    int64             `json:"offset_ms"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name,omitempty"`
	Path        string            `json:"path,omitempty"`
	Method      string            `json:"method,omitempty"`
	Status      int               `json:"status,omitempty"`
	Selector    string            `json:"selector,omitempty"`
	Element     string            `json:"element,omitempty"`
	ErrorType   string            `json:"error_type,omitempty"`
	Message     string            `json:"message,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	IssueID     string            `json:"issue_id,omitempty"`
	TraceID     string            `json:"trace_id,omitempty"`
	SpanID      string            `json:"span_id,omitempty"`
	DurationMs  float64           `json:"duration_ms,omitempty"`
	Props       map[string]string `json:"props,omitempty"`
	Service     string            `json:"service,omitempty"`
}

type obsSessionDocument struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Session    obsSessionDoc          `json:"session"`
	Timeline   []obsTimelineDoc       `json:"timeline"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsSessionOpts struct {
	obsCommon
	pageToken string
	// ranged is whether the caller bounded the search; otherwise the backend
	// finds the session within the 30 days sessions are kept.
	ranged bool
}

func newObsSessionCmd() *cobra.Command {
	var o obsSessionOpts
	cmd := &cobra.Command{
		Use:   "session <id> [env]",
		Short: "What one user did: a text timeline of navigations, clicks, failed requests and errors",
		Long: `Show one browser session as a text timeline, oldest first, with the time since
the session started: the pages the user opened, the elements they clicked (a
masked selector, never the text), the requests that failed (method, path,
status, duration), the errors raised (with the issue each belongs to) and the
custom events the app recorded. Failed requests and errors carry their trace
id, so the next step is ` + "`" + Name() + ` obs trace <id>` + "`" + `. [env] defaults to dev.

Paths have their query and fragment cut. No recording bytes are printed: the
replay itself is for the console's player.

  ` + Name() + ` obs session 91b955b313a1a86644ea53730a92deab prod
  ` + Name() + ` obs session 91b955b313a1a86644ea53730a92deab prod --json

Exit 0 when the session is found, 1 for a bad id or a session the env does not
have (or one older than the 30 days sessions are kept), 2 when it could not be
looked up. A session over --limit entries continues with --page-token. The session is
looked for in the last 30 days; --since, --from and --to narrow that (a
hosted timeline reads at most 7 days).

` + obsSessionSchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			o.ranged = f.Changed("since") || f.Changed("from") || f.Changed("to")
			return runObsSession(cmd.Context(), defaultObsDeps(cmd), args[0], obsEnvArg(args, 1), o)
		},
	}
	o.register(cmd, obsquery.MaxRangeSessions, obsquery.DefaultTimelineLimit)
	f := cmd.Flags()
	f.StringVar(&o.pageToken, "page-token", "", "resume the timeline from a next_page_token")
	return cmd
}

func (o obsSessionOpts) validate() error {
	if o.out.Limit < 1 || o.out.Limit > obsquery.MaxTimelineLimit {
		return obsbackend.Wrong("--limit must be 1-%d", obsquery.MaxTimelineLimit)
	}
	if len(o.filters) > 0 {
		return obsbackend.Wrong("--filter narrows telemetry attributes and does not apply to sessions; use --service")
	}
	return nil
}

func runObsSession(ctx context.Context, d obsDeps, id, env string, o obsSessionOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsSessionSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsSession, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsSession, err) }
	if err := o.validate(); err != nil {
		return fail(err)
	}
	if err := obsquery.CheckSessionID(id); err != nil {
		return fail(obsbackend.Wrong("%s", strings.TrimPrefix(err.Error(), obsquery.ErrInvalid.Error()+": ")))
	}
	n := obsbackend.Narrow{Services: o.services, Explain: o.wantExplain(), Now: d.Now().UTC()}
	if o.ranged {
		if n, err = o.narrow(d.Now()); err != nil {
			return fail(err)
		}
		n.Now = d.Now().UTC()
	}
	b, _, err := o.open(ctx, d, env, "session")
	if err != nil {
		return fail(err)
	}
	r, err := sessionReader(b)
	if err != nil {
		return fail(err)
	}
	res, err := r.GetSession(ctx, obsbackend.GetSessionRequest{Narrow: n, ID: id, Limit: o.out.Limit, PageToken: o.pageToken})
	if err != nil {
		return fail(err)
	}
	doc := obsSessionDocument{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Timeline: []obsTimelineDoc{}}
	if doc.Scope.From.IsZero() {
		to := d.Now().UTC()
		doc.Scope.From, doc.Scope.To = to.Add(-obsquery.MaxRangeSessions), to
	}
	if n.Explain {
		doc.Statements = res.Statements
		doc.Stamp(schemaObsSession, nil)
		if done, err := o.out.Emit(d.Stdout, obsSessionSchema, doc, fields); done || err != nil {
			return err
		}
		explainText(d.Stdout, res.Statements)
		return nil
	}
	doc.Session = sessionDoc(res.Session)
	if !o.ranged {
		// A keyed read sends no range: the scope echoes the span read.
		doc.Scope.From, doc.Scope.To = res.Session.Started, res.Session.Ended
	}
	for _, e := range res.Timeline {
		doc.Timeline = append(doc.Timeline, timelineDoc(e, res.Session.Started))
	}
	doc.Page = obsout.Page{Truncated: res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = sessionSuggestions(env, id, res)
	doc.Stamp(schemaObsSession, nil)
	if done, err := o.out.Emit(d.Stdout, obsSessionSchema, doc, fields); done || err != nil {
		return err
	}
	renderSession(d.Stdout, doc)
	return nil
}

func timelineDoc(e obsbackend.SessionEntry, started time.Time) obsTimelineDoc {
	return obsTimelineDoc{
		Time: e.Time, OffsetMs: e.Time.Sub(started).Milliseconds(), Kind: e.Kind, Name: e.Name, Path: e.Path, Method: e.Method,
		Status: e.Status, Selector: e.Selector, Element: e.Element, ErrorType: e.ErrorType, Message: e.Message,
		Fingerprint: e.Fingerprint, IssueID: e.IssueID, TraceID: e.TraceID, SpanID: e.SpanID, DurationMs: e.DurationMs,
		Props: e.Props, Service: e.Service,
	}
}

// sessionSuggestions ranks the next steps: the first issue an error belongs to,
// the first trace of a failed request or error, the next page of the timeline,
// then the rest.
func sessionSuggestions(env, id string, res obsbackend.GetSessionResult) []string {
	var issues, traces []string
	for _, e := range res.Timeline {
		if e.IssueID != "" {
			issues = append(issues, obsout.Command("forge", "obs", "issue", e.IssueID, env))
		}
		if e.TraceID != "" && (e.Kind == "request_failed" || e.Kind == "error") {
			traces = append(traces, obsout.Command("forge", "obs", "trace", e.TraceID, env))
		}
	}
	var cmds []string
	pick := func(from []string) []string {
		if len(from) == 0 {
			return nil
		}
		cmds = append(cmds, from[0])
		return from[1:]
	}
	issues, traces = pick(issues), pick(traces)
	if res.NextPageToken != "" {
		cmds = append(cmds, obsout.Command("forge", "obs", "session", id, env, "--page-token", res.NextPageToken))
	}
	return obsout.Suggest(append(append(cmds, issues...), traces...)...)
}

// offsetLabel is the time since the session started: +MM:SS.t.
func offsetLabel(ms int64) string {
	sign := "+"
	if ms < 0 {
		sign, ms = "-", -ms
	}
	return fmt.Sprintf("%s%02d:%02d.%d", sign, ms/60000, ms/1000%60, ms%1000/100)
}

func renderSession(w io.Writer, doc obsSessionDocument) {
	s := doc.Session
	who := "anonymous"
	if s.UserID != "" {
		who = "user " + s.UserID
	}
	fmt.Fprintf(w, "session %s  %s %s  %s\n", s.ID, s.Service, orDash(s.Version), who)
	fmt.Fprintf(w, "  %s, %s: %d pages, %d interactions, %d errors", s.Started.UTC().Format(time.RFC3339), sessionLength(s.DurationMs), s.PageViews, s.Interactions, s.Errors)
	if s.HasReplay {
		fmt.Fprint(w, ", replay recorded (watch it in the console)")
	}
	fmt.Fprintln(w)
	if s.EntryPath != "" {
		fmt.Fprintf(w, "  entry %s\n", s.EntryPath)
	}
	fmt.Fprintln(w)
	for _, e := range doc.Timeline {
		fmt.Fprintf(w, "%s  %-8s  %s\n", offsetLabel(e.OffsetMs), timelineVerb(e.Kind), timelineText(e))
	}
	if len(doc.Timeline) == 0 {
		fmt.Fprintln(w, "(no timeline entries)")
	}
	if doc.NextPageToken != "" {
		fmt.Fprintf(w, "\n(more: --page-token %s)\n", doc.NextPageToken)
	}
	if len(doc.Suggested) > 0 {
		fmt.Fprintln(w, "\nsuggested:")
		for _, c := range doc.Suggested {
			fmt.Fprintf(w, "  %s\n", c)
		}
	}
}

func timelineVerb(kind string) string {
	switch kind {
	case "navigation":
		return "navigate"
	case "request_failed":
		return "request"
	}
	return kind
}

func timelineText(e obsTimelineDoc) string {
	var b strings.Builder
	switch e.Kind {
	case "navigation":
		b.WriteString(orDash(e.Path))
		if from := e.Props["from"]; from != "" {
			fmt.Fprintf(&b, " (from %s)", from)
		} else if e.Name != "" {
			fmt.Fprintf(&b, " (%s)", e.Name)
		}
	case "click":
		fmt.Fprintf(&b, "%s %s", orDash(e.Element), orDash(e.Selector))
		if e.Path != "" {
			fmt.Fprintf(&b, " on %s", e.Path)
		}
	case "request_failed":
		fmt.Fprintf(&b, "%s %s → ", orDash(e.Method), orDash(e.Path))
		if e.Status == 0 {
			b.WriteString("network failure")
			if e.ErrorType != "" {
				fmt.Fprintf(&b, " (%s)", e.ErrorType)
			}
		} else {
			fmt.Fprintf(&b, "%d", e.Status)
		}
		if e.DurationMs > 0 {
			fmt.Fprintf(&b, " (%s)", ms(e.DurationMs))
		}
		traceSuffix(&b, e)
	case "error":
		b.WriteString(errorHead(e))
		if e.Path != "" {
			fmt.Fprintf(&b, " on %s", e.Path)
		}
		if e.IssueID != "" {
			fmt.Fprintf(&b, " → issue %s", e.IssueID)
		}
		if e.Fingerprint != "" {
			fmt.Fprintf(&b, " (fingerprint %s)", e.Fingerprint)
		}
		traceSuffix(&b, e)
	case "event":
		b.WriteString(orDash(e.Name))
		keys := make([]string, 0, len(e.Props))
		for k := range e.Props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%s", k, e.Props[k])
		}
		if e.Path != "" {
			fmt.Fprintf(&b, " on %s", e.Path)
		}
	default:
		b.WriteString(orDash(e.Name))
	}
	return b.String()
}

func errorHead(e obsTimelineDoc) string {
	switch {
	case e.ErrorType != "" && e.Message != "":
		return e.ErrorType + ": " + e.Message
	case e.Message != "":
		return e.Message
	case e.ErrorType != "":
		return e.ErrorType
	}
	return orDash(e.Name)
}

func traceSuffix(b *strings.Builder, e obsTimelineDoc) {
	if e.TraceID != "" {
		fmt.Fprintf(b, "  trace %s", e.TraceID)
	}
}
