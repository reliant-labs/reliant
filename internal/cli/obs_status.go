package cli

// `forge obs status [env]` — what is wrong right now? (DESIGN §8.1)
//
// One document over six independent reads: the per-service overview, the new
// and regressed issues, the active alerts, whether telemetry is arriving (and,
// hosted, whether the control plane's telemetry token reached the cluster),
// and the release in force with the last few changes. The reads run
// concurrently and each degrades on its own: a section that cannot be read is
// left out, named in `unread`, and explained in `warnings`, never a failure of
// the whole. A section the backend does not have (alerts locally) is named in
// `not_evaluated`.
//
// EXIT CODES: 0 when at least one section was read, whatever it says (status
// reports, it does not judge: `forge obs check` is the gate); 1 for a wrong
// request (a bad flag); 2 when nothing could be read, or the env's telemetry
// cannot be reached at all.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsStatus = "forge.obs.status/v1"

var obsStatusSchema = obsout.Schema{
	Name: schemaObsStatus,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services",
		"query_mode",
		"services", "services.service", "services.requests", "services.rate_per_second", "services.errors", "services.error_rate",
		"services.p50_ms", "services.p95_ms", "services.p99_ms", "services.error_logs",
		"issues", "issues.id", "issues.platform", "issues.service", "issues.title", "issues.culprit", "issues.exception_type",
		"issues.status", "issues.first_seen", "issues.last_seen", "issues.first_version", "issues.last_version",
		"issues.events", "issues.affected", "issues.latest_trace_id", "issues.resolved_at", "issues.resolved_version",
		"issues.regressed_at", "issues.ignored_until", "issues_truncated",
		"alerts", "alerts.rule", "alerts.state", "alerts.fingerprint", "alerts.labels.*", "alerts.summary",
		"alerts.active_at", "alerts.firing_at", "alerts.value",
		"telemetry", "telemetry.last_log_at", "telemetry.last_span_at", "telemetry.lookback",
		"telemetry.deliveries_enabled", "telemetry.deliveries", "telemetry.deliveries.cluster", "telemetry.deliveries.namespace",
		"telemetry.deliveries.name", "telemetry.deliveries.state", "telemetry.deliveries.expires_at",
		"telemetry.deliveries.last_verified_at", "telemetry.deliveries.last_error", "telemetry.deliveries.consecutive_failures",
		"current_release", "current_release.version", "current_release.promotion_id", "current_release.promoted_at",
		"current_release.direction", "current_release.actor", "current_release.via", "current_release.unsettled",
		"changes", "changes.kind", "changes.at", "changes.summary", "changes.version", "changes.promotion_id",
		"changes.direction", "changes.verb", "changes.target", "changes.actor", "changes.via",
		"unread", "not_evaluated", "warnings", "suggested",
	},
}

// Status sections, as `unread` and `not_evaluated` name them.
const (
	statusOverview   = "services"
	statusIssues     = "issues"
	statusAlerts     = "alerts"
	statusTelemetry  = "telemetry"
	statusDeliveries = "telemetry.deliveries"
	statusChanges    = "changes"
)

const (
	// statusFreshnessLookback is how far back the newest log and span are
	// looked for, at least: a quiet hour is not "telemetry stopped".
	statusFreshnessLookback = 24 * time.Hour
	// statusChangesLookback bounds the change feed; the release in force is
	// found whatever its age.
	statusChangesLookback = 7 * 24 * time.Hour
	// statusChangesLimit is how many changes the document carries.
	statusChangesLimit = 5
)

// obsStatusTelemetry says whether telemetry is arriving.
type obsStatusTelemetry struct {
	// LastLogAt and LastSpanAt are the newest record of each signal within
	// Lookback; absent when there was none.
	LastLogAt  time.Time `json:"last_log_at,omitzero"`
	LastSpanAt time.Time `json:"last_span_at,omitzero"`
	Lookback   string    `json:"lookback"`
	// DeliveriesEnabled and Deliveries are the control plane's telemetry
	// token deliveries (hosted envs).
	DeliveriesEnabled *bool                          `json:"deliveries_enabled,omitempty"`
	Deliveries        []obsbackend.TelemetryDelivery `json:"deliveries,omitzero"`
}

// obsStatusRelease is the release in force.
type obsStatusRelease struct {
	Version     string    `json:"version"`
	PromotionID string    `json:"promotion_id,omitempty"`
	PromotedAt  time.Time `json:"promoted_at"`
	Direction   string    `json:"direction,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	Via         string    `json:"via,omitempty"`
	// Unsettled says the rollout had not been reported finished.
	Unsettled bool `json:"unsettled,omitempty"`
}

// obsStatusDoc is the document. A section that was read is present, as an
// empty list when there is nothing in it; a section that was not is absent.
type obsStatusDoc struct {
	obsout.Head
	Scope           obsout.Scope                 `json:"scope"`
	QueryMode       string                       `json:"query_mode"`
	Services        []obsbackend.ServiceOverview `json:"services,omitzero"`
	Issues          []obsIssueRowDoc             `json:"issues,omitzero"`
	IssuesTruncated bool                         `json:"issues_truncated,omitempty"`
	Alerts          []obsbackend.Alert           `json:"alerts,omitzero"`
	Telemetry       *obsStatusTelemetry          `json:"telemetry,omitempty"`
	CurrentRelease  *obsStatusRelease            `json:"current_release,omitempty"`
	Changes         []obsbackend.Change          `json:"changes,omitzero"`
	Unread          []string                     `json:"unread,omitempty"`
	NotEvaluated    []string                     `json:"not_evaluated,omitempty"`
	Warnings        []string                     `json:"warnings,omitempty"`
	Suggested       []string                     `json:"suggested,omitempty"`
}

type obsStatusOpts struct {
	out      obsout.Flags
	backend  string
	since    time.Duration
	from, to string
	services []string
}

func newObsStatusCmd() *cobra.Command {
	var o obsStatusOpts
	cmd := &cobra.Command{
		Use:   "status [env]",
		Short: "What is wrong right now? Services, open issues, alerts, telemetry and the release in force",
		Long: `One look at [env] (default dev) over --since (default 1h):

  services         requests, error rate and latency per service, busiest first
  issues           the new and regressed issues (locally: every error group)
  alerts           the alerts firing or pending (hosted envs)
  telemetry        when the newest log and span arrived; hosted, whether the
                   control plane's telemetry token reached the cluster
  current_release  the promotion in force, and the last few changes

Each section is read on its own: one the backend cannot answer is listed in
` + "`unread`" + ` with the reason in ` + "`warnings`" + `, and the rest are still shown. The exit
status is 0 whenever anything was read; status reports, it does not judge (use
` + "`" + Name() + ` obs check` + "`" + ` to gate a release).

  ` + Name() + ` obs status prod
  ` + Name() + ` obs status dev --since 15m --service api
  ` + Name() + ` obs status prod --json --fields services,issues.id,issues.title

` + obsStatusSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsStatus(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	f := cmd.Flags()
	o.out.Register(f, 10)
	f.StringVar(&o.backend, "backend", "", "local | cloud (default: from the env's declaration)")
	f.DurationVar(&o.since, "since", time.Hour, "how far back to read (ignored with --from)")
	f.StringVar(&o.from, "from", "", "range start, RFC 3339")
	f.StringVar(&o.to, "to", "", "range end, RFC 3339 (default now)")
	f.StringArrayVar(&o.services, "service", nil, "only this service.name (repeatable)")
	return cmd
}

// statusSection is one read of the document: it fills its own fields of doc
// and returns why it could not.
type statusSection struct {
	name string
	read func(ctx context.Context, doc *obsStatusDoc) error
}

func runObsStatus(ctx context.Context, d obsDeps, env string, o obsStatusOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsStatusSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsStatus, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsStatus, err) }
	rng, err := resolveRange(d.Now(), o.since, o.from, o.to)
	if err != nil {
		return fail(err)
	}
	b, _, err := (&obsCommon{backend: o.backend}).open(ctx, d, env, "status")
	if err != nil {
		return fail(err)
	}
	info := b.Info()
	narrow := obsbackend.Narrow{Range: rng, Services: o.services}
	doc := obsStatusDoc{Scope: obsScope(info, narrow), QueryMode: QueryModeStructured}

	sections, notEvaluated := statusSections(b, env, narrow, o.out.Limit)
	doc.NotEvaluated = notEvaluated
	errs := make([]error, len(sections))
	parts := make([]obsStatusDoc, len(sections))
	var wg sync.WaitGroup
	for i, s := range sections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.read(ctx, &parts[i])
		}()
	}
	wg.Wait()

	var first error
	read := 0
	for i, s := range sections {
		mergeStatusPart(&doc, parts[i])
		if errs[i] != nil {
			if first == nil {
				first = errs[i]
			}
			doc.Unread = append(doc.Unread, s.name)
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("%s: %v", s.name, errs[i]))
			continue
		}
		read++
	}
	if read == 0 {
		return fail(obsbackend.Undetermined("nothing about %s could be read", env).Cause(first).With(obsbackend.SuggestedOf(first)...))
	}
	doc.Suggested = statusSuggestions(env, doc)
	doc.Stamp(schemaObsStatus, nil)
	if done, err := o.out.Emit(d.Stdout, obsStatusSchema, doc, fields); done || err != nil {
		return err
	}
	writeStatusText(d.Stdout, d.Now(), doc)
	for _, w := range doc.Warnings {
		fmt.Fprintf(d.Stderr, "warning: %s\n", w)
	}
	return nil
}

// statusSections are the reads b can answer, and the sections it cannot.
func statusSections(b obsbackend.Backend, env string, n obsbackend.Narrow, issueLimit int) ([]statusSection, []string) {
	var secs []statusSection
	var notEvaluated []string
	if r, ok := b.(obsbackend.OverviewReader); ok {
		secs = append(secs, statusSection{statusOverview, func(ctx context.Context, doc *obsStatusDoc) error {
			res, err := r.Overview(ctx, obsbackend.OverviewRequest{Narrow: n})
			if err != nil {
				return err
			}
			doc.Services = append([]obsbackend.ServiceOverview{}, res.Services...)
			return nil
		}})
	} else {
		notEvaluated = append(notEvaluated, statusOverview)
	}
	if r, ok := b.(obsbackend.IssueReader); ok {
		secs = append(secs, statusSection{statusIssues, func(ctx context.Context, doc *obsStatusDoc) error {
			res, err := r.ListIssues(ctx, obsbackend.ListIssuesRequest{
				Narrow: n, Statuses: []string{obsbackend.IssueNew, obsbackend.IssueRegressed},
				Sort: obsquery.IssueSortLast, Limit: issueLimit,
			})
			if err != nil {
				return err
			}
			doc.Issues = []obsIssueRowDoc{}
			for _, is := range res.Issues {
				doc.Issues = append(doc.Issues, issueRowDoc(is))
			}
			doc.IssuesTruncated = res.Truncated || res.NextPageToken != ""
			return nil
		}})
	} else {
		notEvaluated = append(notEvaluated, statusIssues)
	}
	if r, ok := b.(obsbackend.AlertReader); ok {
		secs = append(secs, statusSection{statusAlerts, func(ctx context.Context, doc *obsStatusDoc) error {
			alerts, err := r.Alerts(ctx)
			if err != nil {
				return err
			}
			doc.Alerts = append([]obsbackend.Alert{}, alerts...)
			return nil
		}})
	} else {
		notEvaluated = append(notEvaluated, statusAlerts)
	}
	secs = append(secs, statusSection{statusTelemetry, func(ctx context.Context, doc *obsStatusDoc) error {
		t, warnings, err := statusFreshness(ctx, b, n)
		if err != nil {
			return err
		}
		doc.Telemetry, doc.Warnings = t, warnings
		return nil
	}})
	if r, ok := b.(obsbackend.DeliveryReader); ok {
		secs = append(secs, statusSection{statusDeliveries, func(ctx context.Context, doc *obsStatusDoc) error {
			res, err := r.TelemetryDeliveries(ctx)
			if err != nil {
				return err
			}
			enabled := res.Enabled
			doc.Telemetry = &obsStatusTelemetry{DeliveriesEnabled: &enabled, Deliveries: append([]obsbackend.TelemetryDelivery{}, res.Deliveries...)}
			return nil
		}})
	}
	if r, ok := b.(obsbackend.ChangesReader); ok {
		secs = append(secs, statusSection{statusChanges, func(ctx context.Context, doc *obsStatusDoc) error {
			res, err := r.Changes(ctx, obsbackend.ChangesRequest{
				Env: env, Range: obsbackend.Range{From: n.Range.To.Add(-statusChangesLookback), To: n.Range.To},
				Limit: statusChangesLimit, Project: hostedProjectName(),
			})
			if err != nil {
				return err
			}
			doc.Changes = append([]obsbackend.Change{}, res.Changes...)
			if c := res.Current; c != nil {
				doc.CurrentRelease = &obsStatusRelease{
					Version: c.Version, PromotionID: c.PromotionID, PromotedAt: c.At, Direction: c.Direction,
					Actor: c.Actor, Via: c.Via, Unsettled: res.CurrentUnsettled,
				}
			}
			for _, w := range res.Warnings {
				doc.Warnings = append(doc.Warnings, statusChanges+": "+w)
			}
			return nil
		}})
	} else {
		notEvaluated = append(notEvaluated, statusChanges)
	}
	return secs, notEvaluated
}

// statusFreshness finds the newest log and span within the lookback (at
// least statusFreshnessLookback, and never shorter than the range). Either
// signal alone answers, and the other's failure is a warning; the section
// fails only when both reads do.
func statusFreshness(ctx context.Context, b obsbackend.Backend, n obsbackend.Narrow) (*obsStatusTelemetry, []string, error) {
	look := max(statusFreshnessLookback, n.Range.To.Sub(n.Range.From))
	fn := obsbackend.Narrow{Range: obsbackend.Range{From: n.Range.To.Add(-look), To: n.Range.To}, Services: n.Services}
	t := &obsStatusTelemetry{Lookback: look.String()}
	logs, lerr := b.Logs(ctx, obsbackend.LogsRequest{Narrow: fn, Limit: 1})
	if lerr == nil && len(logs.Logs) > 0 {
		t.LastLogAt = logs.Logs[0].Time
	}
	traces, terr := b.Traces(ctx, obsbackend.TracesRequest{Narrow: fn, Limit: 1})
	if terr == nil && len(traces.Traces) > 0 {
		t.LastSpanAt = traces.Traces[0].Start
	}
	switch {
	case lerr != nil && terr != nil:
		return nil, nil, lerr
	case lerr != nil:
		return t, []string{fmt.Sprintf("%s: the newest log is unknown: %v", statusTelemetry, lerr)}, nil
	case terr != nil:
		return t, []string{fmt.Sprintf("%s: the newest span is unknown: %v", statusTelemetry, terr)}, nil
	}
	return t, nil, nil
}

// mergeStatusPart copies what one section read into doc. Sections fill
// disjoint fields, except telemetry, which the freshness and delivery reads
// share.
func mergeStatusPart(doc *obsStatusDoc, p obsStatusDoc) {
	if p.Services != nil {
		doc.Services = p.Services
	}
	if p.Issues != nil {
		doc.Issues, doc.IssuesTruncated = p.Issues, p.IssuesTruncated
	}
	if p.Alerts != nil {
		doc.Alerts = p.Alerts
	}
	if t := p.Telemetry; t != nil {
		if doc.Telemetry == nil {
			doc.Telemetry = &obsStatusTelemetry{}
		}
		if t.Lookback != "" {
			doc.Telemetry.LastLogAt, doc.Telemetry.LastSpanAt, doc.Telemetry.Lookback = t.LastLogAt, t.LastSpanAt, t.Lookback
		}
		if t.DeliveriesEnabled != nil {
			doc.Telemetry.DeliveriesEnabled, doc.Telemetry.Deliveries = t.DeliveriesEnabled, t.Deliveries
		}
	}
	if p.Changes != nil {
		doc.Changes = p.Changes
	}
	if p.CurrentRelease != nil {
		doc.CurrentRelease = p.CurrentRelease
	}
	doc.Warnings = append(doc.Warnings, p.Warnings...)
}

// statusSuggestions are the next commands: the newest issue, the failing
// endpoints when a service is erroring, and the release in force's check.
func statusSuggestions(env string, doc obsStatusDoc) []string {
	var cmds []string
	if len(doc.Issues) > 0 {
		cmds = append(cmds, obsout.Command("forge", "obs", "issue", doc.Issues[0].ID, env))
	}
	for _, s := range doc.Services {
		if s.Errors > 0 {
			cmds = append(cmds, obsout.Command("forge", "obs", "endpoints", env, "--service", s.Service, "--sort", "errors"))
			break
		}
	}
	if r := doc.CurrentRelease; r != nil && r.PromotionID != "" {
		cmds = append(cmds, obsout.Command("forge", "obs", "check", env, "--promotion", r.PromotionID))
	}
	return obsout.Suggest(cmds...)
}

// ── text ────────────────────────────────────────────────────────────────

func writeStatusText(w io.Writer, now time.Time, doc obsStatusDoc) {
	fmt.Fprintf(w, "%s (%s backend), %s .. %s\n", doc.Scope.Env, doc.Scope.Backend,
		doc.Scope.From.Format(time.RFC3339), doc.Scope.To.Format(time.RFC3339))

	if r := doc.CurrentRelease; r != nil {
		line := fmt.Sprintf("release %s, promoted %s ago", r.Version, ago(now, r.PromotedAt))
		if r.Actor != "" {
			line += " by " + r.Actor
		}
		if r.Unsettled {
			line += " (rollout not reported finished)"
		}
		fmt.Fprintln(w, line)
	} else if doc.Changes != nil {
		fmt.Fprintln(w, "no promotion on record")
	}
	if t := doc.Telemetry; t != nil {
		writeStatusTelemetry(w, now, t)
	}

	if doc.Services != nil {
		fmt.Fprintln(w)
		if len(doc.Services) == 0 {
			fmt.Fprintln(w, "no service traffic in the range")
		} else {
			rows := make([][]string, len(doc.Services))
			for i, s := range doc.Services {
				rows[i] = []string{s.Service, fmt.Sprintf("%.2f/s", s.RatePerSecond), fmt.Sprintf("%.1f%%", s.ErrorRate*100),
					fmt.Sprintf("%.0fms", s.P95Ms), fmt.Sprint(s.ErrorLogs)}
			}
			obsout.Table(w, []string{"SERVICE", "RATE", "ERRORS", "P95", "ERROR LOGS"}, rows, 40)
		}
	}

	if doc.Issues != nil {
		fmt.Fprintln(w)
		if len(doc.Issues) == 0 {
			fmt.Fprintln(w, "no new or regressed issues")
		} else {
			rows := make([][]string, len(doc.Issues))
			for i, is := range doc.Issues {
				rows[i] = []string{is.ID, is.Status, is.Service, fmt.Sprint(is.Events), ago(now, is.LastSeen), is.Title}
			}
			obsout.Table(w, []string{"ISSUE", "STATUS", "SERVICE", "EVENTS", "LAST SEEN", "TITLE"}, rows, 60)
			if doc.IssuesTruncated {
				fmt.Fprintln(w, "(more; --limit to show them)")
			}
		}
	}

	if doc.Alerts != nil {
		fmt.Fprintln(w)
		if len(doc.Alerts) == 0 {
			fmt.Fprintln(w, "no alerts firing")
		} else {
			for _, a := range doc.Alerts {
				line := fmt.Sprintf("%-8s %s", strings.ToUpper(a.State), a.Rule)
				if a.Summary != "" {
					line += ": " + a.Summary
				}
				fmt.Fprintln(w, line)
			}
		}
	}

	if len(doc.Changes) > 0 {
		fmt.Fprintln(w)
		rows := make([][]string, len(doc.Changes))
		for i, c := range doc.Changes {
			rows[i] = []string{c.At.UTC().Format("01-02 15:04"), c.Summary, firstNonEmptyStr(c.Actor, c.Via)}
		}
		obsout.Table(w, []string{"CHANGED", "WHAT", "BY"}, rows, 60)
	}
	if len(doc.NotEvaluated) > 0 {
		fmt.Fprintf(w, "\nnot evaluated on this backend: %s\n", strings.Join(doc.NotEvaluated, ", "))
	}
}

func writeStatusTelemetry(w io.Writer, now time.Time, t *obsStatusTelemetry) {
	if t.Lookback != "" {
		switch {
		case t.LastLogAt.IsZero() && t.LastSpanAt.IsZero():
			fmt.Fprintf(w, "telemetry: nothing received in the last %s\n", t.Lookback)
		default:
			var parts []string
			if !t.LastLogAt.IsZero() {
				parts = append(parts, "last log "+ago(now, t.LastLogAt)+" ago")
			}
			if !t.LastSpanAt.IsZero() {
				parts = append(parts, "last span "+ago(now, t.LastSpanAt)+" ago")
			}
			fmt.Fprintf(w, "telemetry: %s\n", strings.Join(parts, ", "))
		}
	}
	if t.DeliveriesEnabled != nil && !*t.DeliveriesEnabled {
		fmt.Fprintln(w, "telemetry token: the control plane's delivery reconciler is off; nothing keeps the token valid")
	}
	for _, dl := range t.Deliveries {
		if dl.State == obsbackend.DeliveryDelivered || dl.State == obsbackend.DeliveryWithdrawn {
			continue
		}
		line := fmt.Sprintf("telemetry token %s/%s on %s: %s", dl.Namespace, dl.Name, dl.Cluster, dl.State)
		if dl.LastError != "" {
			line += " (" + dl.LastError + ")"
		}
		fmt.Fprintln(w, line)
	}
}

// ago renders the time since t, coarsely.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return d.Truncate(time.Second).String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
