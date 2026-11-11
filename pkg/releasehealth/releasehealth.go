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
//   - Alerts count when they opened in the after window [P, after.To); a
//     critical one (label severity=critical) fails the release whatever
//     fail_on says. One that opened after the window is not the release's.
//   - Verdict: FAIL when any finding's policy is fail; else INCONCLUSIVE when
//     the after window has not finished, every service is below
//     min_requests, or a critical alert that opened before the promotion is
//     still firing (the release was promoted into it, so nothing proves it
//     good); else PASS. A clear failure is a failure even while the window is
//     still open.
//   - Attribution (attribution.go): P is when the release went live, which a
//     retried promotion reaches after its first promoted_at ([LiveAt]); a
//     finding the after window cannot pin on the judged release (its version
//     served too little of the traffic, the issue's events are all another
//     version's) never fails it, and a window that is not the live release's
//     is INCONCLUSIVE.
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
	// Endpoints are the service's figures per endpoint (server span name).
	// When set, latency is judged endpoint by endpoint (LatencyRatio), so a
	// change in which endpoints were called cannot pass for a regression.
	// Empty = judge the service-level p95.
	Endpoints []EndpointHealth `json:"endpoints,omitempty"`
}

// EndpointHealth is one endpoint's before and after.
type EndpointHealth struct {
	Endpoint string      `json:"endpoint"`
	Before   WindowStats `json:"before"`
	After    WindowStats `json:"after"`
}

// LatencyRatio is how much slower the service got, and whether there is a
// comparison at all. With per-endpoint figures it is the after-window
// request-weighted mean of each endpoint's after/before p95, over the
// endpoints with a p95 in both windows: a window filled with one client's
// fast calls (a deploy polling its own release) and then emptied of them
// moves the service-level p95 without any endpoint getting slower
// (forge#1728). An endpoint with no p95 before the promotion (new in the
// after window) is judged against the service-level before p95: an unknown
// baseline is never read as safe, so a new, slow endpoint counts. An endpoint
// gone from the after window has nothing to judge. Without per-endpoint
// figures it is the service-level ratio.
func (s ServiceHealth) LatencyRatio() (float64, bool) {
	if len(s.Endpoints) == 0 {
		if s.Before.P95Ms <= 0 {
			return 0, false
		}
		return s.After.P95Ms / s.Before.P95Ms, true
	}
	var sum, weight float64
	for _, e := range s.Endpoints {
		// An after p95 of zero is no measurement, not a 0x ratio: counted as
		// zero, a busy fast route would add weight and nothing else, diluting
		// a slow route beside it below the limit. Skipping it can only raise
		// the ratio, never hide a slow endpoint.
		if e.After.P95Ms <= 0 || e.After.Requests <= 0 {
			continue
		}
		base := e.Before.P95Ms
		if base <= 0 {
			base = s.Before.P95Ms
		}
		if base <= 0 {
			continue
		}
		w := float64(e.After.Requests)
		sum += w * e.After.P95Ms / base
		weight += w
	}
	if weight == 0 {
		return 0, false
	}
	return sum / weight, true
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

// Critical reports whether the alert is labelled severity=critical.
func (a Alert) Critical() bool { return strings.EqualFold(a.Labels["severity"], "critical") }

// LabelReleaseHealth is the alert label that says whether a rule's alerts judge
// the env's releases. Set to [ReleaseHealthExclude] (`labels = {release_health =
// "exclude"}` on the forge.AlertRule), it says the rule watches something other
// than the env's own workloads: the machines that build it, a fleet the env
// monitors. Such an alert is the env's to page on, never a release's doing: it
// is not a finding, not a standing critical alert, and it never fails or holds
// a promotion. It is listed as ExcludedAlerts instead, so the report still
// says it was firing. Any other value, or none, counts as before.
const LabelReleaseHealth = "release_health"

// ReleaseHealthExclude is the [LabelReleaseHealth] value that keeps an alert
// out of release health.
const ReleaseHealthExclude = "exclude"

// Excluded reports whether the alert's rule keeps it out of release health
// ([LabelReleaseHealth]).
func (a Alert) Excluded() bool {
	return strings.EqualFold(strings.TrimSpace(a.Labels[LabelReleaseHealth]), ReleaseHealthExclude)
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
	// LiveAt is when an attempt of the promotion shipped its release
	// ([LiveAt]), when its deploy gates say so: later than PromotedAt for a
	// promotion whose first attempt failed.
	LiveAt time.Time `json:"live_at,omitzero"`
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
	// Alerts are the alerts firing now. Those opened after the promotion are
	// findings; critical ones opened before it are StandingCritical.
	Alerts     []Alert
	Workloads  []Workload
	Thresholds Thresholds
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
	// Unattributed says why the finding is not the judged release's doing
	// ([Attribute]); such a finding is a warn whatever fail_on says.
	Unattributed string `json:"unattributed,omitempty"`
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
	// StandingCritical are critical alerts that opened before the promotion
	// and are still firing. They are not the release's doing, so they are not
	// findings, but a release promoted into one is not proven good: they make
	// a PASS INCONCLUSIVE.
	StandingCritical []Alert `json:"standing_critical_alerts,omitempty"`
	// ExcludedAlerts were firing, but their rule says they are not about the
	// env's workloads ([LabelReleaseHealth]), so they judged nothing.
	ExcludedAlerts []Alert `json:"excluded_alerts,omitempty"`
	// AlertStateUnread says why a caller that applies the critical-alert rule
	// itself could not read the alert state (WithAlertStateUnread): a PASS
	// that could not look at it is not proven good.
	AlertStateUnread string     `json:"alert_state_unread,omitempty"`
	Workloads        []Workload `json:"workload_findings,omitempty"`
	// Uninstrumented names the env's services whose request telemetry is
	// their own to export — built from their own Dockerfile or command, or a
	// third-party image, not by forge, which instruments what it builds
	// (WithUninstrumented). Read only to say why a report with no request
	// telemetry could not be judged; it never changes a verdict.
	Uninstrumented []string    `json:"uninstrumented_services,omitempty"`
	Verdict        Verdict     `json:"verdict"`
	Findings       []Finding   `json:"findings"`
	Thresholds     []Threshold `json:"thresholds"`
	NotEvaluated   []string    `json:"not_evaluated,omitempty"`
	// VersionShare is the judged release's share (0..1) of the after
	// window's traffic, over the services whose share was measured; nil when
	// none was.
	VersionShare *float64 `json:"version_share,omitempty"`
	// WindowNotLive says why the after window is not the live release's
	// ([WithLiveAt]); RolloutIncomplete why the release served too little
	// of it ([MinVersionShare]). Either makes a PASS INCONCLUSIVE.
	WindowNotLive     string `json:"window_not_live,omitempty"`
	RolloutIncomplete string `json:"rollout_incomplete,omitempty"`
	// IssueWindowVersions are each issue's after-window events by
	// service.version, keyed by issue id, for the issues the judge read them
	// for: what attributes an issue to the judged release or to another
	// ([Attribute]). An issue with no entry was not read. It lives here, not
	// on [Issue], so Issue stays a comparable value.
	IssueWindowVersions map[string]map[string]int64 `json:"issue_window_versions,omitempty"`
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
	critical := criticalRules(in.Alerts, p, in.Windows.After.To)
	add := func(f Finding) {
		f.Policy = findingPolicy(f, t.FailOn, critical)
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
		if ratio, ok := s.LatencyRatio(); ok && ratio > t.MaxP95Ratio {
			per := ""
			if len(s.Endpoints) > 0 {
				per = " per endpoint"
			}
			add(Finding{Code: release.HealthLatency, Service: s.Service, Value: ratio, Threshold: t.MaxP95Ratio,
				Message: fmt.Sprintf("%s: p95 %s -> %s (%.2fx%s, limit %.2gx)", s.Service,
					ms(s.Before.P95Ms), ms(s.After.P95Ms), ratio, per, t.MaxP95Ratio)})
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
		if afterWindow(a, in.Windows.After.To) {
			continue // a later promotion's, or later still: not this release's
		}
		if a.Excluded() {
			r.ExcludedAlerts = append(r.ExcludedAlerts, a)
			continue
		}
		if !a.FiringAt.IsZero() && a.FiringAt.Before(p) {
			if a.Critical() {
				r.StandingCritical = append(r.StandingCritical, a)
			}
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
	r.Verdict = verdictOf(r.Findings, r.AfterComplete, r.Services, t.MinRequests, r.unproven())
	return Attribute(r)
}

// unproven reports whether something outside the findings keeps a PASS from
// being proven: a standing critical alert, alert state that was not read, or
// an after window that is not the live release's or that it barely served.
func (r Report) unproven() bool {
	return len(r.StandingCritical) > 0 || r.AlertStateUnread != "" || r.WindowNotLive != "" || r.RolloutIncomplete != ""
}

// WithStandingAlerts applies the critical-alert rule to a judged report from
// the alerts firing now, read by the caller (`forge obs check` reads them
// itself, so the rule holds over a control plane whose answer predates it):
// a critical alert opened in the after window [P, after.To) is a failing
// alert_firing finding, whatever the policy that judged the report; one opened
// before P joins StandingCritical; one opened after the window, or with no
// opening time, is not attributed. The report's own FiringAlerts are applied the same
// way. The verdict is recomputed; alerts that are not critical are ignored.
func WithStandingAlerts(r Report, firing []Alert) Report {
	out := r
	out.Findings = slices.Clone(r.Findings)
	out.StandingCritical = slices.Clone(r.StandingCritical)
	p := r.Subject.PromotedAt
	seen := map[string]bool{}
	for _, a := range r.StandingCritical {
		seen[alertKey(a)] = true
	}
	for _, a := range append(slices.Clone(r.FiringAlerts), firing...) {
		// An alert with no opening time cannot be placed against the
		// promotion, and one that opened after the judged after window (a
		// later promotion's, or later still) is not this release's.
		if !a.Critical() || a.Excluded() || a.FiringAt.IsZero() || !a.FiringAt.Before(r.After.To) || seen[alertKey(a)] {
			continue
		}
		seen[alertKey(a)] = true
		if a.FiringAt.Before(p) {
			out.StandingCritical = append(out.StandingCritical, a)
			continue
		}
		out.Findings = failAlert(out.Findings, a.Rule)
		if !slices.ContainsFunc(out.FiringAlerts, func(b Alert) bool { return alertKey(b) == alertKey(a) }) {
			out.FiringAlerts = append(out.FiringAlerts, a)
		}
	}
	sortFindings(out.Findings)
	out.Verdict = worse(r.Verdict, verdictOf(out.Findings, out.AfterComplete, out.Services, minRequestsOf(out.Thresholds), out.unproven()))
	return out
}

// worse is the less favourable verdict: these folds only ever add evidence
// against a release, so they never upgrade the verdict they were handed.
func worse(a, b Verdict) Verdict {
	rank := map[Verdict]int{Pass: 0, Inconclusive: 1, Fail: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// failAlert makes the alert_firing finding for rule fail, adding it when the
// report has none. A finding that crossed a wire without its ref is matched
// by its message, which names the rule.
func failAlert(fs []Finding, rule string) []Finding {
	for i, f := range fs {
		if f.Code == release.HealthAlertFiring && (f.Ref == rule || (f.Ref == "" && strings.Contains(f.Message, "alert "+rule+" "))) {
			if f.Unattributed != "" {
				return fs // not the live release's window (Attribute): INCONCLUSIVE, critical or not
			}
			fs[i].Ref, fs[i].Policy = rule, PolicyFail
			return fs
		}
	}
	return append(fs, Finding{Code: release.HealthAlertFiring, Ref: rule, Value: 1, Threshold: 1, Policy: PolicyFail,
		Message: fmt.Sprintf("critical alert %s opened after the promotion and is still firing", rule)})
}

// WithoutExcludedAlerts takes the alerts whose rule keeps them out of release
// health ([LabelReleaseHealth]) out of a judged report: from FiringAlerts and
// StandingCritical into ExcludedAlerts (listed once each), with the
// alert_firing finding of each such rule that has no counted alert left. live
// is the env's alert state read now, or nil: it names an excluded rule the
// report itself does not list (a control plane that omits its alert arrays),
// and a counted alert of that rule in it keeps the finding. A finding is
// removed only when its rule is known to be excluded and no counted alert of
// it remains. The verdict is recomputed from what is left, so it can improve:
// the excluded alerts were never the release's evidence. It is how the rule
// reaches a control plane's answer judged without it (`forge obs check`
// applies it before its own alert read).
func WithoutExcludedAlerts(r Report, live []Alert) Report {
	out := r
	out.ExcludedAlerts = nil
	seen := map[string]bool{}
	excluded := map[string]bool{}
	counted := map[string]bool{}
	exclude := func(a Alert) {
		excluded[a.Rule] = true
		if !seen[alertKey(a)] {
			seen[alertKey(a)] = true
			out.ExcludedAlerts = append(out.ExcludedAlerts, a)
		}
	}
	keep := func(as []Alert) []Alert {
		var kept []Alert
		for _, a := range as {
			if a.Excluded() {
				exclude(a)
				continue
			}
			kept = append(kept, a)
		}
		return kept
	}
	for _, a := range r.ExcludedAlerts {
		exclude(a)
	}
	out.FiringAlerts = keep(r.FiringAlerts)
	out.StandingCritical = keep(r.StandingCritical)
	for _, a := range out.FiringAlerts {
		counted[a.Rule] = true
	}
	for _, a := range live {
		switch {
		case afterWindow(a, r.After.To):
			// a later promotion's, or later still: not this release's
		case a.Excluded():
			exclude(a)
		default:
			counted[a.Rule] = true
		}
	}
	findingRule := func(f Finding) string {
		if f.Ref != "" {
			return f.Ref
		}
		for rule := range excluded {
			if strings.Contains(f.Message, "alert "+rule+" ") {
				return rule
			}
		}
		return ""
	}
	out.Findings = slices.DeleteFunc(slices.Clone(r.Findings), func(f Finding) bool {
		if f.Code != release.HealthAlertFiring {
			return false
		}
		rule := findingRule(f)
		return excluded[rule] && !counted[rule]
	})
	if len(out.Findings) == len(r.Findings) && len(out.StandingCritical) == len(r.StandingCritical) {
		// Nothing the verdict rests on changed: the verdict as judged.
		if len(out.FiringAlerts) == len(r.FiringAlerts) && len(out.ExcludedAlerts) == len(r.ExcludedAlerts) {
			return r // nothing excluded here: the report as judged
		}
		return out
	}
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, minRequestsOf(out.Thresholds), out.unproven())
	return out
}

// WithAlertStateUnread records that the alert state could not be read, so a
// PASS is not proven: the verdict becomes INCONCLUSIVE, naming why. A FAIL
// stays a FAIL.
func WithAlertStateUnread(r Report, why string) Report {
	out := r
	out.AlertStateUnread = why
	out.Verdict = worse(r.Verdict, verdictOf(out.Findings, out.AfterComplete, out.Services, minRequestsOf(out.Thresholds), out.unproven()))
	return out
}

func alertKey(a Alert) string {
	if a.Fingerprint != "" {
		return a.Fingerprint
	}
	keys := make([]string, 0, len(a.Labels))
	for k, v := range a.Labels {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return a.Rule + "\x00" + strings.Join(keys, ",")
}

// afterWindow reports whether a opened at or after end, the after window's
// end: [P, end) is what a release answers for. A zero end bounds nothing.
func afterWindow(a Alert, end time.Time) bool {
	return !end.IsZero() && !a.FiringAt.IsZero() && !a.FiringAt.Before(end)
}

// criticalRules are the rules with a critical alert opened in [p, end).
func criticalRules(alerts []Alert, p, end time.Time) map[string]bool {
	out := map[string]bool{}
	for _, a := range alerts {
		if a.Critical() && !a.Excluded() && !afterWindow(a, end) && (a.FiringAt.IsZero() || !a.FiringAt.Before(p)) {
			out[a.Rule] = true
		}
	}
	return out
}

// findingPolicy is policyOf, except that a critical alert always fails.
func findingPolicy(f Finding, failOn []string, critical map[string]bool) string {
	if f.Code == release.HealthAlertFiring && critical[f.Ref] {
		return PolicyFail
	}
	return policyOf(f.Code, failOn)
}

// Rejudge applies a different fail-on set to a judged report: each finding's
// policy follows the set and the verdict is recomputed. It is how `--fail-on`
// reaches a control plane's answer, whose own verdict used the env's policy.
// The min_requests it needs is read from the report's thresholds.
func Rejudge(r Report, failOn []string) Report {
	failOn = normalizeCodes(failOn)
	out := r
	out.Findings = make([]Finding, len(r.Findings))
	critical := criticalRules(r.FiringAlerts, r.Subject.PromotedAt, r.After.To)
	for i, f := range r.Findings {
		if f.Unattributed == "" {
			f.Policy = findingPolicy(f, failOn, critical)
		}
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
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, minRequestsOf(out.Thresholds), out.unproven())
	return out
}

// AfterEnds is when the after window finishes: After.To once the window is
// complete, else the window's start plus the window the report was judged
// with. While the window is open After.To is the moment it was read (Plan
// stops the window at now), so it advances with every read and is not the end
// to wait for. The start is After.From, which a judge anchoring at the live
// time ([LiveAt]) puts after the promotion's promoted_at.
func (r Report) AfterEnds() time.Time {
	if r.AfterComplete {
		return r.After.To
	}
	from := r.After.From
	if from.IsZero() {
		from = r.Subject.PromotedAt
	}
	if from.IsZero() {
		return r.After.To
	}
	return from.Add(windowOf(r.Thresholds))
}

func windowOf(ths []Threshold) time.Duration {
	for _, th := range ths {
		if th.Name == ThresholdWindow {
			if d, err := time.ParseDuration(th.Value); err == nil && d > 0 {
				return d
			}
		}
	}
	return release.DefaultReleaseHealth().Window()
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

func verdictOf(findings []Finding, afterComplete bool, services []ServiceHealth, minRequests int, unproven bool) Verdict {
	for _, f := range findings {
		if f.Policy == PolicyFail {
			return Fail
		}
	}
	if !afterComplete || unproven {
		return Inconclusive
	}
	for _, s := range services {
		if s.After.Requests >= int64(minRequests) {
			return Pass
		}
	}
	return Inconclusive
}

// Why a report is INCONCLUSIVE, as a stable code (InconclusiveCode) a gate
// or an agent can branch on; InconclusiveWhy is the same in a sentence.
const (
	ReasonWindowNotLive      = "window_not_live"
	ReasonRolloutIncomplete  = "rollout_incomplete"
	ReasonAlertStateUnread   = "alert_state_unread"
	ReasonStandingCritical   = "standing_critical_alert"
	ReasonWindowOpen         = "window_open"
	ReasonNoRequestTelemetry = "no_request_telemetry"
	ReasonLowTraffic         = "low_traffic"
)

// NoRequestTelemetry reports whether not one service sent a request in
// either window: there is no request data at all, which is not the same as
// too little of it. Release health measures requests from each service's
// own OpenTelemetry server spans, so an app that exports none looks like
// this however much traffic it serves — as does an instrumented one nobody
// called.
func (r Report) NoRequestTelemetry() bool {
	for _, s := range r.Services {
		if s.Before.Requests > 0 || s.After.Requests > 0 {
			return false
		}
	}
	return true
}

// WithUninstrumented records the env's services whose telemetry is their
// own to export (Report.Uninstrumented), sorted. The verdict is unchanged.
func WithUninstrumented(r Report, services []string) Report {
	out := r
	out.Uninstrumented = normalizeCodes(services)
	return out
}

// InconclusiveCode is why a report is INCONCLUSIVE, as one of the Reason*
// codes, in the precedence InconclusiveWhy explains it.
func InconclusiveCode(r Report) string {
	switch {
	case r.WindowNotLive != "":
		return ReasonWindowNotLive
	case r.RolloutIncomplete != "":
		return ReasonRolloutIncomplete
	case r.AlertStateUnread != "":
		return ReasonAlertStateUnread
	case len(r.StandingCritical) > 0:
		return ReasonStandingCritical
	case !r.AfterComplete:
		return ReasonWindowOpen
	case r.NoRequestTelemetry():
		return ReasonNoRequestTelemetry
	}
	return ReasonLowTraffic
}

// InconclusiveWhy says why a report is INCONCLUSIVE, in one line.
func InconclusiveWhy(r Report) string {
	if r.WindowNotLive != "" {
		return r.WindowNotLive
	}
	if r.RolloutIncomplete != "" {
		return r.RolloutIncomplete
	}
	if r.AlertStateUnread != "" {
		return "the env's alert state could not be read, so a firing critical alert cannot be ruled out: " + r.AlertStateUnread
	}
	if n := len(r.StandingCritical); n > 0 {
		rules := make([]string, 0, n)
		for _, a := range r.StandingCritical {
			if !slices.Contains(rules, a.Rule) {
				rules = append(rules, a.Rule)
			}
		}
		return fmt.Sprintf("promoted into %d firing critical alert(s) (%s) that opened before the promotion; resolve them, then check again", n, strings.Join(rules, ", "))
	}
	if !r.AfterComplete {
		return fmt.Sprintf("the after window has not finished (it ends %s)", r.AfterEnds().UTC().Format(time.RFC3339))
	}
	if r.NoRequestTelemetry() {
		return noRequestTelemetryWhy(r)
	}
	return fmt.Sprintf("no service had at least %d requests after the promotion, so there is not enough traffic to judge", minRequestsOf(r.Thresholds))
}

// noRequestTelemetryWhy is InconclusiveWhy for a report with no request data
// at all. It says what is missing and how to supply it, and what WAS judged
// without it, rather than reading as "not enough traffic" to someone whose
// app served plenty.
func noRequestTelemetryWhy(r Report) string {
	var b strings.Builder
	b.WriteString("no request telemetry: no service sent a single server span in either window, so error rate and latency could not be judged. ")
	b.WriteString("Release health measures requests from each service's own OpenTelemetry traces")
	if n := len(r.Uninstrumented); n > 0 {
		verb := "exports"
		if n > 1 {
			verb = "export"
		}
		fmt.Fprintf(&b, ", and %s %s none unless it is instrumented: forge did not build it, so it did not instrument it", strings.Join(r.Uninstrumented, ", "), verb)
	}
	b.WriteString(". Add an OpenTelemetry SDK that exports traces over OTLP to $OTEL_EXPORTER_OTLP_ENDPOINT (hosting sets it, and OTEL_SERVICE_NAME, on every workload); ")
	b.WriteString("if the app already does, send it traffic and check again. ")
	checked := "workload restarts, alerts and issues"
	if len(r.NotEvaluated) > 0 {
		checked += " (not evaluated here: " + strings.Join(r.NotEvaluated, ", ") + ")"
	}
	if len(r.Findings) == 0 {
		b.WriteString("What forge sees without it — " + checked + " — found nothing against the release")
	} else {
		fmt.Fprintf(&b, "What forge sees without it — %s — found %d warning(s), listed in findings", checked, len(r.Findings))
	}
	return b.String()
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
