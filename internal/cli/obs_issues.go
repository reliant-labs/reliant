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
		"issues", "issues.id", "issues.platform", "issues.service", "issues.title", "issues.culprit", "issues.exception_type",
		"issues.status", "issues.first_seen", "issues.last_seen", "issues.first_version", "issues.last_version",
		"issues.events", "issues.affected", "issues.latest_trace_id", "issues.resolved_at", "issues.resolved_version",
		"issues.regressed_at", "issues.ignored_until",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

// obsIssueRowDoc is one issue in a list, and the issue of an issue document.
type obsIssueRowDoc struct {
	ID              string    `json:"id"`
	Platform        string    `json:"platform"`
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
	LatestTraceID   string    `json:"latest_trace_id,omitempty"`
	ResolvedAt      time.Time `json:"resolved_at,omitzero"`
	ResolvedVersion string    `json:"resolved_version,omitempty"`
	RegressedAt     time.Time `json:"regressed_at,omitzero"`
	IgnoredUntil    time.Time `json:"ignored_until,omitzero"`
}

func issueRowDoc(i obsbackend.Issue) obsIssueRowDoc {
	return obsIssueRowDoc{
		ID: i.ID, Platform: i.Platform, Service: i.Service, Title: i.Title, Culprit: i.Culprit, ExceptionType: i.ExceptionType,
		Status: i.Status, FirstSeen: i.FirstSeen, LastSeen: i.LastSeen, FirstVersion: i.FirstVersion, LastVersion: i.LastVersion,
		Events: i.Events, Affected: i.Affected, LatestTraceID: i.LatestTraceID,
		ResolvedAt: zeroUnixEpoch(i.ResolvedAt), ResolvedVersion: i.ResolvedVersion,
		RegressedAt: zeroUnixEpoch(i.RegressedAt), IgnoredUntil: zeroUnixEpoch(i.IgnoredUntil),
	}
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
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Sort       string                 `json:"sort"`
	Issues     []obsIssueRowDoc       `json:"issues"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	obsout.Page
	Suggested []string `json:"suggested,omitempty"`
}

type obsIssuesOpts struct {
	obsCommon
	statuses     []string
	platform     string
	sinceRelease string
	sinceDeploy  string
	sort         string
	pageToken    string
}

func newObsIssuesCmd() *cobra.Command {
	var o obsIssuesOpts
	cmd := &cobra.Command{
		Use:   "issues [env]",
		Short: "Grouped errors: what is failing, how often, since which release",
		Long: `List the env's error issues: ERROR logs grouped by fingerprint (sfp1), with how
often each fired, how many traces it touched and when. [env] defaults to dev.

On a hosted env the control plane keeps the issues, so they have a status
(new, unresolved, resolved, regressed, ignored) that survives log retention, and
--since-release / --since-deploy ask "what did this release open". On a local
env they are grouped at query time over the local ClickStack: no state, every
issue is unresolved, and the counts are those of the range.

  ` + Name() + ` obs issues prod                          # the open ones, last 24h
  ` + Name() + ` obs issues prod --status regressed
  ` + Name() + ` obs issues prod --since-release v1.4.0   # first seen in that release
  ` + Name() + ` obs issues dev --service api --sort events

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
	f.StringVar(&o.sinceRelease, "since-release", "", "only issues first seen in this release version (hosted env)")
	f.StringVar(&o.sinceDeploy, "since-deploy", "", "only issues first seen at or after this promotion id (hosted env)")
	f.StringVar(&o.sort, "sort", obsquery.IssueSortLast, "last | first | events")
	f.StringVar(&o.pageToken, "page-token", "", "resume from a next_page_token")
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
	if !slices.Contains([]string{obsquery.IssueSortLast, obsquery.IssueSortFirst, obsquery.IssueSortEvents}, o.sort) {
		return obsbackend.Wrong("--sort %q is not a sort order; use last, first or events", o.sort)
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
	b, _, err := o.open(ctx, d, env, "issues")
	if err != nil {
		return fail(err)
	}
	r, err := issueReader(b)
	if err != nil {
		return fail(err)
	}
	res, err := r.ListIssues(ctx, obsbackend.ListIssuesRequest{
		Narrow: n, Statuses: o.statuses, Platform: o.platform, SinceRelease: o.sinceRelease, SincePromotion: o.sinceDeploy,
		Sort: o.sort, Limit: o.out.Limit, PageToken: o.pageToken,
	})
	if err != nil {
		return fail(err)
	}
	doc := obsIssuesDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Sort: o.sort, Issues: []obsIssueRowDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, i := range res.Issues {
		doc.Issues = append(doc.Issues, issueRowDoc(i))
	}
	doc.Page = obsout.Page{Truncated: res.Truncated || res.NextPageToken != "", NextPageToken: res.NextPageToken}
	doc.Suggested = issuesSuggestions(env, res.Issues)
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
	rows := make([][]string, len(res.Issues))
	for i, is := range res.Issues {
		rows[i] = []string{is.ID, is.Status, strconv.FormatInt(is.Events, 10), strconv.FormatInt(is.Affected, 10),
			is.LastSeen.UTC().Format("01-02 15:04:05"), is.Service, is.Title}
	}
	obsout.Table(d.Stdout, []string{"ISSUE", "STATUS", "EVENTS", "AFFECTED", "LAST SEEN", "SERVICE", "TITLE"}, rows, 70)
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
