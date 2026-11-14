package releasehealth

// THE BASELINE (forge bug d1dc1977). Error rate, latency and the browser
// figures are COMPARATIVE: each holds the after window against the window
// before the promotion. A before window with fewer than min_requests requests
// (page views, for browser figures) is no baseline: compared against it, a
// release proves nothing good. So, by default, a service is judged on those
// findings only when BOTH windows reach min_requests; one whose after window
// does and whose before window does not has a thin baseline, is named in
// not_evaluated ([NotEvaluatedService], [NotEvaluatedBrowser]), and cannot
// carry a PASS. When no service can be compared the verdict is INCONCLUSIVE
// ([ReasonThinBaseline]).
//
// require_baseline (release.ShapeReleaseHealth.RequireBaseline, the
// ThresholdRequireBaseline override) is the env's own gate:
//
//   - any (the default): as above.
//   - every: a thin baseline on any service holds the release INCONCLUSIVE,
//     however many others were compared.
//   - none: compare against whatever the before window held, however thin
//     (the rule before d1dc1977).
//
// The absolute findings need no baseline and are judged whatever the before
// window held, under every policy: a new or regressed issue, a firing alert,
// a workload restart. A thin baseline never hides one of them.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// Prefixes of a not_evaluated entry that names a service whose comparative
// findings were not judged because its before window was below min_requests:
// "service:<name>" for its request figures (error_rate, latency),
// "browser:<name>" for its browser figures. The other entries are finding
// codes the backend could not evaluate at all.
const (
	NotEvaluatedService = "service:"
	NotEvaluatedBrowser = "browser:"
)

// ReasonThinBaseline: a service saw enough traffic after the promotion to
// judge, but too little before it to compare against: none could be compared,
// or (require_baseline every) one could not.
const ReasonThinBaseline = "thin_baseline"

// gauge is what decides whether a service can be compared: min_requests and
// the require_baseline policy.
type gauge struct {
	minRequests int
	policy      string
}

func (t Thresholds) gauge() gauge {
	return gauge{minRequests: t.MinRequests, policy: baselineOr(t.RequireBaseline)}
}

// gaugeOf reads the gauge from a judged report's thresholds; a report from a
// control plane that predates require_baseline lists none, which is the
// default.
func gaugeOf(ths []Threshold) gauge {
	g := gauge{minRequests: minRequestsOf(ths), policy: release.BaselineAny}
	for _, th := range ths {
		if th.Name == ThresholdRequireBaseline && slices.Contains(release.BaselinePolicies, th.Value) {
			g.policy = th.Value
		}
	}
	return g
}

func baselineOr(p string) string {
	if p == "" {
		return release.BaselineAny
	}
	return p
}

// comparable reports whether s's request figures are judged: min_requests
// requests after the promotion and, unless the policy is none, before it.
func (g gauge) comparable(s ServiceHealth) bool {
	m := int64(g.minRequests)
	return s.After.Requests >= m && (g.policy == release.BaselineNone || s.Before.Requests >= m)
}

// browserComparable is comparable for s's browser figures, by page views.
func (g gauge) browserComparable(s ServiceHealth) bool {
	b, m := s.Browser, int64(g.minRequests)
	return b != nil && b.After.PageViews >= m && (g.policy == release.BaselineNone || b.Before.PageViews >= m)
}

// thin reports whether s's request figures, or its browser figures, reached
// min_requests after the promotion but not before it. Under policy none
// nothing is thin: a thin before window is compared against.
func (g gauge) thin(s ServiceHealth) (requests, browser bool) {
	if g.policy == release.BaselineNone {
		return false, false
	}
	m := int64(g.minRequests)
	requests = s.After.Requests >= m && s.Before.Requests < m
	browser = s.Browser != nil && s.Browser.After.PageViews >= m && s.Browser.Before.PageViews < m
	return requests, browser
}

// entries are the not_evaluated entries of services' thin baselines, in
// their order.
func (g gauge) entries(services []ServiceHealth) []string {
	var out []string
	for _, s := range services {
		requests, browser := g.thin(s)
		if requests {
			out = append(out, NotEvaluatedService+s.Service)
		}
		if browser {
			out = append(out, NotEvaluatedBrowser+s.Service)
		}
	}
	return out
}

// holds reports whether the policy holds the release INCONCLUSIVE over
// services whatever else they show: every, with a thin baseline among them.
func (g gauge) holds(services []ServiceHealth) bool {
	return g.policy == release.BaselineEvery && len(g.entries(services)) > 0
}

// withNotEvaluated is have plus each of add it does not hold, in a new slice.
func withNotEvaluated(have, add []string) []string {
	if len(add) == 0 {
		return have
	}
	out := slices.Clone(have)
	for _, e := range add {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// Compared reports whether s's request figures were compared under r's
// thresholds: min_requests requests in the after window and, unless
// require_baseline is none, in the before window.
func (r Report) Compared(s ServiceHealth) bool { return gaugeOf(r.Thresholds).comparable(s) }

// WithThinBaselines applies the baseline rule to a judged report: each
// service with a thin baseline is named in NotEvaluated, and the verdict is
// recomputed. Like the other folds it only adds evidence against a release:
// a PASS the rule does not allow becomes INCONCLUSIVE, a FAIL stays a FAIL.
// It is idempotent, [Evaluate] applies the rule itself, and it is how
// `forge obs check` holds a control plane whose answer predates the rule to
// it.
func WithThinBaselines(r Report) Report {
	out := r
	out.NotEvaluated = withNotEvaluated(r.NotEvaluated, gaugeOf(r.Thresholds).entries(r.Services))
	out.Verdict = worse(r.Verdict, verdictOf(out.Findings, out.AfterComplete, out.Services, gaugeOf(out.Thresholds), out.unproven()))
	return out
}

// hasThinBaseline reports whether any service of r has a thin baseline.
func (r Report) hasThinBaseline() bool {
	return len(gaugeOf(r.Thresholds).entries(r.Services)) > 0
}

// thinBaselineWhy is InconclusiveWhy for [ReasonThinBaseline]: which services
// had too little traffic before the promotion, with their counts.
func thinBaselineWhy(r Report) string {
	g := gaugeOf(r.Thresholds)
	var parts []string
	for _, s := range r.Services {
		requests, browser := g.thin(s)
		if requests {
			parts = append(parts, fmt.Sprintf("%s had %d requests before the promotion (%d after)", s.Service, s.Before.Requests, s.After.Requests))
		}
		if browser {
			parts = append(parts, fmt.Sprintf("%s had %d page views before the promotion (%d after)", s.Service, s.Browser.Before.PageViews, s.Browser.After.PageViews))
		}
	}
	const shown = 3
	if len(parts) > shown {
		parts = append(parts[:shown], fmt.Sprintf("%d more", len(parts)-shown))
	}
	head := "the before window is too thin to compare against"
	if g.policy == release.BaselineEvery {
		head = "require_baseline is every, and a service's before window is too thin to compare against"
	}
	return fmt.Sprintf("%s: %s, below min_requests (%d), so error rate and latency could not be judged against a baseline",
		head, strings.Join(parts, "; "), g.minRequests)
}
