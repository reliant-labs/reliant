package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsSessions = "forge.obs.sessions/v1"

var obsSessionsSchema = obsout.Schema{
	Name: schemaObsSessions,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services",
		"query_mode",
		"sessions", "sessions.id", "sessions.service", "sessions.version", "sessions.user_id", "sessions.entry_path",
		"sessions.started", "sessions.ended", "sessions.duration_ms", "sessions.page_views", "sessions.interactions",
		"sessions.errors", "sessions.has_replay",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// obsSessionDoc is one session, in a list and in a session document.
type obsSessionDoc struct {
	ID           string    `json:"id"`
	Service      string    `json:"service"`
	Version      string    `json:"version,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	EntryPath    string    `json:"entry_path,omitempty"`
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended"`
	DurationMs   int64     `json:"duration_ms"`
	PageViews    int64     `json:"page_views"`
	Interactions int64     `json:"interactions"`
	Errors       int64     `json:"errors"`
	HasReplay    bool      `json:"has_replay"`
}

func sessionDoc(s obsbackend.SessionSummary) obsSessionDoc {
	return obsSessionDoc{
		ID: s.ID, Service: s.Service, Version: s.Version, UserID: s.UserID, EntryPath: s.EntryPath,
		Started: s.Started, Ended: s.Ended, DurationMs: s.Ended.Sub(s.Started).Milliseconds(),
		PageViews: s.PageViews, Interactions: s.Interactions, Errors: s.Errors, HasReplay: s.HasReplay,
	}
}

type obsSessionsDoc struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Sessions   []obsSessionDoc        `json:"sessions"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsSessionsOpts struct {
	obsCommon
	hasError, hasReplay bool
	user, path          string
	pageToken           string
}

func newObsSessionsCmd() *cobra.Command {
	var o obsSessionsOpts
	cmd := &cobra.Command{
		Use:   "sessions [env]",
		Short: "Real-user browser sessions: who hit errors, and which have a replay",
		Long: `List the env's browser sessions that started in the range, newest first: the
frontend (APP) and start time, user, entry path, length, page views,
interactions, browser errors, whether a replay was recorded, and the session id
(last column). [env] defaults to dev; --since defaults to 24h (at most 30d).

Read one with ` + "`" + Name() + ` obs session <id>` + "`" + `: a text timeline of what the user did.

  ` + Name() + ` obs sessions prod --has-error
  ` + Name() + ` obs sessions prod --user user-42 --since 7d
  ` + Name() + ` obs sessions prod --path /checkout --has-replay --json

Sessions are derived by the control plane from the browser's telemetry: a local
ClickStack has no session tables, so a local env answers "could not look".

` + obsSessionsSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsSessions(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, 24*time.Hour, obsquery.DefaultSessionLimit)
	f := cmd.Flags()
	f.BoolVar(&o.hasError, "has-error", false, "only sessions with at least one browser error")
	f.BoolVar(&o.hasReplay, "has-replay", false, "only sessions with a recorded replay")
	f.StringVar(&o.user, "user", "", "only this user id (the userId the app identified the session with)")
	f.StringVar(&o.path, "path", "", "only sessions whose entry path contains this, case-insensitive")
	f.StringVar(&o.pageToken, "page-token", "", "resume from a next_page_token")
	return cmd
}

func (o obsSessionsOpts) validate() error {
	if o.out.Limit < 1 || o.out.Limit > obsquery.MaxSessionLimit {
		return obsbackend.Wrong("--limit must be 1-%d", obsquery.MaxSessionLimit)
	}
	if o.since > obsquery.MaxRangeSessions {
		return obsbackend.Wrong("--since %s is longer than the %s sessions are kept", o.since, obsquery.MaxRangeSessions)
	}
	if len(o.filters) > 0 {
		return obsbackend.Wrong("--filter narrows telemetry attributes and does not apply to sessions; use --service, --user and --path")
	}
	return nil
}

// sessionReader is the backend's session side; a backend without one cannot look.
func sessionReader(b obsbackend.Backend) (obsbackend.SessionReader, error) {
	r, ok := b.(obsbackend.SessionReader)
	if !ok {
		return nil, obsbackend.Undetermined("the %s backend serves no sessions", b.Info().Backend)
	}
	return r, nil
}

func runObsSessions(ctx context.Context, d obsDeps, env string, o obsSessionsOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsSessionsSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsSessions, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsSessions, err) }
	if err := o.validate(); err != nil {
		return fail(err)
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	pageToken, err := resumePage(&n, o.pageToken)
	if err != nil {
		return fail(err)
	}
	if n.Range.To.Sub(n.Range.From) > obsquery.MaxRangeSessions {
		return fail(obsbackend.Wrong("the range is longer than the %s sessions are kept", obsquery.MaxRangeSessions))
	}
	b, _, err := o.open(ctx, d, env, "sessions")
	if err != nil {
		return fail(err)
	}
	r, err := sessionReader(b)
	if err != nil {
		return fail(err)
	}
	res, err := r.ListSessions(ctx, obsbackend.ListSessionsRequest{
		Narrow: n, HasError: o.hasError, HasReplay: o.hasReplay, UserID: o.user, PathContains: o.path,
		Limit: o.out.Limit, PageToken: pageToken,
	})
	if err != nil {
		return fail(err)
	}
	doc := obsSessionsDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Sessions: []obsSessionDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, s := range res.Sessions {
		doc.Sessions = append(doc.Sessions, sessionDoc(s))
	}
	res.NextPageToken = wrapPageToken(n.Range, res.NextPageToken)
	doc.Page = obsout.Page{Truncated: res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = sessionsSuggestions(env, res.Sessions)
	doc.Stamp(schemaObsSessions, nil)
	if done, err := o.out.Emit(d.Stdout, obsSessionsSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	renderSessions(d.Stdout, doc)
	return nil
}

func renderSessions(w io.Writer, doc obsSessionsDoc) {
	if len(doc.Sessions) == 0 {
		fmt.Fprintln(w, "no sessions in the range")
		return
	}
	// A row reads the way a person names a session — which app, when — so the
	// opaque id trails as the last column, still whole for `obs session <id>`.
	rows := make([][]string, len(doc.Sessions))
	for i, s := range doc.Sessions {
		replay := "-"
		if s.HasReplay {
			replay = "replay"
		}
		rows[i] = []string{orDash(s.Service), s.Started.UTC().Format("01-02 15:04:05"), sessionLength(s.DurationMs), orDash(s.UserID), orDash(s.EntryPath),
			strconv.FormatInt(s.PageViews, 10), strconv.FormatInt(s.Interactions, 10), strconv.FormatInt(s.Errors, 10), replay, s.ID}
	}
	obsout.Table(w, []string{"APP", "STARTED (UTC)", "LENGTH", "USER", "ENTRY", "PAGES", "CLICKS", "ERRORS", "REPLAY", "SESSION"}, rows, 40)
	if doc.NextPageToken != "" {
		fmt.Fprintf(w, "\n(more: --page-token %s)\n", doc.NextPageToken)
	}
}

func sessionLength(ms int64) string {
	d := (time.Duration(ms) * time.Millisecond).Round(time.Second)
	if d < time.Second {
		return "<1s"
	}
	return d.String()
}

func sessionsSuggestions(env string, sessions []obsbackend.SessionSummary) []string {
	var cmds []string
	for _, s := range sessions {
		if s.Errors > 0 {
			cmds = append(cmds, obsout.Command("forge", "obs", "session", s.ID, env))
		}
	}
	for _, s := range sessions {
		cmds = append(cmds, obsout.Command("forge", "obs", "session", s.ID, env))
	}
	return obsout.Suggest(cmds...)
}
