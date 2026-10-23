// Package releasehealth is the rule that judges a release: given the stats of
// the window before a promotion and the window after it, plus the issues,
// alerts and workload restarts the release opened, it produces findings and a
// verdict (PASS, FAIL or INCONCLUSIVE).
//
// It is pure: no I/O, no clock, no control-plane or ClickHouse types. Callers
// fetch the facts, this package decides.
//
// ONE DEFINITION, THREE CALLERS. `forge obs check` runs these functions over
// the local ClickStack's numbers; on a hosted env it renders the control
// plane's GetReleaseHealth answer into the same [Report] and re-judges it with
// [Rejudge]. The control plane's own implementation (control-plane phase C4)
// is meant to import this package from forge rather than re-derive the rule,
// so every function and type a server needs is exported on purpose: [Plan]
// (the windows), [Resolve] (effective thresholds and where each came from),
// [Evaluate] (findings and verdict) and [Rejudge] (a different fail-on set).
// Keep them exported.
//
// THE RULE (DESIGN §6.1).
//
//   - Windows: before = [P-w, P), after = [P, P+w). Each stops at the
//     neighbouring promotion; the after window also stops at now.
//   - A service is judged only when its after window has at least
//     min_requests requests.
//   - Findings: error_rate, latency, new_issue, regressed_issue, alert_firing,
//     workload_restarts. A finding's policy is "fail" when its code is in
//     fail_on, "warn" otherwise.
//   - Verdict: FAIL when any finding's policy is fail; else INCONCLUSIVE when
//     the after window has not finished or every service is below
//     min_requests; else PASS. A clear failure is a failure even while the
//     window is still open.
package releasehealth

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Verdict is the outcome of a release-health check.
type Verdict string

const (
	// Pass: nothing that fails the release was found, and enough traffic was
	// seen to say so.
	Pass Verdict = "PASS"
	// Fail: a finding whose policy is fail fired.
	Fail Verdict = "FAIL"
	// Inconclusive: not proven good and not proven bad.
	Inconclusive Verdict = "INCONCLUSIVE"
)

// Finding policies.
const (
	PolicyFail = "fail"
	PolicyWarn = "warn"
)

// Where an effective threshold came from.
const (
	SourceReleaseShape = "release_shape"
	SourceDefault      = "default"
	SourceRequest      = "request"
)

// Threshold names, as they appear in a response and in --threshold.
const (
	ThresholdWindow               = "window"
	ThresholdMinRequests          = "min_requests"
	ThresholdMaxErrorRateIncrease = "max_error_rate_increase"
	ThresholdMaxP95Ratio          = "max_p95_ratio"
	ThresholdNewIssueMinEvents    = "new_issue_min_events"
	ThresholdFailOn               = "fail_on"
)

// ErrInvalid marks a request the rule refuses (an unknown threshold, a value
// out of range). A caller reports it as "we looked, and the request is wrong".
var ErrInvalid = errors.New("invalid release-health request")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ── windows ─────────────────────────────────────────────────────────────

// Range is a half-open time range [From, To).
type Range struct{ From, To time.Time }

// Windows are the two windows a promotion is judged over.
type Windows struct {
	Before Range
	After  Range
	// AfterComplete is true once the after window can no longer grow: its
	// intended end (P+w, or the next promotion if that came first) is not in
	// the future.
	AfterComplete bool
}

// Plan computes the windows of the promotion at promotedAt. prev and next are
// the neighbouring promotions of the same env (nil when there is none): each
// window stops at its neighbour so a release is never blamed for the one
// before it or credited with the one after it. The after window also stops at
// now.
func Plan(promotedAt time.Time, window time.Duration, prev, next *time.Time, now time.Time) Windows {
	p := promotedAt.UTC()
	w := Windows{Before: Range{From: p.Add(-window), To: p}}
	if prev != nil && prev.After(w.Before.From) && prev.Before(p) {
		w.Before.From = prev.UTC()
	}
	end := p.Add(window)
	if next != nil && next.After(p) && next.Before(end) {
		end = next.UTC()
	}
	w.AfterComplete = !end.After(now)
	w.After = Range{From: p, To: end}
	if now.Before(end) {
		w.After.To = now.UTC()
	}
	return w
}

// ── thresholds ──────────────────────────────────────────────────────────

// Thresholds are the effective values a release is judged by.
type Thresholds struct {
	Window               time.Duration
	MinRequests          int
	MaxErrorRateIncrease float64
	MaxP95Ratio          float64
	NewIssueMinEvents    int
	FailOn               []string
	// Sources maps a threshold name to SourceReleaseShape, SourceDefault or
	// SourceRequest.
	Sources map[string]string
}

// Threshold is one effective threshold, tagged with where it came from.
type Threshold struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// Options are what [Resolve] reads.
type Options struct {
	// Shape is the env's recorded policy; nil means every default.
	Shape *release.ShapeReleaseHealth
	// Window overrides the window when positive.
	Window time.Duration
	// FailOn overrides the fail-on set when non-nil.
	FailOn []string
	// Overrides are --threshold name=value pairs, by name.
	Overrides map[string]string
}

// OverrideNames are the thresholds a request may override by name.
var OverrideNames = []string{ThresholdMaxErrorRateIncrease, ThresholdMaxP95Ratio, ThresholdMinRequests, ThresholdNewIssueMinEvents}

// ParseOverrides reads --threshold name=value pairs.
func ParseOverrides(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, invalid("--threshold %q: use name=value", p)
		}
		switch k {
		case ThresholdWindow:
			return nil, invalid("--threshold window: use --window")
		case ThresholdFailOn:
			return nil, invalid("--threshold fail_on: use --fail-on")
		}
		if !slices.Contains(OverrideNames, k) {
			return nil, invalid("--threshold %q is not a threshold; use one of %s", k, strings.Join(OverrideNames, ", "))
		}
		out[k] = v
	}
	return out, nil
}

// Resolve computes the effective thresholds: the env's recorded policy (or the
// defaults), then the request's overrides, each tagged with its source.
func Resolve(o Options) (Thresholds, error) {
	base := release.DefaultReleaseHealth()
	src := SourceDefault
	if !o.Shape.IsDefault() {
		base, src = *o.Shape, SourceReleaseShape
	}
	t := Thresholds{
		Window:               base.Window(),
		MinRequests:          base.MinRequests,
		MaxErrorRateIncrease: base.MaxErrorRateIncrease,
		MaxP95Ratio:          base.MaxP95Ratio,
		NewIssueMinEvents:    base.NewIssueMinEvents,
		FailOn:               normalizeCodes(base.FailOn),
		Sources: map[string]string{
			ThresholdWindow: src, ThresholdMinRequests: src, ThresholdMaxErrorRateIncrease: src,
			ThresholdMaxP95Ratio: src, ThresholdNewIssueMinEvents: src, ThresholdFailOn: src,
		},
	}
	if o.Window > 0 {
		t.Window, t.Sources[ThresholdWindow] = o.Window, SourceRequest
	}
	if o.FailOn != nil {
		t.FailOn, t.Sources[ThresholdFailOn] = normalizeCodes(o.FailOn), SourceRequest
	}
	for k, v := range o.Overrides {
		var err error
		switch k {
		case ThresholdMinRequests:
			t.MinRequests, err = strconv.Atoi(v)
		case ThresholdNewIssueMinEvents:
			t.NewIssueMinEvents, err = strconv.Atoi(v)
		case ThresholdMaxErrorRateIncrease:
			t.MaxErrorRateIncrease, err = strconv.ParseFloat(v, 64)
		case ThresholdMaxP95Ratio:
			t.MaxP95Ratio, err = strconv.ParseFloat(v, 64)
		default:
			return Thresholds{}, invalid("unknown threshold %q; use one of %s", k, strings.Join(OverrideNames, ", "))
		}
		if err != nil {
			return Thresholds{}, invalid("threshold %s=%q is not a number", k, v)
		}
		t.Sources[k] = SourceRequest
	}
	return t, t.validate()
}

func (t Thresholds) validate() error {
	switch {
	case t.Window < release.MinHealthWindow || t.Window > release.MaxHealthWindow:
		return invalid("window %s is outside %s to %s", t.Window, release.MinHealthWindow, release.MaxHealthWindow)
	case t.MinRequests < 1:
		return invalid("min_requests must be at least 1")
	case t.MaxErrorRateIncrease <= 0 || t.MaxErrorRateIncrease > 1:
		return invalid("max_error_rate_increase %v must be a fraction in (0, 1]", t.MaxErrorRateIncrease)
	case t.MaxP95Ratio <= 1:
		return invalid("max_p95_ratio %v must be above 1", t.MaxP95Ratio)
	case t.NewIssueMinEvents < 1:
		return invalid("new_issue_min_events must be at least 1")
	}
	for _, c := range t.FailOn {
		if !slices.Contains(release.HealthFindings, c) {
			return invalid("fail_on %q is not a finding; use %s", c, strings.Join(release.HealthFindings, ", "))
		}
	}
	return nil
}

// ParseFailOn reads --fail-on's comma-separated codes.
func ParseFailOn(csv string) ([]string, error) {
	var out []string
	for _, c := range strings.Split(csv, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if !slices.Contains(release.HealthFindings, c) {
			return nil, invalid("--fail-on %q is not a finding; use %s", c, strings.Join(release.HealthFindings, ", "))
		}
		out = append(out, c)
	}
	return normalizeCodes(out), nil
}

func normalizeCodes(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return slices.Compact(out)
}

// List is the thresholds in a stable order, for a response.
func (t Thresholds) List() []Threshold {
	src := func(n string) string {
		if s := t.Sources[n]; s != "" {
			return s
		}
		return SourceDefault
	}
	return []Threshold{
		{ThresholdWindow, durationText(t.Window), src(ThresholdWindow)},
		{ThresholdMinRequests, strconv.Itoa(t.MinRequests), src(ThresholdMinRequests)},
		{ThresholdMaxErrorRateIncrease, strconv.FormatFloat(t.MaxErrorRateIncrease, 'g', -1, 64), src(ThresholdMaxErrorRateIncrease)},
		{ThresholdMaxP95Ratio, strconv.FormatFloat(t.MaxP95Ratio, 'g', -1, 64), src(ThresholdMaxP95Ratio)},
		{ThresholdNewIssueMinEvents, strconv.Itoa(t.NewIssueMinEvents), src(ThresholdNewIssueMinEvents)},
		{ThresholdFailOn, strings.Join(t.FailOn, ","), src(ThresholdFailOn)},
	}
}

func durationText(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return d.String()
}

// ── facts ───────────────────────────────────────────────────────────────

// WindowStats are one service's figures over one window, over weighted server
// spans (tail sampling already corrected for).
type WindowStats struct {
	Requests  int64   `json:"requests"`
	ErrorRate float64 `json:"error_rate"` // fraction, 0..1
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

// ServiceHealth is one service's before and after.
type ServiceHealth struct {
	Service string      `json:"service"`
	Before  WindowStats `json:"before"`
	After   WindowStats `json:"after"`
	// VersionShare is the fraction (0..1) of after-window traffic served by
	// the judged version: rollout and canary progress. Nil when unknown (a
	// local env has no promoted version).
	VersionShare *float64 `json:"version_share,omitempty"`
}

// Issue is an error group a release opened or regressed.
type Issue struct {
	ID           string    `json:"id"`
	Service      string    `json:"service,omitempty"`
	Title        string    `json:"title,omitempty"`
	Culprit      string    `json:"culprit,omitempty"`
	Events       int64     `json:"events"`
	FirstSeenAt  time.Time `json:"first_seen_at,omitzero"`
	FirstVersion string    `json:"first_version,omitempty"`
	RegressedAt  time.Time `json:"regressed_at,omitzero"`
}

// Alert is an alert instance that opened at or after the promotion.
type Alert struct {
	Rule        string            `json:"rule"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	FiringAt    time.Time         `json:"firing_at,omitzero"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// Workload is a restart or OOM kill during the after window.
type Workload struct {
	Workload             string `json:"workload"`
	Restarts             int64  `json:"restarts"`
	LastTerminatedReason string `json:"last_terminated_reason,omitempty"`
}

// Subject names the promotion judged.
type Subject struct {
	PromotionID     string    `json:"promotion_id,omitempty"`
	Release         string    `json:"release,omitempty"`
	GitCommit       string    `json:"git_commit,omitempty"`
	PromotedAt      time.Time `json:"promoted_at"`
	PreviousRelease string    `json:"previous_release,omitempty"`
}

// Input is everything [Evaluate] reads, already fetched.
type Input struct {
	Subject Subject
	Windows Windows
	// Services are the per-service figures of both windows.
	Services []ServiceHealth
	// NewIssues are candidate issues first seen in the after window;
	// RegressedIssues are issues that regressed. Evaluate keeps the ones that
	// count (see the finding rules).
	NewIssues       []Issue
	RegressedIssues []Issue
	Alerts          []Alert
	Workloads       []Workload
	Thresholds      Thresholds
	// NotEvaluated names the checks the backend could not run (a local env has
	// no alert state or workload restarts), so a PASS is not read as covering
	// them.
	NotEvaluated []string
}

// Finding is one thing a release did.
type Finding struct {
	// Code is one of release.HealthFindings.
	Code string `json:"code"`
	// Service is empty for a finding that is not about one service.
	Service string `json:"service,omitempty"`
	// Ref names the object (issue id, alert rule, workload).
	Ref string `json:"ref,omitempty"`
	// Value and Threshold are in the unit the code implies: a fraction for
	// error_rate, a ratio for latency, a count for the rest.
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	// Policy is PolicyFail or PolicyWarn.
	Policy  string `json:"policy"`
	Message string `json:"message"`
}

// Report is a judged release: the facts and the verdict.
type Report struct {
	Subject         Subject         `json:"subject"`
	Before          Range           `json:"-"`
	After           Range           `json:"-"`
	AfterComplete   bool            `json:"after_complete"`
	Services        []ServiceHealth `json:"services"`
	NewIssues       []Issue         `json:"new_issues,omitempty"`
	RegressedIssues []Issue         `json:"regressed_issues,omitempty"`
	FiringAlerts    []Alert         `json:"firing_alerts,omitempty"`
	Workloads       []Workload      `json:"workload_findings,omitempty"`
	Verdict         Verdict         `json:"verdict"`
	Findings        []Finding       `json:"findings"`
	Thresholds      []Threshold     `json:"thresholds"`
	NotEvaluated    []string        `json:"not_evaluated,omitempty"`
}

// ── evaluation ──────────────────────────────────────────────────────────

// Evaluate applies the rule to the facts.
func Evaluate(in Input) Report {
	t := in.Thresholds
	p := in.Subject.PromotedAt
	r := Report{
		Subject: in.Subject, Before: in.Windows.Before, After: in.Windows.After, AfterComplete: in.Windows.AfterComplete,
		Services: in.Services, Findings: []Finding{}, Thresholds: t.List(), NotEvaluated: in.NotEvaluated,
	}
	add := func(f Finding) {
		f.Policy = policyOf(f.Code, t.FailOn)
		r.Findings = append(r.Findings, f)
	}

	for _, s := range in.Services {
		if s.After.Requests < int64(t.MinRequests) {
			continue
		}
		if inc := s.After.ErrorRate - s.Before.ErrorRate; inc > t.MaxErrorRateIncrease {
			add(Finding{Code: release.HealthErrorRate, Service: s.Service, Value: inc, Threshold: t.MaxErrorRateIncrease,
				Message: fmt.Sprintf("%s: error rate %s -> %s (+%s, limit +%s)", s.Service,
					pct(s.Before.ErrorRate), pct(s.After.ErrorRate), pct(inc), pct(t.MaxErrorRateIncrease))})
		}
		if s.Before.P95Ms > 0 {
			if ratio := s.After.P95Ms / s.Before.P95Ms; ratio > t.MaxP95Ratio {
				add(Finding{Code: release.HealthLatency, Service: s.Service, Value: ratio, Threshold: t.MaxP95Ratio,
					Message: fmt.Sprintf("%s: p95 %s -> %s (%.2fx, limit %.2gx)", s.Service,
						ms(s.Before.P95Ms), ms(s.After.P95Ms), ratio, t.MaxP95Ratio)})
			}
		}
	}

	for _, is := range in.NewIssues {
		if in.Subject.Release != "" && is.FirstVersion != "" && is.FirstVersion != in.Subject.Release {
			continue
		}
		if !is.FirstSeenAt.IsZero() && is.FirstSeenAt.Before(p) {
			continue
		}
		if is.Events < int64(t.NewIssueMinEvents) {
			continue
		}
		r.NewIssues = append(r.NewIssues, is)
		add(Finding{Code: release.HealthNewIssue, Service: is.Service, Ref: is.ID, Value: float64(is.Events), Threshold: float64(t.NewIssueMinEvents),
			Message: fmt.Sprintf("new issue %s: %s (%d events)", is.ID, firstNonEmpty(is.Title, is.Culprit), is.Events)})
	}
	for _, is := range in.RegressedIssues {
		if !is.RegressedAt.IsZero() && is.RegressedAt.Before(p) {
			continue
		}
		r.RegressedIssues = append(r.RegressedIssues, is)
		add(Finding{Code: release.HealthRegressedIssue, Service: is.Service, Ref: is.ID, Value: float64(is.Events), Threshold: 1,
			Message: fmt.Sprintf("issue %s regressed: %s", is.ID, firstNonEmpty(is.Title, is.Culprit))})
	}
	for _, a := range in.Alerts {
		if !a.FiringAt.IsZero() && a.FiringAt.Before(p) {
			continue
		}
		r.FiringAlerts = append(r.FiringAlerts, a)
		add(Finding{Code: release.HealthAlertFiring, Ref: a.Rule, Value: 1, Threshold: 1,
			Message: fmt.Sprintf("alert %s opened after the promotion and is still firing", a.Rule)})
	}
	for _, w := range in.Workloads {
		if w.Restarts < 1 {
			continue
		}
		r.Workloads = append(r.Workloads, w)
		why := ""
		if w.LastTerminatedReason != "" {
			why = " (" + w.LastTerminatedReason + ")"
		}
		add(Finding{Code: release.HealthWorkloadRestarts, Ref: w.Workload, Value: float64(w.Restarts), Threshold: 1,
			Message: fmt.Sprintf("%s restarted %d time(s) after the promotion%s", w.Workload, w.Restarts, why)})
	}

	sortFindings(r.Findings)
	r.Verdict = verdictOf(r.Findings, r.AfterComplete, r.Services, t.MinRequests)
	return r
}

// Rejudge applies a different fail-on set to a judged report: each finding's
// policy follows the set and the verdict is recomputed. It is how `--fail-on`
// reaches a control plane's answer, whose own verdict used the env's policy.
// The min_requests it needs is read from the report's thresholds.
func Rejudge(r Report, failOn []string) Report {
	failOn = normalizeCodes(failOn)
	out := r
	out.Findings = make([]Finding, len(r.Findings))
	for i, f := range r.Findings {
		f.Policy = policyOf(f.Code, failOn)
		out.Findings[i] = f
	}
	out.Thresholds = slices.Clone(r.Thresholds)
	set := false
	for i, th := range out.Thresholds {
		if th.Name == ThresholdFailOn {
			out.Thresholds[i] = Threshold{ThresholdFailOn, strings.Join(failOn, ","), SourceRequest}
			set = true
		}
	}
	if !set {
		out.Thresholds = append(out.Thresholds, Threshold{ThresholdFailOn, strings.Join(failOn, ","), SourceRequest})
	}
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, minRequestsOf(out.Thresholds))
	return out
}

func minRequestsOf(ths []Threshold) int {
	for _, th := range ths {
		if th.Name == ThresholdMinRequests {
			if n, err := strconv.Atoi(th.Value); err == nil && n > 0 {
				return n
			}
		}
	}
	return release.DefaultReleaseHealth().MinRequests
}

func policyOf(code string, failOn []string) string {
	if slices.Contains(failOn, code) {
		return PolicyFail
	}
	return PolicyWarn
}

func verdictOf(findings []Finding, afterComplete bool, services []ServiceHealth, minRequests int) Verdict {
	for _, f := range findings {
		if f.Policy == PolicyFail {
			return Fail
		}
	}
	if !afterComplete {
		return Inconclusive
	}
	for _, s := range services {
		if s.After.Requests >= int64(minRequests) {
			return Pass
		}
	}
	return Inconclusive
}

// InconclusiveWhy says why a report is INCONCLUSIVE, in one line.
func InconclusiveWhy(r Report) string {
	if !r.AfterComplete {
		return fmt.Sprintf("the after window has not finished (it ends %s)", r.After.To.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("no service had at least %d requests after the promotion, so there is not enough traffic to judge", minRequestsOf(r.Thresholds))
}

func sortFindings(fs []Finding) {
	order := func(code string) int { return slices.Index(release.HealthFindings, code) }
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if oa, ob := order(a.Code), order(b.Code); oa != ob {
			return oa < ob
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Ref < b.Ref
	})
}

func pct(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" }

func ms(v float64) string {
	if v >= 1000 {
		return strconv.FormatFloat(v/1000, 'f', 2, 64) + "s"
	}
	return strconv.FormatFloat(v, 'f', 0, 64) + "ms"
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
