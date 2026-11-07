package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/suspect"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsIssue = "forge.obs.issue/v1"

var obsIssueSchema = obsout.Schema{
	Name: schemaObsIssue,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services",
		"query_mode",
		"issue", "issue.id", "issue.platform", "issue.service", "issue.title", "issue.culprit", "issue.exception_type",
		"issue.status", "issue.first_seen", "issue.last_seen", "issue.first_version", "issue.last_version",
		"issue.events", "issue.affected", "issue.affected_unit", "issue.latest_trace_id", "issue.resolved_at", "issue.resolved_version",
		"issue.regressed_at", "issue.ignored_until", "issue.incident_key", "issue.related_count",
		"issue.assignee", "issue.assignee.id", "issue.assignee.name",
		"incident_issue_ids",
		"timeline", "timeline.id", "timeline.time", "timeline.issue_id", "timeline.kind",
		"timeline.actor", "timeline.actor.kind", "timeline.actor.id", "timeline.actor.name",
		"timeline.status", "timeline.status.from", "timeline.status.to", "timeline.status.version", "timeline.status.ignored_until",
		"timeline.comment", "timeline.comment.id", "timeline.comment.issue_id", "timeline.comment.author", "timeline.comment.author.id",
		"timeline.comment.author.name", "timeline.comment.body", "timeline.comment.created_at", "timeline.comment.updated_at",
		"timeline.assignment", "timeline.assignment.from", "timeline.assignment.from.id", "timeline.assignment.from.name",
		"timeline.assignment.to", "timeline.assignment.to.id", "timeline.assignment.to.name",
		"timeline.chat", "timeline.chat.chat_id", "timeline.chat.title", "timeline.chat.linked_by", "timeline.chat.linked_by.id",
		"timeline.chat.linked_by.name", "timeline.chat.linked_at",
		"timeline_omitted", "timeline_issue_id", "timeline_note",
		"comment", "comment.id", "comment.issue_id", "comment.author", "comment.author.id", "comment.author.name",
		"comment.body", "comment.created_at", "comment.updated_at",
		"latest_event", "latest_event.time", "latest_event.trace_id", "latest_event.span_id", "latest_event.version",
		"latest_event.message", "latest_event.attributes.*", "latest_event.session_id", "latest_event.url_path",
		"events", "events.time", "events.trace_id", "events.span_id", "events.version", "events.message", "events.attributes.*",
		"frames", "frames.function", "frames.file", "frames.line", "frames.in_app",
		"by_version", "by_version.version", "by_version.events", "by_version.affected", "by_version.affected_unit", "by_version.first_seen", "by_version.last_seen",
		"co_occurring_issue_ids", "incident_issue_ids",
		"release", "release.version", "release.promotion_id", "release.promoted_at", "release.unsettled",
		"first_release", "first_release.version", "first_release.git_commit", "first_release.commit_range", "first_release.promoted_at",
		"suspect_commits", "suspect_commits.sha", "suspect_commits.subject", "suspect_commits.author", "suspect_commits.time",
		"suspect_commits.files", "suspect_commits.pr", "suspect_note",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsIssueEventDoc struct {
	Time       time.Time         `json:"time"`
	TraceID    string            `json:"trace_id,omitempty"`
	SpanID     string            `json:"span_id,omitempty"`
	Version    string            `json:"version,omitempty"`
	Message    string            `json:"message,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	URLPath    string            `json:"url_path,omitempty"`
}

func issueEventDoc(e obsbackend.IssueEvent) obsIssueEventDoc {
	return obsIssueEventDoc{Time: e.Time, TraceID: e.TraceID, SpanID: e.SpanID, Version: e.Version, Message: e.Message,
		Attributes: e.Attributes, SessionID: e.SessionID, URLPath: e.URLPath}
}

type obsIssueFrameDoc struct {
	Function string `json:"function,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	InApp    bool   `json:"in_app,omitempty"`
}

type obsIssueVersionDoc struct {
	Version      string    `json:"version"`
	Events       int64     `json:"events"`
	Affected     int64     `json:"affected"`
	AffectedUnit string    `json:"affected_unit,omitempty"`
	FirstSeen    time.Time `json:"first_seen,omitzero"`
	LastSeen     time.Time `json:"last_seen,omitzero"`
}

type obsIssueReleaseDoc struct {
	Version     string    `json:"version"`
	GitCommit   string    `json:"git_commit,omitempty"`
	CommitRange string    `json:"commit_range,omitempty"`
	PromotedAt  time.Time `json:"promoted_at,omitzero"`
}

type obsSuspectDoc struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author,omitempty"`
	Time    time.Time `json:"time"`
	Files   []string  `json:"files,omitempty"`
	PR      int       `json:"pr,omitempty"`
}

type obsIssueDoc struct {
	obsout.Head
	Scope       obsout.Scope         `json:"scope"`
	QueryMode   string               `json:"query_mode"`
	Issue       obsIssueRowDoc       `json:"issue"`
	LatestEvent *obsIssueEventDoc    `json:"latest_event,omitempty"`
	Events      []obsIssueEventDoc   `json:"events,omitempty"`
	Frames      []obsIssueFrameDoc   `json:"frames,omitempty"`
	ByVersion   []obsIssueVersionDoc `json:"by_version,omitempty"`
	CoOccurring []string             `json:"co_occurring_issue_ids,omitempty"`
	// Incident are the other issues of this issue's incident: the same cause,
	// first seen close in time, in any service.
	Incident []string `json:"incident_issue_ids,omitempty"`
	obsIssueTimeline
	// Release is the release live in the env when the issue was last seen.
	Release      *liveRelease           `json:"release,omitempty"`
	FirstRelease *obsIssueReleaseDoc    `json:"first_release,omitempty"`
	Suspects     []obsSuspectDoc        `json:"suspect_commits"`
	SuspectNote  string                 `json:"suspect_note,omitempty"`
	Statements   []obsbackend.Statement `json:"statements,omitempty"`
	Suggested    []string               `json:"suggested,omitempty"`
}

// obsIssueSuspects finds the suspect commits of an issue in the developer's
// checkout. A seam: tests point it at a temp repository.
var obsIssueSuspects = func(ctx context.Context, commitRange string, files []string) ([]suspect.Commit, error) {
	return suspect.Finder{Dir: projectDirForKCL()}.Find(ctx, commitRange, files)
}

type obsIssueOpts struct {
	obsCommon
	events        int
	timelineLimit int
}

func newObsIssueCmd() *cobra.Command {
	var o obsIssueOpts
	cmd := &cobra.Command{
		Use:   "issue <id> [env]",
		Short: "Everything needed to fix one issue; resolve, ignore and unresolve triage it",
		Long: `Show one error issue: the issue, its latest event and call site, its events per
version, the issues that fire in the same traces, the release it first appeared
in, and the suspect commits: the commits of that release's range (or, with no
range known, the last 20) that touched the files the error was raised from,
read from this checkout with git. A hosted env also shows who it is assigned to,
the incident it belongs to (the other issues with the same cause, first seen
close together: related_count and incident_key), and the newest
--timeline-limit timeline entries, oldest first. [env] defaults to dev.

  ` + Name() + ` obs issue ISS-7f3a prod
  ` + Name() + ` obs issue sfp1-0123456789abcdef dev --events 10
  ` + Name() + ` obs issue resolve ISS-7f3a prod --in next
  ` + Name() + ` obs issue ignore ISS-7f3a prod --until 72h
  ` + Name() + ` obs issue unresolve ISS-7f3a prod
  ` + Name() + ` obs issue comment ISS-7f3a "checked the pool; it is the new retry loop" prod
  ` + Name() + ` obs issue assign ISS-7f3a <user-id|none> prod

Triage needs a hosted env: a local issue is a query over logs and has no state.

` + obsIssueSchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsIssue(cmd.Context(), defaultObsDeps(cmd), args[0], obsEnvArg(args, 1), o)
		},
	}
	o.register(cmd, 72*time.Hour, 0)
	cmd.Flags().IntVar(&o.events, "events", 5, "newest events to include (0 = none)")
	cmd.Flags().IntVar(&o.timelineLimit, "timeline-limit", defaultTimelineLimit, fmt.Sprintf("newest timeline entries (comments, status changes, assignment, linked chats) to include, oldest first (0 = none; at most %d)", maxTimelineLimit))
	_ = cmd.Flags().MarkHidden("limit")
	cmd.AddCommand(newObsIssueTriageCmd("resolve"), newObsIssueTriageCmd("ignore"), newObsIssueTriageCmd("unresolve"),
		newObsIssueCommentCmd(), newObsIssueAssignCmd())
	return cmd
}

func runObsIssue(ctx context.Context, d obsDeps, id, env string, o obsIssueOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsIssueSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsIssue, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsIssue, err) }
	if o.events < 0 || o.events > obsquery.MaxIssueEvents {
		return fail(obsbackend.Wrong("--events must be 0-%d", obsquery.MaxIssueEvents))
	}
	if o.timelineLimit < 0 || o.timelineLimit > maxTimelineLimit {
		return fail(obsbackend.Wrong("--timeline-limit must be 0-%d", maxTimelineLimit))
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	b, _, err := o.open(ctx, d, env, "issue")
	if err != nil {
		return fail(err)
	}
	r, err := issueReader(b)
	if err != nil {
		return fail(err)
	}
	res, err := r.GetIssue(ctx, obsbackend.GetIssueRequest{Narrow: n, ID: id})
	if err != nil {
		return fail(err)
	}
	doc := obsIssueDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Suspects: []obsSuspectDoc{}, Statements: nil}
	doc.Issue = issueRowDoc(res.Issue)
	if n.Explain {
		doc.Statements = res.Statements
		doc.Stamp(schemaObsIssue, nil)
		if done, err := o.out.Emit(d.Stdout, obsIssueSchema, doc, fields); done || err != nil {
			return err
		}
		explainText(d.Stdout, res.Statements)
		return nil
	}
	if res.LatestEvent != nil {
		e := issueEventDoc(*res.LatestEvent)
		doc.LatestEvent = &e
	}
	for _, f := range res.Frames {
		doc.Frames = append(doc.Frames, obsIssueFrameDoc{Function: f.Function, File: f.File, Line: f.Line, InApp: f.InApp})
	}
	for _, v := range res.ByVersion {
		doc.ByVersion = append(doc.ByVersion, obsIssueVersionDoc{Version: v.Version, Events: v.Events, Affected: v.Affected, AffectedUnit: v.AffectedUnit, FirstSeen: v.FirstSeen, LastSeen: v.LastSeen})
	}
	doc.CoOccurring = res.CoOccurring
	doc.Incident = res.Incident
	doc.obsIssueTimeline = readTimeline(ctx, d.Stderr, r, id, o.timelineLimit)
	if fr := res.FirstRelease; fr != nil {
		doc.FirstRelease = &obsIssueReleaseDoc{Version: fr.Version, GitCommit: fr.GitCommit, CommitRange: fr.CommitRange, PromotedAt: fr.PromotedAt}
	}
	if o.events > 0 {
		evs, err := r.ListIssueEvents(ctx, obsbackend.ListIssueEventsRequest{Narrow: n, ID: id, Limit: o.events})
		if err != nil {
			return fail(err)
		}
		for _, e := range evs.Events {
			doc.Events = append(doc.Events, issueEventDoc(e))
		}
	}
	if !res.Issue.LastSeen.IsZero() {
		doc.Release = stampRelease(ctx, d.Release, d.Stderr, env, res.Issue.LastSeen)
	}
	commitRange := ""
	if doc.FirstRelease != nil {
		commitRange = doc.FirstRelease.CommitRange
	}
	doc.Suspects, doc.SuspectNote = suspectCommits(ctx, d.Stderr, res, commitRange)
	doc.Suggested = issueSuggestions(env, res)
	doc.Stamp(schemaObsIssue, nil)
	if done, err := o.out.Emit(d.Stdout, obsIssueSchema, doc, fields); done || err != nil {
		return err
	}
	renderIssue(d.Stdout, doc)
	return nil
}

// suspectCommits asks the checkout which commits touched the files the error
// was raised from. It never fails the verb: a missing checkout or shallow
// history is a note on stderr and in the document, and the list is empty.
func suspectCommits(ctx context.Context, errOut io.Writer, res obsbackend.GetIssueResult, commitRange string) ([]obsSuspectDoc, string) {
	var files []string
	if f := suspect.CulpritFile(res.Issue.Culprit); f != "" {
		files = append(files, f)
	}
	for _, fr := range res.Frames {
		if fr.File != "" && fr.InApp && len(files) < 4 {
			files = append(files, fr.File)
		}
	}
	if len(files) == 0 {
		return []obsSuspectDoc{}, ""
	}
	commits, err := obsIssueSuspects(ctx, commitRange, files)
	if err != nil {
		note := "suspect commits unavailable: " + err.Error()
		switch {
		case errors.Is(err, suspect.ErrNotCheckout):
			note = "suspect commits unavailable: not inside a git checkout"
		case errors.Is(err, suspect.ErrNoFiles):
			note = "suspect commits unavailable: the culprit file (" + strings.Join(files, ", ") + ") is not tracked in this checkout"
		}
		fmt.Fprintln(errOut, "note:", note)
		return []obsSuspectDoc{}, note
	}
	out := make([]obsSuspectDoc, 0, len(commits))
	for _, c := range commits {
		out = append(out, obsSuspectDoc{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time, Files: c.Files, PR: c.PR})
	}
	return out, ""
}

func issueSuggestions(env string, res obsbackend.GetIssueResult) []string {
	var cmds []string
	if t := res.Issue.LatestTraceID; t != "" {
		cmds = append(cmds, obsout.Command("forge", "obs", "trace", t, env), obsout.Command("forge", "obs", "logs", env, "--trace", t))
	}
	if res.Issue.Platform != obsbackend.PlatformBrowser && res.Issue.Status != obsbackend.IssueResolved && !strings.HasPrefix(res.Issue.ID, obsquery.LocalIssuePrefix) {
		cmds = append(cmds, obsout.Command("forge", "obs", "issue", "resolve", res.Issue.ID, env, "--in", "next"))
	}
	return obsout.Suggest(cmds...)
}

func renderIssue(w io.Writer, doc obsIssueDoc) {
	i := doc.Issue
	fmt.Fprintf(w, "%s  %s  %s\n", i.ID, i.Status, i.Title)
	if i.Assignee != nil || doc.Scope.Backend != "local" {
		fmt.Fprintf(w, "  assignee %s\n", issueUserLabel(i.Assignee))
	}
	if i.Culprit != "" {
		fmt.Fprintf(w, "  at %s\n", i.Culprit)
	}
	fmt.Fprintf(w, "  %d events, %s affected; first %s (%s), last %s (%s)\n", i.Events, affectedText(i.Affected, i.AffectedUnit),
		i.FirstSeen.UTC().Format(time.RFC3339), orDash(i.FirstVersion), i.LastSeen.UTC().Format(time.RFC3339), orDash(i.LastVersion))
	if r := doc.Release; r != nil {
		fmt.Fprintf(w, "  live when last seen: %s (promoted %s)", r.Version, r.PromotedAt.UTC().Format(time.RFC3339))
		if r.Unsettled {
			fmt.Fprint(w, ", rollout not reported finished")
		}
		fmt.Fprintln(w)
	}
	if r := doc.FirstRelease; r != nil {
		fmt.Fprintf(w, "  first release %s", r.Version)
		if r.CommitRange != "" {
			fmt.Fprintf(w, " (%s)", r.CommitRange)
		}
		fmt.Fprintln(w)
	}
	if e := doc.LatestEvent; e != nil {
		fmt.Fprintf(w, "\nlatest event %s  trace %s\n  %s\n", e.Time.UTC().Format(time.RFC3339), orDash(e.TraceID), e.Message)
	}
	if len(doc.Frames) > 0 {
		fmt.Fprintln(w, "\nframes:")
		for _, f := range doc.Frames {
			fmt.Fprintf(w, "  %s  %s:%d\n", f.Function, f.File, f.Line)
		}
	}
	if len(doc.ByVersion) > 0 {
		fmt.Fprintln(w, "\nby version:")
		rows := make([][]string, len(doc.ByVersion))
		for k, v := range doc.ByVersion {
			rows[k] = []string{orDash(v.Version), fmt.Sprint(v.Events), affectedText(v.Affected, v.AffectedUnit)}
		}
		obsout.Table(w, []string{"VERSION", "EVENTS", "AFFECTED"}, rows, 40)
	}
	if len(doc.Incident) > 0 {
		fmt.Fprintf(w, "\nincident %s: %d other issues share this cause: %s\n", doc.Issue.IncidentKey, len(doc.Incident), strings.Join(doc.Incident, ", "))
	}
	if len(doc.CoOccurring) > 0 {
		fmt.Fprintf(w, "\nalso fires with: %s\n", strings.Join(doc.CoOccurring, ", "))
	}
	if len(doc.Events) > 0 {
		fmt.Fprintln(w, "\nrecent events:")
		for _, e := range doc.Events {
			fmt.Fprintf(w, "  %s  %s  %s\n", e.Time.UTC().Format("01-02 15:04:05"), orDash(e.TraceID), e.Message)
		}
	}
	if len(doc.Suspects) > 0 {
		fmt.Fprintln(w, "\nsuspect commits:")
		for _, c := range doc.Suspects {
			pr := ""
			if c.PR != 0 {
				pr = fmt.Sprintf("  #%d", c.PR)
			}
			fmt.Fprintf(w, "  %s  %s  %s%s\n", c.SHA[:min(8, len(c.SHA))], c.Time.UTC().Format("2006-01-02"), c.Subject, pr)
		}
	} else if doc.SuspectNote != "" {
		fmt.Fprintf(w, "\n%s\n", doc.SuspectNote)
	}
	renderTimeline(w, i.ID, doc)
}

// ── triage ──────────────────────────────────────────────────────────────

type obsIssueTriageOpts struct {
	backend string
	in      string
	until   string
	out     obsout.Flags
}

func newObsIssueTriageCmd(verb string) *cobra.Command {
	var o obsIssueTriageOpts
	short := map[string]string{
		"resolve":   "Mark an issue resolved, now or in a release",
		"ignore":    "Silence an issue, until a time or until unignored",
		"unresolve": "Reopen a resolved or ignored issue",
	}[verb]
	cmd := &cobra.Command{
		Use:   verb + " <id> [env]",
		Short: short,
		Long: short + `. A hosted env only: a local issue carries no state, so this exits 2.

The write sends the row version it read, so a change made since is refused
("re-read and retry", exit 1) instead of overwriting it.

` + obsIssueSchema.Help(),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsIssueTriage(cmd.Context(), defaultObsDeps(cmd), verb, args[0], obsEnvArg(args, 1), o)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.out.JSON, "json", false, "print one JSON document on stdout")
	f.StringVar(&o.out.Fields, "fields", "", "with --json: keep only these comma-separated dotted fields (the head is always kept)")
	f.StringVar(&o.backend, "backend", "", "local | cloud (default: from the env's declaration)")
	switch verb {
	case "resolve":
		f.StringVar(&o.in, "in", "", `when the resolve takes effect: "next" (the next release promoted to the env) or a version; default now`)
	case "ignore":
		f.StringVar(&o.until, "until", "", "un-ignore at this time: an RFC 3339 time or a duration from now such as 72h; default until unignored")
	}
	return cmd
}

// parseUntil reads --until as an RFC 3339 time or a duration from now.
func parseUntil(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.UTC().Add(d), nil
	}
	return time.Time{}, obsbackend.Wrong("--until %q is neither an RFC 3339 time (2026-10-22T10:00:00Z) nor a duration from now (72h)", s)
}

func runObsIssueTriage(ctx context.Context, d obsDeps, verb, id, env string, o obsIssueTriageOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsIssueSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsIssue, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsIssue, err) }
	req := obsbackend.UpdateIssueStatusRequest{ID: id}
	switch verb {
	case "resolve":
		req.Status, req.ResolveIn = obsbackend.IssueResolved, obsbackend.ResolveNow
		switch o.in {
		case "", "now":
		case "next":
			req.ResolveIn = obsbackend.ResolveNextRelease
		default:
			req.ResolveIn, req.Version = obsbackend.ResolveVersion, o.in
		}
	case "ignore":
		req.Status = obsbackend.IssueIgnored
		if req.IgnoredUntil, err = parseUntil(o.until, d.Now()); err != nil {
			return fail(err)
		}
	default:
		req.Status = obsbackend.IssueUnresolved
	}
	c := obsCommon{backend: o.backend, out: o.out}
	b, _, err := c.open(ctx, d, env, verb)
	if err != nil {
		return fail(err)
	}
	r, err := issueReader(b)
	if err != nil {
		return fail(err)
	}
	res, err := r.UpdateIssueStatus(ctx, req)
	if err != nil {
		return fail(err)
	}
	now := d.Now().UTC()
	doc := obsIssueDoc{
		Scope: obsout.Scope{Env: b.Info().Env, Backend: b.Info().Backend, EnvID: b.Info().EnvID, From: now, To: now}, QueryMode: QueryModeStructured,
		Issue: issueRowDoc(res.Issue), Suspects: []obsSuspectDoc{},
		Suggested: obsout.Suggest(obsout.Command("forge", "obs", "issue", res.Issue.ID, env)),
	}
	doc.Stamp(schemaObsIssue, nil)
	if done, err := o.out.Emit(d.Stdout, obsIssueSchema, doc, fields); done || err != nil {
		return err
	}
	fmt.Fprintf(d.Stdout, "%s is now %s\n", res.Issue.ID, res.Issue.Status)
	return nil
}
