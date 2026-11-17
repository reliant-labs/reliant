package releasehealth

// THE ERROR-RATE RULE. error_rate compares a service's error rate after the
// promotion with its rate before it. Compared service-wide, that measures
// which endpoints each window happened to hold as much as whether any of
// them got worse. control-plane v1.20.1-rc.1 (2026-11-15, control-plane
// #2820) was halted on reliant-api-server going 0.0% -> 3.3%: 8 errors of
// 246 requests, every one a terminal.session websocket from one user daemon
// whose machine did not answer. That endpoint had failed 9.2% of the time for
// the 12 hours before; the 15-minute before window held none of it, so the
// service read 0%, and every Connect RPC was at 0% in both windows.
//
// Two rules, each general, close it without hiding a real regression:
//
//   - Per endpoint. With per-endpoint figures (ServiceHealth.Endpoints) the
//     rise is the after-window request-weighted mean of each endpoint's own
//     rise: sum(after requests * (after rate - baseline rate)) / sum(after
//     requests). It is forge#1728's latency rule applied to errors: a change
//     in WHICH endpoints a window holds no longer reads as a change in how
//     often they fail, while a busy endpoint that got worse dominates the
//     mean, and a rise across many endpoints adds up exactly as it did
//     service-wide. An endpoint's baseline is its before window when that
//     held min_requests requests; else its figures over baseline_window
//     before the promotion (EndpointHealth.Baseline) when THAT did; else the
//     service's before rate, so an endpoint with no history is never read as
//     error-free (and the rule degrades to the service-wide one, never past
//     it). Without per-endpoint figures it is the service-wide rise.
//   - A floor. error_rate fires only when the service had at least
//     error_rate_min_errors (default 10) errors in the after window. A rise
//     of a few errors on a quiet service is one client's bad minute, not
//     evidence a release broke something, by the reasoning that gave
//     new_issue and regressed_issue their min-events thresholds. 1 is the
//     rule before the floor. It is applied to a control plane's answer too
//     ([WithErrorRateMinErrors]), which needs no per-endpoint figures: the
//     errors are the after rate times the after requests.
//
// Neither touches what makes a service comparable (baseline.go): a service
// is still judged only with min_requests in both windows.

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// ErrorRateIncrease is how much s's error rate rose, and whether it was
// judged endpoint by endpoint (above). minRequests is the requests an
// endpoint's before window or baseline needs to be its baseline; pass 1 to
// take any before traffic, as require_baseline none does.
func (s ServiceHealth) ErrorRateIncrease(minRequests int) (float64, bool) {
	flat := s.After.ErrorRate - s.Before.ErrorRate
	var excess, weight float64
	for _, e := range s.Endpoints {
		if e.After.Requests <= 0 {
			continue // gone from the after window: nothing of it to judge
		}
		n := float64(e.After.Requests)
		excess += n * (e.After.ErrorRate - e.BaselineErrorRate(s.Before.ErrorRate, minRequests))
		weight += n
	}
	if weight == 0 {
		return flat, false
	}
	return excess / weight, true
}

// BaselineErrorRate is the error rate e's after window is compared against:
// its before window's when that held minRequests requests, else its
// baseline_window's when that did, else serviceBefore, the service's before
// rate.
func (e EndpointHealth) BaselineErrorRate(serviceBefore float64, minRequests int) float64 {
	m := int64(max(minRequests, 1))
	switch {
	case e.Before.Requests >= m:
		return e.Before.ErrorRate
	case e.Baseline != nil && e.Baseline.Requests >= m:
		return e.Baseline.ErrorRate
	}
	return serviceBefore
}

// AfterErrors is the errors s served in the after window: what
// error_rate_min_errors is held against.
func (s ServiceHealth) AfterErrors() int64 {
	return int64(math.Round(s.After.ErrorRate * float64(s.After.Requests)))
}

// endpointMin is the requests an endpoint's baseline needs: min_requests,
// or any under require_baseline none, which compares however thin.
func (g gauge) endpointMin() int {
	if g.policy == release.BaselineNone {
		return 1
	}
	return g.minRequests
}

// errorRateFinding is s's error_rate finding under t, if it has one. s is
// comparable (baseline.go).
func errorRateFinding(s ServiceHealth, t Thresholds) (Finding, bool) {
	inc, perEndpoint := s.ErrorRateIncrease(t.gauge().endpointMin())
	errs := s.AfterErrors()
	if inc <= t.MaxErrorRateIncrease || errs < int64(t.ErrorRateMinErrors) {
		return Finding{}, false
	}
	per := ""
	if perEndpoint {
		per = " per endpoint"
	}
	return Finding{Code: release.HealthErrorRate, Service: s.Service, Value: inc, Threshold: t.MaxErrorRateIncrease,
		Message: fmt.Sprintf("%s: error rate %s -> %s (+%s%s, limit +%s; %d errors)", s.Service,
			pct(s.Before.ErrorRate), pct(s.After.ErrorRate), pct(inc), per, pct(t.MaxErrorRateIncrease), errs)}, true
}

// WithErrorRateMinErrors applies error_rate_min_errors to a judged report:
// an error_rate finding whose service had fewer after-window errors than it
// is no finding. The threshold is read from the report, except that an
// override (the request's --threshold pairs, by name; nil for none) wins; a
// report judged before it existed is held to the default, and lists it. A
// finding whose service the report does not list is kept: there is nothing
// to count. The verdict is recomputed when a finding is removed, so it can
// improve: what is taken out was never evidence. [Evaluate] judges by the
// same rule, so over its report this changes nothing; it is how the floor
// reaches a control plane's answer judged without it (`forge obs check`
// applies it before attribution).
func WithErrorRateMinErrors(r Report, overrides map[string]string) Report {
	ths := withOverride(r.Thresholds, overrides, ThresholdErrorRateMinErrors)
	floor, listed := release.DefaultErrorRateMinErrors, false
	for _, th := range ths {
		if th.Name != ThresholdErrorRateMinErrors {
			continue
		}
		listed = true
		if n, err := strconv.Atoi(th.Value); err == nil && n >= 1 {
			floor = n
		}
	}
	out := r
	if !listed {
		at := slices.IndexFunc(ths, func(th Threshold) bool { return th.Name == ThresholdMaxErrorRateIncrease }) + 1
		if at == 0 {
			at = len(ths)
		}
		ths = slices.Insert(slices.Clone(ths), at, Threshold{ThresholdErrorRateMinErrors, strconv.Itoa(floor), SourceDefault})
	}
	out.Thresholds = ths
	errs := map[string]int64{}
	for _, s := range r.Services {
		errs[s.Service] = s.AfterErrors()
	}
	out.Findings = slices.DeleteFunc(slices.Clone(r.Findings), func(f Finding) bool {
		n, ok := errs[f.Service]
		return f.Code == release.HealthErrorRate && ok && n < int64(floor)
	})
	if len(out.Findings) == len(r.Findings) {
		out.Findings = r.Findings
		return out // nothing below the floor: the report as judged, with the threshold listed
	}
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, gaugeOf(out.Thresholds), out.unproven())
	return out
}

// withOverride is ths with threshold name replaced by the request's value
// for it, tagged SourceRequest, when overrides sets a positive integer.
func withOverride(ths []Threshold, overrides map[string]string, name string) []Threshold {
	v, ok := overrides[name]
	if n, err := strconv.Atoi(strings.TrimSpace(v)); !ok || err != nil || n < 1 {
		return ths
	}
	th := Threshold{name, v, SourceRequest}
	i := slices.IndexFunc(ths, func(t Threshold) bool { return t.Name == name })
	switch {
	case i < 0:
		return append(slices.Clone(ths), th)
	case ths[i] == th:
		return ths
	}
	out := slices.Clone(ths)
	out[i] = th
	return out
}
