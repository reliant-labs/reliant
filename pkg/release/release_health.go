package release

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"
)

// Release-health finding codes: what `forge obs check` and the control plane's
// GetReleaseHealth can report about a promotion, comparing the window after it
// with the window before it.
const (
	// HealthErrorRate: the error rate rose by more than MaxErrorRateIncrease,
	// each endpoint against its own baseline, with at least
	// ErrorRateMinErrors errors in the window.
	HealthErrorRate = "error_rate"
	// HealthLatency: p95 latency grew by more than MaxP95Ratio.
	HealthLatency = "latency"
	// HealthNewIssue: an issue first seen in this release, with at least
	// NewIssueMinEvents events.
	HealthNewIssue = "new_issue"
	// HealthRegressedIssue: a resolved issue regressed in the window, with at
	// least RegressedIssueMinEvents events in it.
	HealthRegressedIssue = "regressed_issue"
	// HealthAlertFiring: an alert opened after the promotion is still firing.
	HealthAlertFiring = "alert_firing"
	// HealthWorkloadRestarts: restarts or OOM kills after the promotion.
	HealthWorkloadRestarts = "workload_restarts"
	// HealthBrowserErrors: a frontend's JavaScript errors per page view rose
	// by more than MaxErrorRateIncrease (browser telemetry).
	HealthBrowserErrors = "browser_errors"
	// HealthBrowserFailedRequests: the share of a frontend's own fetch and XHR
	// requests that failed (5xx, or no response) rose by more than
	// MaxErrorRateIncrease.
	HealthBrowserFailedRequests = "browser_failed_requests"
	// HealthBrowserPageLoad: a frontend's p95 page load grew by more than
	// MaxP95Ratio.
	HealthBrowserPageLoad = "browser_page_load"
	// HealthProbeFailed: a release probe (forge.Probe) failed in the window
	// after the promotion. It fails a release by default, whatever the real
	// traffic says.
	HealthProbeFailed = "probe_failed"
	// HealthProbeLatency: a release probe's p95 grew by more than MaxP95Ratio,
	// and by at least the probe latency floor. It warns by default, as
	// latency does.
	HealthProbeLatency = "probe_latency"
	// HealthChangedOpErrors: an op (a component method, an rpc, an endpoint)
	// that the release's commit range changed has an error rate that rose by
	// more than changed_op_max_error_rate_increase, with at least
	// changed_op_min_errors errors after the promotion. It names the newest
	// commit that changed the op. It warns by default: which ops a diff
	// changed is inferred, and one op's window is a thin sample.
	HealthChangedOpErrors = "changed_op_errors"
	// HealthChangedOpLatency: a changed op's p95 grew by more than
	// changed_op_max_p95_ratio. It warns by default, as latency does.
	HealthChangedOpLatency = "changed_op_latency"
	// HealthSEORegressed: a static frontend's site audit of the release
	// (pkg/siteaudit) is worse for search than the same env's audit of the
	// previous release: the site's or a route's SEO score dropped by more
	// than seo_max_score_drop, or a new error-severity SEO finding appeared
	// (the env became noindex, robots.txt disallows /, a canonical names
	// another host, an internal link answers 404). It warns by default.
	HealthSEORegressed = "seo_regressed"
	// HealthA11yRegressed: likewise for accessibility: the accessibility
	// score dropped by more than a11y_max_score_drop, or a new critical axe
	// violation appeared. It warns by default.
	HealthA11yRegressed = "a11y_regressed"
)

// HealthFindings is every finding code, in a stable order. A code added here
// is a value a control plane that predates it refuses in a recorded fail_on,
// so it needs a bundle feature that withholds it (bundle_features.go:
// BundleFeatureReleaseProbes, BundleFeatureReleaseHealthChangedOps,
// BundleFeatureReleaseHealthSiteAudit).
var HealthFindings = []string{HealthErrorRate, HealthLatency, HealthNewIssue, HealthRegressedIssue, HealthAlertFiring, HealthWorkloadRestarts,
	HealthBrowserErrors, HealthBrowserFailedRequests, HealthBrowserPageLoad, HealthProbeFailed, HealthProbeLatency,
	HealthChangedOpErrors, HealthChangedOpLatency, HealthSEORegressed, HealthA11yRegressed}

// ChangedOpFindings are the codes judged per op the release changed
// (HealthChangedOpErrors, HealthChangedOpLatency).
var ChangedOpFindings = []string{HealthChangedOpErrors, HealthChangedOpLatency}

// SiteAuditFindings are the codes judged from a static frontend's site audit
// (HealthSEORegressed, HealthA11yRegressed).
var SiteAuditFindings = []string{HealthSEORegressed, HealthA11yRegressed}

// How many services a release needs a baseline for
// (ShapeReleaseHealth.RequireBaseline). A service's baseline is its before
// window; it is too thin to compare error rate and latency against below
// MinRequests requests.
const (
	// BaselineAny, the default: a release is proven good by any service it
	// can compare; a service with a thin baseline is not compared, and is
	// named in not_evaluated. With no comparable service it is inconclusive.
	BaselineAny = "any"
	// BaselineEvery: every service with enough after-window traffic needs a
	// baseline; one with a thin baseline holds the release inconclusive.
	BaselineEvery = "every"
	// BaselineNone: compare against whatever the before window held, however
	// thin, as forge did before the baseline rule. For a low-traffic env that
	// accepts the noise.
	BaselineNone = "none"
)

// BaselinePolicies are the RequireBaseline values, default first.
var BaselinePolicies = []string{BaselineAny, BaselineEvery, BaselineNone}

// Release-health window bounds.
const (
	MinHealthWindow = time.Minute
	MaxHealthWindow = 6 * time.Hour
)

// ShapeReleaseHealth is the env's release-health policy (forge.ReleaseHealth):
// the thresholds a promotion is judged by, and which findings fail it rather
// than warn. The control plane's GetReleaseHealth reads it from the env's
// recorded shape, so the console, the CLI and alerting judge a release by the
// one declaration.
//
// Shape.ReleaseHealth is nil when every value is the default
// ([DefaultReleaseHealth]), so every shape recorded before the field encodes
// and hashes as it did. A reader treats nil as the defaults.
type ShapeReleaseHealth struct {
	// WindowSeconds is the length of each of the two windows compared: before
	// the promotion and after it.
	WindowSeconds int `json:"window_seconds"`
	// MinRequests is the after-window traffic below which a service is not
	// judged (the verdict is inconclusive, not a pass).
	MinRequests int `json:"min_requests"`
	// MaxErrorRateIncrease is the absolute error-rate rise, as a fraction
	// (0.01 = one percentage point), above which error_rate fires.
	MaxErrorRateIncrease float64 `json:"max_error_rate_increase"`
	// MaxP95Ratio is the after/before p95 ratio above which latency fires.
	MaxP95Ratio float64 `json:"max_p95_ratio"`
	// NewIssueMinEvents is the events a new issue needs before it counts.
	NewIssueMinEvents int `json:"new_issue_min_events"`
	// FailOn are the finding codes that fail a release; the rest warn.
	// Sorted, no duplicates.
	FailOn []string `json:"fail_on"`
	// RequireBaseline is how many services the release needs a before window
	// of MinRequests for: BaselineAny, BaselineEvery or BaselineNone. Empty
	// means BaselineAny, and is how the default is recorded, so a shape that
	// keeps it encodes as it did before the field (a reader that predates it
	// is sent none: BundleFeatureReleaseHealthBaseline).
	RequireBaseline string `json:"require_baseline,omitempty"`
	// RegressedIssueMinEvents is the after-window events a regressed issue
	// needs before it counts. 0 means "the same as NewIssueMinEvents", and
	// is how the default is recorded, so a shape that keeps it encodes as it
	// did before the field (a reader that predates it is sent none:
	// BundleFeatureReleaseHealthRegressedMinEvents). 1 counts every
	// regression, as forge did before the field.
	RegressedIssueMinEvents int `json:"regressed_issue_min_events,omitempty"`
	// ErrorRateMinErrors is the after-window errors a service needs before
	// error_rate can fire: below it, a rise in the rate is too few errors to
	// be evidence (one client's broken sessions on a quiet service). 0 means
	// DefaultErrorRateMinErrors, and is how the default is recorded, so a
	// shape that keeps it encodes as it did before the field (a reader that
	// predates it is sent none: BundleFeatureReleaseHealthErrorRate). 1 is
	// the rule before the field: any rise past the limit fires.
	ErrorRateMinErrors int `json:"error_rate_min_errors,omitempty"`
	// BaselineWindowSeconds is the longer stretch before the promotion an
	// endpoint's error rate is compared against when its before window held
	// fewer than MinRequests requests: an endpoint absent from a quiet 15
	// minutes is judged against its own history, not read as error-free. 0
	// means DefaultHealthBaselineWindow, recorded as absent like
	// ErrorRateMinErrors and withheld from a reader by the same feature.
	BaselineWindowSeconds int `json:"baseline_window_seconds,omitempty"`
	// AlertBaselineMinFirings is how many times an alert must have fired in
	// the baseline window before the promotion for the same alert, opening
	// after it, to be read as the env's chronic noise rather than the
	// release's doing: it is then a warn finding, never a failing one
	// (pkg/releasehealth alert_baseline.go). 0 counts every alert opened
	// after the promotion, the rule before the field. Nil means
	// DefaultAlertBaselineMinFirings, and is how the default is recorded, so
	// a shape that keeps it encodes as it did before the field (a reader
	// that predates it is sent none: BundleFeatureReleaseHealthAlertBaseline).
	AlertBaselineMinFirings *int `json:"alert_baseline_min_firings,omitempty"`
	// SEOMaxScoreDrop is how many points (0-100) a static frontend's SEO
	// score, site-wide or on one route, may drop against the same env's
	// audit of the previous release before seo_regressed fires; 0 fires on
	// any drop. Nil means DefaultSiteAuditMaxScoreDrop, and is how the
	// default is recorded, so a shape that keeps it encodes as it did before
	// the field (a reader that predates it is sent none:
	// BundleFeatureReleaseHealthSiteAudit).
	SEOMaxScoreDrop *int `json:"seo_max_score_drop,omitempty"`
	// A11yMaxScoreDrop is SEOMaxScoreDrop for the accessibility score and
	// a11y_regressed.
	A11yMaxScoreDrop *int `json:"a11y_max_score_drop,omitempty"`
}

// The defaults of the fields a shape records as 0.
const (
	DefaultErrorRateMinErrors   = 10
	DefaultHealthBaselineWindow = 24 * time.Hour
	// MaxHealthBaselineWindow bounds BaselineWindowSeconds: the longest
	// range one overview read covers (obsquery.MaxRangeOverview).
	MaxHealthBaselineWindow = 7 * 24 * time.Hour
	// DefaultAlertBaselineMinFirings: an alert that fired even once in the
	// baseline window was already firing before the release.
	DefaultAlertBaselineMinFirings = 1
	// DefaultSiteAuditMaxScoreDrop is the default of SEOMaxScoreDrop and
	// A11yMaxScoreDrop: a score drop of more than 10 points is a regression.
	DefaultSiteAuditMaxScoreDrop = 10
	// MaxSiteAuditScoreDrop bounds them: a score is 0-100.
	MaxSiteAuditScoreDrop = 100
)

// SEOScoreDrop is SEOMaxScoreDrop with the default filled in.
func (h ShapeReleaseHealth) SEOScoreDrop() int { return scoreDropOr(h.SEOMaxScoreDrop) }

// A11yScoreDrop is A11yMaxScoreDrop with the default filled in.
func (h ShapeReleaseHealth) A11yScoreDrop() int { return scoreDropOr(h.A11yMaxScoreDrop) }

func scoreDropOr(p *int) int {
	if p == nil {
		return DefaultSiteAuditMaxScoreDrop
	}
	return *p
}

// AlertBaselineFirings is AlertBaselineMinFirings with the default filled
// in; 0 counts every alert opened after the promotion.
func (h ShapeReleaseHealth) AlertBaselineFirings() int {
	if h.AlertBaselineMinFirings == nil {
		return DefaultAlertBaselineMinFirings
	}
	return *h.AlertBaselineMinFirings
}

// ErrorMinErrors is ErrorRateMinErrors with the default filled in.
func (h ShapeReleaseHealth) ErrorMinErrors() int {
	if h.ErrorRateMinErrors == 0 {
		return DefaultErrorRateMinErrors
	}
	return h.ErrorRateMinErrors
}

// BaselineWindow is BaselineWindowSeconds as a duration, with the default
// filled in.
func (h ShapeReleaseHealth) BaselineWindow() time.Duration {
	if h.BaselineWindowSeconds == 0 {
		return DefaultHealthBaselineWindow
	}
	return time.Duration(h.BaselineWindowSeconds) * time.Second
}

// Baseline is RequireBaseline with the default filled in.
func (h ShapeReleaseHealth) Baseline() string {
	if h.RequireBaseline == "" {
		return BaselineAny
	}
	return h.RequireBaseline
}

// RegressedMinEvents is RegressedIssueMinEvents with the default filled in:
// NewIssueMinEvents.
func (h ShapeReleaseHealth) RegressedMinEvents() int {
	if h.RegressedIssueMinEvents == 0 {
		return h.NewIssueMinEvents
	}
	return h.RegressedIssueMinEvents
}

// DefaultReleaseHealth is the policy of an env that declares none. Latency and
// restarts warn: both are noisy on a fresh rollout. A release-probe failure
// fails: an env has probe findings only when it is probed. The browser
// findings fail (owner, 2026-11-18): for a frontend alone they are the only
// regression a release can show, and they need min_requests page views in
// both windows before they fire. forge.ReleaseHealth.warn_on demotes any of
// them without restating the list.
//
// It must agree with the defaults of forge.ReleaseHealth in kcl/schema.k;
// TestReleaseHealthDefaultsAgreeWithKCL and TestDefaultFailOnMatchesKCL fail
// if they drift.
func DefaultReleaseHealth() ShapeReleaseHealth {
	return ShapeReleaseHealth{
		WindowSeconds:        int((15 * time.Minute).Seconds()),
		MinRequests:          50,
		MaxErrorRateIncrease: 0.01,
		MaxP95Ratio:          1.5,
		NewIssueMinEvents:    3,
		FailOn: []string{HealthAlertFiring, HealthBrowserErrors, HealthBrowserFailedRequests, HealthBrowserPageLoad,
			HealthErrorRate, HealthNewIssue, HealthProbeFailed, HealthRegressedIssue},
	}
}

// Window is WindowSeconds as a duration.
func (h ShapeReleaseHealth) Window() time.Duration {
	return time.Duration(h.WindowSeconds) * time.Second
}

// Fails reports whether finding code fails a release under h.
func (h ShapeReleaseHealth) Fails(code string) bool { return slices.Contains(h.FailOn, code) }

// IsDefault reports whether h is the default policy, which a shape records as
// absent.
func (h *ShapeReleaseHealth) IsDefault() bool {
	return h == nil || reflect.DeepEqual(h.canonical(), DefaultReleaseHealth())
}

// canonical returns a copy with FailOn sorted, deduplicated and never nil.
func (h ShapeReleaseHealth) canonical() ShapeReleaseHealth {
	out := h
	out.FailOn = append([]string{}, h.FailOn...)
	sort.Strings(out.FailOn)
	out.FailOn = slices.Compact(out.FailOn)
	if out.RequireBaseline == BaselineAny {
		out.RequireBaseline = ""
	}
	if out.RegressedIssueMinEvents == out.NewIssueMinEvents {
		out.RegressedIssueMinEvents = 0
	}
	if out.ErrorRateMinErrors == DefaultErrorRateMinErrors {
		out.ErrorRateMinErrors = 0
	}
	if time.Duration(out.BaselineWindowSeconds)*time.Second == DefaultHealthBaselineWindow {
		out.BaselineWindowSeconds = 0
	}
	if out.AlertBaselineMinFirings != nil && *out.AlertBaselineMinFirings == DefaultAlertBaselineMinFirings {
		out.AlertBaselineMinFirings = nil
	}
	if out.SEOMaxScoreDrop != nil && *out.SEOMaxScoreDrop == DefaultSiteAuditMaxScoreDrop {
		out.SEOMaxScoreDrop = nil
	}
	if out.A11yMaxScoreDrop != nil && *out.A11yMaxScoreDrop == DefaultSiteAuditMaxScoreDrop {
		out.A11yMaxScoreDrop = nil
	}
	return out
}

func (h ShapeReleaseHealth) validate() error {
	if w := h.Window(); w < MinHealthWindow || w > MaxHealthWindow {
		return fmt.Errorf("%w: shape release_health window %s is outside %s to %s", ErrInvalid, w, MinHealthWindow, MaxHealthWindow)
	}
	if h.MinRequests < 1 {
		return fmt.Errorf("%w: shape release_health min_requests must be at least 1", ErrInvalid)
	}
	if h.MaxErrorRateIncrease <= 0 || h.MaxErrorRateIncrease > 1 {
		return fmt.Errorf("%w: shape release_health max_error_rate_increase %v must be a fraction in (0, 1]", ErrInvalid, h.MaxErrorRateIncrease)
	}
	if h.MaxP95Ratio <= 1 {
		return fmt.Errorf("%w: shape release_health max_p95_ratio %v must be above 1", ErrInvalid, h.MaxP95Ratio)
	}
	if h.NewIssueMinEvents < 1 {
		return fmt.Errorf("%w: shape release_health new_issue_min_events must be at least 1", ErrInvalid)
	}
	if h.RegressedIssueMinEvents < 0 {
		return fmt.Errorf("%w: shape release_health regressed_issue_min_events must be at least 1 (or absent: the same as new_issue_min_events)", ErrInvalid)
	}
	if h.ErrorRateMinErrors < 0 {
		return fmt.Errorf("%w: shape release_health error_rate_min_errors must be at least 1 (or absent: %d)", ErrInvalid, DefaultErrorRateMinErrors)
	}
	if h.AlertBaselineMinFirings != nil && *h.AlertBaselineMinFirings < 0 {
		return fmt.Errorf("%w: shape release_health alert_baseline_min_firings must be at least 0 (0 counts every alert; absent: %d)", ErrInvalid, DefaultAlertBaselineMinFirings)
	}
	for _, d := range []struct {
		name string
		p    *int
	}{{"seo_max_score_drop", h.SEOMaxScoreDrop}, {"a11y_max_score_drop", h.A11yMaxScoreDrop}} {
		if d.p != nil && (*d.p < 0 || *d.p > MaxSiteAuditScoreDrop) {
			return fmt.Errorf("%w: shape release_health %s %d must be 0 to %d score points (0 fires on any drop; absent: %d)",
				ErrInvalid, d.name, *d.p, MaxSiteAuditScoreDrop, DefaultSiteAuditMaxScoreDrop)
		}
	}
	if h.BaselineWindowSeconds != 0 {
		if b := h.BaselineWindow(); b < h.Window() || b > MaxHealthBaselineWindow {
			return fmt.Errorf("%w: shape release_health baseline_window %s is outside the window (%s) to %s", ErrInvalid, b, h.Window(), MaxHealthBaselineWindow)
		}
	}
	if h.RequireBaseline != "" && !slices.Contains(BaselinePolicies, h.RequireBaseline) {
		return fmt.Errorf("%w: shape release_health require_baseline %q (expected one of %v)", ErrInvalid, h.RequireBaseline, BaselinePolicies)
	}
	for _, c := range h.FailOn {
		if !slices.Contains(HealthFindings, c) {
			return fmt.Errorf("%w: shape release_health fails on %q (expected one of %v)", ErrInvalid, c, HealthFindings)
		}
	}
	return nil
}
