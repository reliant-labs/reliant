package obsbackend

// Release health and the change feed over the control plane. The wire shapes
// are declared locally (protojson: camelCase, int64 as a string); the field
// names are those of control-plane proto/services/observability/v1 and
// proto/services/activity/v1.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/releasetimeline"
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

type wireReleaseHealth struct {
	Promotion struct {
		ID                     string    `json:"id"`
		ReleaseVersion         string    `json:"releaseVersion"`
		GitCommit              string    `json:"gitCommit"`
		PromotedAt             time.Time `json:"promotedAt"`
		PreviousReleaseVersion string    `json:"previousReleaseVersion"`
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
	FiringAlerts    []struct {
		Rule        string            `json:"rule"`
		Fingerprint string            `json:"fingerprint"`
		FiringAt    time.Time         `json:"firingAt"`
		Labels      map[string]string `json:"labels"`
	} `json:"firingAlerts"`
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
	} `json:"findings"`
	Thresholds []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Source string `json:"source"`
	} `json:"thresholds"`
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
	body := map[string]any{"scope": c.scope(Narrow{Range: Range{From: now.Add(-time.Hour), To: now}})}
	if o.Window > 0 {
		body["window"] = fmt.Sprintf("%ds", int64(o.Window.Seconds()))
	}
	switch {
	case req.PromotionID != "":
		body["promotionId"] = req.PromotionID
	case req.Release != "":
		body["releaseVersion"] = req.Release
	}
	if len(o.Overrides) > 0 {
		body["thresholdOverrides"] = o.Overrides
	}
	var w wireReleaseHealth
	if err := c.call(ctx, procGetReleaseHealth, body, &w); err != nil {
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

func reportFromWire(w wireReleaseHealth) (releasehealth.Report, error) {
	r := releasehealth.Report{
		Subject: releasehealth.Subject{
			PromotionID: w.Promotion.ID, Release: w.Promotion.ReleaseVersion, GitCommit: w.Promotion.GitCommit,
			PromotedAt: w.Promotion.PromotedAt.UTC(), PreviousRelease: w.Promotion.PreviousReleaseVersion,
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
		r.FiringAlerts = append(r.FiringAlerts, releasehealth.Alert{Rule: a.Rule, Fingerprint: a.Fingerprint, FiringAt: a.FiringAt.UTC(), Labels: a.Labels})
	}
	for _, f := range w.WorkloadFindings {
		r.Workloads = append(r.Workloads, releasehealth.Workload{Workload: f.Workload, Restarts: int64(f.Restarts), LastTerminatedReason: f.LastTerminatedReason})
	}
	for _, f := range w.Findings {
		code := strings.ToLower(strings.TrimPrefix(f.Code, "RELEASE_FINDING_CODE_"))
		if code == "" || code == "unspecified" {
			continue
		}
		r.Findings = append(r.Findings, releasehealth.Finding{Code: code, Service: f.Service, Value: f.Value, Threshold: f.Threshold, Policy: f.Policy, Message: f.Message})
	}
	for _, t := range w.Thresholds {
		r.Thresholds = append(r.Thresholds, releasehealth.Threshold{Name: t.Name, Value: t.Value, Source: t.Source})
	}
	return r, nil
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
	body := map[string]any{"scope": scope, "pageSize": changesPageSize}
	var act struct {
		Events []wireActivityEvent `json:"events"`
	}
	if err := c.call(ctx, procListActivity, body, &act); err != nil {
		note := fmt.Sprintf("activity unreadable, only the deploy ledger is shown: %v", err)
		if unimplemented(err) {
			note = "the control plane does not serve activity yet; only the deploy ledger is shown"
		}
		res.Warnings = append(res.Warnings, note)
	}
	for _, ev := range act.Events {
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
	return res, nil
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
	ch := Change{At: e.At, Version: e.Version, PromotionID: e.PromotionID, Actor: e.Actor, Via: e.Via}
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
