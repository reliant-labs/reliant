package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsIssues = "forge.obs.issues/v1"

var obsIssuesSchema = obsout.Schema{
	Name: schemaObsIssues,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services",
		"query_mode", "sort",
		"issues", "issues.id", "issues.platform", "issues.source", "issues.service", "issues.title", "issues.culprit", "issues.exception_type",
		"issues.status", "issues.first_seen", "issues.last_seen", "issues.first_version", "issues.last_version",
		"issues.events", "issues.affected", "issues.affected_unit", "issues.latest_trace_id", "issues.resolved_at", "issues.resolved_version",
		"issues.regressed_at", "issues.ignored_until", "issues.incident_key", "issues.related_count",
		"collapsed",
		"issues.assignee", "issues.assignee.id", "issues.assignee.name",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// obsIssueRowDoc is one issue in a list, and the issue of an issue document.
type obsIssueRowDoc struct {
	ID              string    `json:"id"`
	Platform        string    `json:"platform"`
	Source          string    `json:"source,omitempty"`
	Service         string    `json:"service"`
	Title           string    `json:"title"`
	Culprit         string    `json:"culprit,omitempty"`
	ExceptionType   string    `json:"exception_type,omitempty"`
	Status          string    `json:"status"`
	FirstSeen       time.Time `json:"first_seen,omitzero"`
	LastSeen        time.Time `json:"last_seen,omitzero"`
	FirstVersion    string    `json:"first_version,omitempty"`
	LastVersion     string    `json:"last_version,omitempty"`
	Events          int64     `json:"events"`
	Affected        int64     `json:"affected"`
	AffectedUnit    string    `json:"affected_unit,omitempty"`
	LatestTraceID   string    `json:"latest_trace_id,omitempty"`
	ResolvedAt      time.Time `json:"resolved_at,omitzero"`
	ResolvedVersion string    `json:"resolved_version,omitempty"`
	RegressedAt     time.Time `json:"regressed_at,omitzero"`
	IgnoredUntil    time.Time `json:"ignored_until,omitzero"`
	// Assignee is absent when the issue is unassigned.
	Assignee *obsUserDoc `json:"assignee,omitempty"`
	// IncidentKey is shared by the issues of one incident (the same cause,
	// first seen close in time, across services); RelatedCount is how many
	// others carry it. Issues are never merged: each keeps its own counts.
	IncidentKey  string `json:"incident_key,omitempty"`
	RelatedCount int    `json:"related_count,omitempty"`
}

func issueRowDoc(i obsbackend.Issue) obsIssueRowDoc {
	return obsIssueRowDoc{
		ID: i.ID, Platform: i.Platform, Source: i.Source, Service: i.Service, Title: i.Title, Culprit: i.Culprit, ExceptionType: i.ExceptionType,
		Status: i.Status, FirstSeen: i.FirstSeen, LastSeen: i.LastSeen, FirstVersion: i.FirstVersion, LastVersion: i.LastVersion,
		Events: i.Events, Affected: i.Affected, AffectedUnit: i.AffectedUnit, LatestTraceID: i.LatestTraceID,
		ResolvedAt: zeroUnixEpoch(i.ResolvedAt), ResolvedVersion: i.ResolvedVersion,
		RegressedAt: zeroUnixEpoch(i.RegressedAt), IgnoredUntil: zeroUnixEpoch(i.IgnoredUntil),
		Assignee: userDoc(i.Assignee), IncidentKey: i.IncidentKey, RelatedCount: i.RelatedCount,
	}
}

// affectedText is a count with the unit it counted: "3 chats", never a bare 3.
// An unknown unit (a control plane that does not say) is the number alone.
func affectedText(n int64, unit string) string {
	if unit == "" {
		return strconv.FormatInt(n, 10)
	}
	if n == 1 {
		unit = strings.TrimSuffix(unit, "s")
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// zeroUnixEpoch turns the epoch an unset protobuf Timestamp decodes to into
// the zero time, which "absent means unknown" leaves out of the document.
func zeroUnixEpoch(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t
}

type obsIssuesDoc struct {
	obsout.Head
	Scope     obsout.Scope     `json:"scope"`
	QueryMode string           `json:"query_mode"`
	Sort      string           `json:"sort"`
	Issues    []obsIssueRowDoc `json:"issues"`
	// Collapsed is how many issues of this page are hidden under another issue
	// of their incident; --expand-incidents lists them.
	Collapsed  int                    `json:"collapsed,omitempty"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsIssuesOpts struct {
	obsCommon
	statuses     []string
	platform     string
	source       string
	sinceRelease string
	sinceDeploy  string
	assignee     string
	sort         string
	pageToken    string
	// expandIncidents lists every issue of an incident; by default the issues
	// of one incident collapse under the first of them.
	expandIncidents bool
	incidentWindow  time.Duration
}

func newObsIssuesCmd() *cobra.Command {
	var o obsIssuesOpts
	cmd := &cobra.Command{
		Use:   "issues [env]",
		Short: "Grouped errors: what is failing, how often, since which release",
		Long: `List the env's error issues: ERROR logs grouped by fingerprint (sfp1), with how
often each fired, how many traces it touched and when. [env] defaults to dev.

--source <name> lists the issues of an issue source instead: records of any
level that carry issue.source=<name> (observe.IssueSource), such as the reports
agents file (--source llm_bug_report). They are grouped the same way, never
count as errors, and are listed only when asked for.

On a hosted env the control plane keeps the issues, so they have a status
(new, unresolved, resolved, regressed, ignored) that survives log retention, and
--since-release / --since-deploy ask "what did this release open". On a local
env they are grouped at query time over the local ClickStack: no state, every
issue is unresolved, and the counts are those of the range.

  ` + Name() + ` obs issues prod                          # the open ones, last 24h
  ` + Name() + ` obs issues prod --status regressed
  ` + Name() + ` obs issues prod --since-release v1.4.0   # first seen in that release
  ` + Name() + ` obs issues dev --service api --sort events
  ` + Name() + ` obs issues prod --source llm_bug_report  # agent-filed bug reports
  ` + Name() + ` obs issues prod --expand-incidents       # every issue of an incident, not the first

An INCIDENT is one cause breaking many callers: a dead database fails every
service that touches it, and each logs from its own call site, so one outage is
dozens of issues. Issues that share a cause (the SQLSTATE or refused connection
target their error carries, else its normalized error class) and first fired
within --incident-window of one another, in any service, share an incident_key.
The list collapses them under the first, which shows "+N related"; nothing is
merged and each keeps its own counts and triage state. forge obs issue <id>
lists the rest of its incident.
  ` + Name() + ` obs issues prod --assignee me            # credential's acting person
  ` + Name() + ` obs issues prod --assignee none          # unassigned
  ` + Name() + ` obs issues prod --assignee 7c1e2a90-0b1d-4a61-9e7d-5a1d3c9f2b10

The server resolves "me" from the verified credential. An organization automation
token acts for nobody and is refused FailedPrecondition (exit 2); name a user id.

` + obsIssuesSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsIssues(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, 24*time.Hour, obsquery.DefaultIssueLimit)
	f := cmd.Flags()
	f.StringArrayVar(&o.statuses, "status", nil, "only issues in this state: "+strings.Join(obsbackend.IssueStatuses, ", ")+" (repeatable; default the open ones)")
	f.StringVar(&o.platform, "platform", "", "server | browser")
	f.StringVar(&o.source, "source", "", "list the issues of this issue source (issue.source) instead of errors, e.g. llm_bug_report")
	f.StringVar(&o.sinceRelease, "since-release", "", "only issues first seen in this release version (hosted env)")
	f.StringVar(&o.sinceDeploy, "since-deploy", "", "only issues first seen at or after this promotion id (hosted env)")
	f.StringVar(&o.assignee, "assignee", "", "only issues assigned to this user id, me (credential's acting user), or none (unassigned)")
	f.StringVar(&o.sort, "sort", obsquery.IssueSortLast, "last | first | events")
	f.StringVar(&o.pageToken, "page-token", "", "resume from a next_page_token")
	f.BoolVar(&o.expandIncidents, "expand-incidents", false, "list every issue of an incident instead of collapsing each incident under its first issue")
	f.DurationVar(&o.incidentWindow, "incident-window", obsquery.DefaultIncidentWindow, "issues of one cause first seen within this long of each other are one incident (max "+obsquery.MaxIncidentWindow.String()+")")
	return cmd
}

func (o obsIssuesOpts) validate() error {
	for _, s := range o.statuses {
		if !slices.Contains(obsbackend.IssueStatuses, s) {
			return obsbackend.Wrong("--status %q is not a status; use %s", s, strings.Join(obsbackend.IssueStatuses, ", "))
		}
	}
	switch o.platform {
	case "", obsbackend.PlatformServer, obsbackend.PlatformBrowser:
	default:
		return obsbackend.Wrong("--platform %q is not a platform; use server or browser", o.platform)
	}
	if o.source != "" && !obsquery.ValidIssueSource(o.source) {
		return obsbackend.Wrong("--source %q is not an issue source name (lowercase letters, digits, '_', '.', '-')", o.source)
	}
	if o.source != "" && o.platform == obsbackend.PlatformBrowser {
		return obsbackend.Wrong("--source lists server issue events; it does not combine with --platform browser")
	}
	if o.assignee != "" {
		if _, err := assigneeFilter(o.assignee); err != nil {
			return err
		}
	}
	if !slices.Contains([]string{obsquery.IssueSortLast, obsquery.IssueSortFirst, obsquery.IssueSortEvents}, o.sort) {
		return obsbackend.Wrong("--sort %q is not a sort order; use last, first or events", o.sort)
	}
	if o.incidentWindow <= 0 || o.incidentWindow > obsquery.MaxIncidentWindow {
		return obsbackend.Wrong("--incident-window must be greater than 0 and at most %s", obsquery.MaxIncidentWindow)
	}
	if o.out.Limit < 1 || o.out.Limit > obsquery.MaxIssueLimit {
		return obsbackend.Wrong("--limit must be 1-%d", obsquery.MaxIssueLimit)
	}
	return nil
}

// issueReader is the backend's issue side; a backend without one cannot look.
func issueReader(b obsbackend.Backend) (obsbackend.IssueReader, error) {
	r, ok := b.(obsbackend.IssueReader)
	if !ok {
		return nil, obsbackend.Undetermined("the %s backend serves no issues", b.Info().Backend)
	}
	return r, nil
}

func runObsIssues(ctx context.Context, d obsDeps, env string, o obsIssuesOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsIssuesSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsIssues, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsIssues, err) }
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
	b, _, err := o.open(ctx, d, env, "issues")
	if err != nil {
		return fail(err)
	}
	r, err := issueReader(b)
	if err != nil {
		return fail(err)
	}
	assignee, err := assigneeFilter(o.assignee)
	if err != nil {
		return fail(err)
	}
	unassigned := assignee == "none"
	if unassigned {
		assignee = ""
	}
	res, err := r.ListIssues(ctx, obsbackend.ListIssuesRequest{
		Narrow: n, Statuses: o.statuses, Platform: o.platform, Source: o.source, SinceRelease: o.sinceRelease, SincePromotion: o.sinceDeploy,
		Sort: o.sort, Limit: o.out.Limit, PageToken: pageToken, IncidentWindow: o.incidentWindow,
		Assignee: assignee, Unassigned: unassigned,
	})
	if err != nil {
		return fail(err)
	}
	doc := obsIssuesDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Sort: o.sort, Issues: []obsIssueRowDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	shown := res.Issues
	if !o.expandIncidents {
		shown, doc.Collapsed = collapseIncidents(res.Issues)
	}
	for _, i := range shown {
		doc.Issues = append(doc.Issues, issueRowDoc(i))
	}
	res.NextPageToken = wrapPageToken(n.Range, res.NextPageToken)
	doc.Page = obsout.Page{Truncated: res.Truncated || res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = issuesSuggestions(env, shown)
	doc.Stamp(schemaObsIssues, nil)
	if done, err := o.out.Emit(d.Stdout, obsIssuesSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	if len(res.Issues) == 0 {
		fmt.Fprintln(d.Stdout, "no issues")
		return nil
	}
	header := []string{"ISSUE", "STATUS", "EVENTS", "AFFECTED", "LAST SEEN", "SERVICE", "TITLE"}
	rows := make([][]string, len(shown))
	for i, is := range shown {
		title := is.Title
		if is.RelatedCount > 0 && !o.expandIncidents {
			title += fmt.Sprintf("  (+%d related, incident %s)", is.RelatedCount, is.IncidentKey)
		} else if is.IncidentKey != "" {
			title += fmt.Sprintf("  (incident %s)", is.IncidentKey)
		}
		rows[i] = []string{is.ID, is.Status, strconv.FormatInt(is.Events, 10), affectedText(is.Affected, is.AffectedUnit),
			is.LastSeen.UTC().Format("01-02 15:04:05"), is.Service, title}
	}
	// The assignee column appears only when some issue has one, so a list with
	// none reads exactly as before.
	if slices.ContainsFunc(shown, func(i obsbackend.Issue) bool { return i.Assignee != nil }) {
		header = slices.Insert(header, 6, "ASSIGNEE")
		for i, is := range shown {
			a := "-"
			if is.Assignee != nil {
				a = is.Assignee.ID
			}
			rows[i] = slices.Insert(rows[i], 6, a)
		}
	}
	obsout.Table(d.Stdout, header, rows, 70)
	if doc.Collapsed > 0 {
		fmt.Fprintf(d.Stdout, "\n(%d more issues of the same incidents are collapsed; --expand-incidents lists them)\n", doc.Collapsed)
	}
	if res.Truncated && res.NextPageToken == "" {
		fmt.Fprintf(d.Stdout, "\n(more issues than --limit %d; raise it or narrow with --service)\n", o.out.Limit)
	}
	if res.NextPageToken != "" {
		fmt.Fprintf(d.Stdout, "\n(more: --page-token %s)\n", res.NextPageToken)
	}
	return nil
}

func issuesSuggestions(env string, issues []obsbackend.Issue) []string {
	var cmds []string
	for i, is := range issues {
		if i == 2 {
			break
		}
		cmds = append(cmds, obsout.Command("forge", "obs", "issue", is.ID, env))
	}
	return obsout.Suggest(cmds...)
}

// collapseIncidents keeps the first issue of each incident in list order and
// drops the rest of that incident's issues from the page, counting them.
// Nothing is merged: the hidden issues exist, with their own counts, and
// --expand-incidents lists them.
func collapseIncidents(issues []obsbackend.Issue) ([]obsbackend.Issue, int) {
	seen := map[string]bool{}
	out := make([]obsbackend.Issue, 0, len(issues))
	for _, is := range issues {
		if is.IncidentKey != "" {
			if seen[is.IncidentKey] {
				continue
			}
			seen[is.IncidentKey] = true
		}
		out = append(out, is)
	}
	return out, len(issues) - len(out)
}

// assigneeFilter normalizes reserved CLI spellings, preserving user IDs.
// "me" is resolved by the server from the verified credential, never locally.
func assigneeFilter(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "none", "me":
		return strings.ToLower(s), nil
	}
	if len(s) > 128 {
		return "", obsbackend.Wrong("--assignee is longer than 128 characters")
	}
	return s, nil
}
