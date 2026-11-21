package obsbackend

// Release health and the change feed over the control plane. The wire shapes
// are declared locally (protojson: camelCase, int64 as a string); the field
// names are those of control-plane proto/services/observability/v1 and
// proto/services/activity/v1.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

const (
	procGetReleaseHealth = "controlplane.v1.ObservabilityService/GetReleaseHealth"
	procListActivity     = "controlplane.v1.ActivityService/ListActivity"
)

type wireHealthStats struct {
	Requests  jsonInt `json:"requests"`
	ErrorRate float64 `json:"errorRate"`
	P50Ms     float64 `json:"p50Ms"`
	P95Ms     float64 `json:"p95Ms"`
	P99Ms     float64 `json:"p99Ms"`
}

type wireHealthIssue struct {
	ID           string    `json:"id"`
	Service      string    `json:"service"`
	Title        string    `json:"title"`
	Culprit      string    `json:"culprit"`
	Events       jsonInt   `json:"events"`
	FirstSeenAt  time.Time `json:"firstSeenAt"`
	FirstVersion string    `json:"firstVersion"`
	RegressedAt  time.Time `json:"regressedAt"`
}

// wireAlertRef is ReleaseAlertRef.
type wireAlertRef struct {
	Rule            string            `json:"rule"`
	Fingerprint     string            `json:"fingerprint"`
	FiringAt        time.Time         `json:"firingAt"`
	Labels          map[string]string `json:"labels"`
	BaselineFirings int               `json:"baselineFirings"`
}

func (a wireAlertRef) alert() releasehealth.Alert {
	return releasehealth.Alert{Rule: a.Rule, Fingerprint: a.Fingerprint, FiringAt: a.FiringAt.UTC(), Labels: a.Labels, BaselineFirings: a.BaselineFirings}
}

type wireReleaseHealth struct {
	Promotion struct {
		ID                     string    `json:"id"`
		ReleaseVersion         string    `json:"releaseVersion"`
		GitCommit              string    `json:"gitCommit"`
		PromotedAt             time.Time `json:"promotedAt"`
		PreviousReleaseVersion string    `json:"previousReleaseVersion"`
		// Inferred and MarkerSource: a control plane that judged a version
		// no promotion carried, from its first sighting in telemetry.
		Inferred     bool   `json:"inferred"`
		MarkerSource string `json:"markerSource"`
		// LiveAt: when the release went live, from the promotion's deploy
		// gates (releasehealth.LiveAt). A control plane that sends it
		// started the after window there when it is later than promotedAt
		// (control-plane#2754), so a retried promotion is judged on what it
		// served, not on its failed attempt's rollout (forge#2268). Zero
		// from an older control plane, which anchored at promotedAt.
		LiveAt time.Time `json:"liveAt"`
	} `json:"promotion"`
	Before        wireTimeRange `json:"before"`
	After         wireTimeRange `json:"after"`
	AfterComplete bool          `json:"afterComplete"`
	Services      []struct {
		Service      string          `json:"service"`
		Before       wireHealthStats `json:"before"`
		After        wireHealthStats `json:"after"`
		VersionShare *float64        `json:"versionShare"`
	} `json:"services"`
	NewIssues       []wireHealthIssue `json:"newIssues"`
	RegressedIssues []wireHealthIssue `json:"regressedIssues"`
	FiringAlerts    []wireAlertRef    `json:"firingAlerts"`
	// BaselineAlerts: alerts that were flapping before the promotion, so
	// their alert_firing findings are warn and say why in unattributed.
	// Empty from an older control plane, which counted every alert.
	BaselineAlerts   []wireAlertRef `json:"baselineAlerts"`
	WorkloadFindings []struct {
		Workload             string  `json:"workload"`
		Restarts             jsonInt `json:"restarts"`
		LastTerminatedReason string  `json:"lastTerminatedReason"`
	} `json:"workloadFindings"`
	Verdict  string `json:"verdict"`
	Findings []struct {
		Code      string  `json:"code"`
		Service   string  `json:"service"`
		Value     float64 `json:"value"`
		Threshold float64 `json:"threshold"`
		Policy    string  `json:"policy"`
		Message   string  `json:"message"`
		// Unattributed says why the finding is not the release's (an
		// alert already flapping before it): Rejudge never fails on one.
		Unattributed string `json:"unattributed"`
	} `json:"findings"`
	Thresholds []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Source string `json:"source"`
	} `json:"thresholds"`
	NotEvaluated []string `json:"notEvaluated"`
}

// ReleaseHealth implements HealthReader: GetReleaseHealth for the env and
// promotion, mapped onto a releasehealth.Report. The control plane judges with
// the env's recorded policy; the caller re-judges a different --fail-on with
// releasehealth.Rejudge.
func (c Cloud) ReleaseHealth(ctx context.Context, req HealthRequest) (HealthResult, error) {
	o := req.Options
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	// scope.time_range is ignored by the server (the windows come from the
	// promotion) but is a required field of QueryScope.
	//
	// A release is the environment's, and the control plane judges its health
	// over the whole environment: a dev instance is never promoted, so
	// GetReleaseHealth drops any instance narrowing and echoes none. The
	// request therefore carries none, and is sent env-wide whatever the
	// backend's instance narrowing — narrowing it made every check of an
	// env on hosted dev telemetry exit 2 waiting for an echo that never comes.
	scope := c.scope(Narrow{Range: Range{From: now.Add(-time.Hour), To: now}})
	scope.InstanceIDs, scope.InstanceUsers = nil, nil
	body := map[string]any{"scope": scope}
	if o.Window > 0 {
		body["window"] = fmt.Sprintf("%ds", int64(o.Window.Seconds()))
	}
	switch {
	case req.PromotionID != "":
		body["promotionId"] = req.PromotionID
	case req.Release != "":
		body["releaseVersion"] = req.Release
	}
	if ov := cloudOverrides(o.Overrides); len(ov) > 0 {
		body["thresholdOverrides"] = ov
	}
	var w wireReleaseHealth
	if err := c.callEnvWide(ctx, procGetReleaseHealth, body, &w); err != nil {
		if unimplemented(err) {
			return HealthResult{}, Undetermined("the control plane does not serve release health yet (GetReleaseHealth is unimplemented); nothing was judged").Cause(err)
		}
		return HealthResult{}, err
	}
	rep, err := reportFromWire(w)
	if err != nil {
		return HealthResult{}, err
	}
	return HealthResult{Report: rep}, nil
}

// cloudOverrides are the --threshold overrides the control plane is sent:
// all but the changed-op thresholds. A control plane that predates them
// refuses an unknown name, which would cost the whole check; the caller
// applies them itself (releasehealth.WithChangedOps).
func cloudOverrides(overrides map[string]string) map[string]string {
	out := make(map[string]string, len(overrides))
	for k, v := range overrides {
		if !slices.Contains(releasehealth.ChangedOpThresholds, k) {
			out[k] = v
		}
	}
	return out
}

func reportFromWire(w wireReleaseHealth) (releasehealth.Report, error) {
	r := releasehealth.Report{
		Subject: releasehealth.Subject{
			PromotionID: w.Promotion.ID, Release: w.Promotion.ReleaseVersion, GitCommit: w.Promotion.GitCommit,
			PromotedAt: w.Promotion.PromotedAt.UTC(), PreviousRelease: w.Promotion.PreviousReleaseVersion,
			Inferred: w.Promotion.Inferred, MarkerSource: w.Promotion.MarkerSource,
		},
		Before:        releasehealth.Range{From: w.Before.Start.UTC(), To: w.Before.End.UTC()},
		After:         releasehealth.Range{From: w.After.Start.UTC(), To: w.After.End.UTC()},
		AfterComplete: w.AfterComplete,
		Services:      []releasehealth.ServiceHealth{},
		Findings:      []releasehealth.Finding{},
	}
	switch strings.TrimPrefix(w.Verdict, "RELEASE_VERDICT_") {
	case "PASS":
		r.Verdict = releasehealth.Pass
	case "FAIL":
		r.Verdict = releasehealth.Fail
	case "INCONCLUSIVE":
		r.Verdict = releasehealth.Inconclusive
	default:
		return r, Undetermined("the control plane returned no release-health verdict (%q)", w.Verdict)
	}
	stats := func(s wireHealthStats) releasehealth.WindowStats {
		return releasehealth.WindowStats{Requests: int64(s.Requests), ErrorRate: s.ErrorRate, P50Ms: s.P50Ms, P95Ms: s.P95Ms, P99Ms: s.P99Ms}
	}
	for _, s := range w.Services {
		r.Services = append(r.Services, releasehealth.ServiceHealth{Service: s.Service, Before: stats(s.Before), After: stats(s.After), VersionShare: s.VersionShare})
	}
	issue := func(i wireHealthIssue) releasehealth.Issue {
		return releasehealth.Issue{ID: i.ID, Service: i.Service, Title: i.Title, Culprit: i.Culprit, Events: int64(i.Events),
			FirstSeenAt: i.FirstSeenAt.UTC(), FirstVersion: i.FirstVersion, RegressedAt: i.RegressedAt.UTC()}
	}
	for _, i := range w.NewIssues {
		r.NewIssues = append(r.NewIssues, issue(i))
	}
	for _, i := range w.RegressedIssues {
		r.RegressedIssues = append(r.RegressedIssues, issue(i))
	}
	for _, a := range w.FiringAlerts {
		r.FiringAlerts = append(r.FiringAlerts, a.alert())
	}
	for _, a := range w.BaselineAlerts {
		r.BaselineAlerts = append(r.BaselineAlerts, a.alert())
	}
	for _, f := range w.WorkloadFindings {
		r.Workloads = append(r.Workloads, releasehealth.Workload{Workload: f.Workload, Restarts: int64(f.Restarts), LastTerminatedReason: f.LastTerminatedReason})
	}
	for _, f := range w.Findings {
		code := strings.ToLower(strings.TrimPrefix(f.Code, "RELEASE_FINDING_CODE_"))
		if code == "" || code == "unspecified" {
			continue
		}
		r.Findings = append(r.Findings, releasehealth.Finding{Code: code, Service: f.Service, Value: f.Value, Threshold: f.Threshold, Policy: f.Policy, Message: f.Message, Unattributed: f.Unattributed})
	}
	for _, t := range w.Thresholds {
		r.Thresholds = append(r.Thresholds, releasehealth.Threshold{Name: t.Name, Value: t.Value, Source: t.Source})
	}
	r.NotEvaluated = w.NotEvaluated
	if !w.Promotion.LiveAt.IsZero() {
		r.Subject.LiveAt = w.Promotion.LiveAt.UTC()
	}
	return r, nil
}

const procListGates = "controlplane.v1.DeployService/ListGates"

type wirePromotionGate struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	RunID      string     `json:"runId"`
	RecordedAt *time.Time `json:"recordedAt"`
}

// PromotionGates reads every gate recorded on a promotion, the pre-promote
// ones and those appended after it: what says when an attempt of it shipped
// (releasehealth.LiveAt). The read is about the promotion, not the env's
// telemetry, so an instance narrowing does not apply to it.
func (c Cloud) PromotionGates(ctx context.Context, promotionID string) ([]release.Gate, error) {
	var out struct {
		Gates []wirePromotionGate `json:"gates"`
	}
	if err := c.callEnvWide(ctx, procListGates, map[string]any{"promotionId": promotionID}, &out); err != nil {
		return nil, err
	}
	gates := make([]release.Gate, 0, len(out.Gates))
	for _, w := range out.Gates {
		status, raw := release.GateStatusFromStored(w.Status)
		gates = append(gates, release.Gate{Name: w.Name, Status: status, RawStatus: raw,
			StartedAt: w.StartedAt, FinishedAt: w.FinishedAt, RunID: w.RunID, RecordedAt: w.RecordedAt})
	}
	return gates, nil
}

type wireActivityEvent struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Actor struct {
		Name string `json:"name"`
	} `json:"actor"`
	Verb   string `json:"verb"`
	Target struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"target"`
	Via string `json:"via"`
}

// changesPageSize is the activity page asked for; the feed is capped by
// ChangesRequest.Limit after the merge.
const changesPageSize = 200

// changesMaxPages bounds how many activity pages one read follows. Past it
// the feed is reported truncated, never silently short.
const changesMaxPages = 25

// Changes implements ChangesReader: the env's promotions and failed deploys
// (internal/releasetimeline over the control plane's deploy ledger) merged
// with its activity feed. A source that cannot be read costs its rows, not the
// feed, and says so in Warnings.
func (c Cloud) Changes(ctx context.Context, req ChangesRequest) (ChangesResult, error) {
	var res ChangesResult
	src := releasetimeline.NewCloud(c.Caller, req.Project)
	data, err := src.Load(ctx, c.Env, req.Range.From)
	if err != nil {
		return res, MapCloudError(err)
	}
	res.Warnings = append(res.Warnings, data.Warnings...)
	tl := releasetimeline.Assemble(data, req.Range.From, 0)
	res.current(tl, req.Range.To)
	for _, e := range tl.Entries {
		if !req.Range.To.IsZero() && e.At.After(req.Range.To) {
			continue
		}
		res.Changes = append(res.Changes, changeOfEntry(e))
	}

	scope := map[string]any{
		"orgId": c.OrgID, "envIds": []string{c.EnvID},
		"timeRange": map[string]any{"start": req.Range.From.UTC(), "end": req.Range.To.UTC()},
	}
	if req.Project != "" {
		scope["projectId"] = req.Project
	}
	events, more, err := c.activityPages(ctx, scope, req.Limit)
	if err != nil {
		note := fmt.Sprintf("activity unreadable, only the deploy ledger is shown: %v", err)
		if unimplemented(err) {
			note = "the control plane does not serve activity yet; only the deploy ledger is shown"
		}
		if len(events) > 0 {
			note = fmt.Sprintf("activity only partly read (%d events), the rest is missing: %v", len(events), err)
		}
		res.Warnings = append(res.Warnings, note)
	}
	for _, ev := range events {
		// A promotion is already a ledger row; its activity event only adds
		// who and how, which the timeline merged.
		if ev.Verb == "promote" {
			continue
		}
		target := ev.Target.Kind
		if id := firstNonEmpty(ev.Target.Label, ev.Target.ID); id != "" {
			target += " " + id
		}
		res.Changes = append(res.Changes, Change{
			Kind: ChangeActivity, At: ev.At.UTC(), Verb: ev.Verb, Target: strings.TrimSpace(target),
			Actor: ev.Actor.Name, Via: ev.Via,
			Summary: strings.TrimSpace(fmt.Sprintf("%s %s", strings.ReplaceAll(ev.Verb, "_", " "), target)),
		})
	}
	sortChanges(&res, req.Limit)
	if more && res.Omitted == 0 {
		// Activity older than the pages read exists in the range: the feed
		// is short by an unknown count, and must not read as complete
		// (forge report 0108cca3: ~200 items with truncated:false).
		res.Omitted = 1
		res.Warnings = append(res.Warnings, fmt.Sprintf("activity has more events in the range than the %d read; raise --limit, or narrow --from/--to", len(events)))
	}
	return res, nil
}

// activityPages reads the activity feed newest first, following
// next_page_token until it holds limit events (every event when limit is 0)
// or the feed ends. more reports a page left unread. Only the newest limit
// changes survive the merge, so events past limit are never needed.
func (c Cloud) activityPages(ctx context.Context, scope map[string]any, limit int) (events []wireActivityEvent, more bool, err error) {
	token := ""
	for page := 0; ; page++ {
		if page == changesMaxPages {
			return events, true, nil
		}
		body := map[string]any{"scope": scope, "pageSize": changesPageSize}
		if token != "" {
			body["pageToken"] = token
		}
		var act struct {
			Events        []wireActivityEvent `json:"events"`
			NextPageToken string              `json:"nextPageToken"`
		}
		if err := c.callEnvWide(ctx, procListActivity, body, &act); err != nil {
			return events, false, err
		}
		events = append(events, act.Events...)
		token = act.NextPageToken
		if token == "" {
			return events, false, nil
		}
		if limit > 0 && len(events) >= limit {
			return events, true, nil
		}
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// changeOfEntry renders a timeline row as a feed row.
func changeOfEntry(e releasetimeline.Entry) Change {
	ch := Change{At: e.At, Version: e.Version, PromotionID: e.PromotionID, Actor: e.Actor, Via: e.Via, Recorded: e.Recorded}
	switch e.Kind {
	case releasetimeline.KindFailedDeploy:
		ch.Kind = ChangeFailedDeploy
		why := ""
		if e.Rollout != nil {
			why = firstNonEmpty(e.Rollout.Reason, e.Rollout.Status)
		}
		ch.Summary = strings.TrimSpace(fmt.Sprintf("deploy of %s failed %s", e.Version, why))
	default:
		ch.Kind = ChangePromotion
		ch.Direction = string(e.Direction)
		verb := "promoted"
		if e.Direction == releasetimeline.DirBehind {
			verb = "rolled back to"
		}
		ch.Summary = fmt.Sprintf("%s %s", verb, e.Version)
		if e.Recorded {
			ch.Summary += " (recorded by CI)"
		}
	}
	return ch
}

// sortChanges orders the feed newest first (stable: ties keep source order)
// and applies the limit.
func sortChanges(res *ChangesResult, limit int) {
	sort.SliceStable(res.Changes, func(i, j int) bool { return res.Changes[i].At.After(res.Changes[j].At) })
	if limit > 0 && len(res.Changes) > limit {
		res.Omitted = len(res.Changes) - limit
		res.Changes = res.Changes[:limit]
	}
}
