package releasehealth

import (
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/pkg/release"
)

// What a service's figures were measured from (ServiceHealth.Signal).
//
// A release is judged on each service's own server spans. A service that sent
// none in either window (a static site, an app without OpenTelemetry) is
// judged on what the hosting platform's ingress measured for it instead: the
// requests it answered, their status classes and their time to headers
// (pkg/obsquery Overview.IngressRED). A frontend that sends browser telemetry
// is judged on that too, beside either or alone (Overview.BrowserRED): its
// JavaScript errors per page view, the share of its fetch/XHR requests that
// failed, and its p95 page load. [Combine] applies that precedence.
const (
	SignalSpans   = "spans"
	SignalIngress = "ingress"
	SignalBrowser = "browser"
)

// BrowserStats are one frontend's browser figures over one window. Its fields
// are pkg/obsquery.BrowserFigures', in the same order, so one converts to the
// other.
type BrowserStats struct {
	PageViews      int64   `json:"page_views"`
	PageLoads      int64   `json:"page_loads"`
	JSErrors       int64   `json:"js_errors"`
	Requests       int64   `json:"requests"`
	FailedRequests int64   `json:"failed_requests"`
	PageLoadP95Ms  float64 `json:"page_load_p95_ms"`
}

// JSErrorRate is JavaScript errors per page view (0 with no page views). It is
// a rate, not a fraction: a page can raise several.
func (b BrowserStats) JSErrorRate() float64 {
	if b.PageViews <= 0 {
		return 0
	}
	return float64(b.JSErrors) / float64(b.PageViews)
}

// FailedRequestRate is the fraction of the page's own requests that failed.
func (b BrowserStats) FailedRequestRate() float64 {
	if b.Requests <= 0 {
		return 0
	}
	return float64(b.FailedRequests) / float64(b.Requests)
}

// BrowserHealth is one frontend's browser figures, before and after.
type BrowserHealth struct {
	Before BrowserStats `json:"before"`
	After  BrowserStats `json:"after"`
}

// seen reports whether the frontend sent anything in either window.
func (b *BrowserHealth) seen() bool {
	if b == nil {
		return false
	}
	return b.Before.PageViews > 0 || b.After.PageViews > 0 || b.Before.Requests > 0 || b.After.Requests > 0 ||
		b.Before.JSErrors > 0 || b.After.JSErrors > 0
}

// judged reports whether the after window holds enough traffic to judge the
// service on: min_requests server requests, or min_requests page views.
func (s ServiceHealth) judged(minRequests int) bool {
	return s.After.Requests >= int64(minRequests) || (s.Browser != nil && s.Browser.After.PageViews >= int64(minRequests))
}

// Combine builds the services a release is judged on from each signal, in
// precedence order:
//
//  1. spans: each service's own server-span figures, as given. A service with
//     a span request in either window is judged on them, whatever else says.
//  2. ingress: a service with no span request in either window takes the
//     ingress figures of the same name, marked SignalIngress (no endpoints,
//     no version share: ingress carries neither).
//  3. browser: each frontend's browser figures, attached to the service of
//     the same name, or added on their own (SignalBrowser) when there is
//     none.
//
// Spans' order is kept; services added from ingress and browser follow, by
// name. A span service with no figures is replaced by its ingress figures, or
// kept when there are none.
func Combine(spans, ingress []ServiceHealth, browser map[string]BrowserHealth) []ServiceHealth {
	out := make([]ServiceHealth, 0, len(spans)+len(ingress)+len(browser))
	at := map[string]int{}
	ingressBy := map[string]ServiceHealth{}
	for _, s := range ingress {
		ingressBy[s.Service] = s
	}
	for _, s := range spans {
		if s.Before.Requests == 0 && s.After.Requests == 0 {
			if in, ok := ingressBy[s.Service]; ok && (in.Before.Requests > 0 || in.After.Requests > 0) {
				continue // judged on ingress below
			}
		}
		if s.Signal == "" {
			s.Signal = SignalSpans
		}
		at[s.Service] = len(out)
		out = append(out, s)
	}
	var added []ServiceHealth
	for _, s := range ingress {
		if _, ok := at[s.Service]; ok {
			continue
		}
		added = append(added, ServiceHealth{Service: s.Service, Before: s.Before, After: s.After, Signal: SignalIngress})
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Service < added[j].Service })
	for _, s := range added {
		at[s.Service] = len(out)
		out = append(out, s)
	}
	names := make([]string, 0, len(browser))
	for n := range browser {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := browser[n]
		if !b.seen() {
			continue
		}
		if i, ok := at[n]; ok {
			out[i].Browser = &b
			continue
		}
		out = append(out, ServiceHealth{Service: n, Signal: SignalBrowser, Browser: &b})
	}
	return out
}

// browserFindings judges a frontend's browser figures: the after window needs
// min_requests page views. The JavaScript error rate (per page view) and the
// failed-request rate are held to max_error_rate_increase, the p95 page load
// to max_p95_ratio. They warn unless fail_on names them.
func browserFindings(s ServiceHealth, t Thresholds) []Finding {
	b := s.Browser
	if b == nil || b.After.PageViews < int64(t.MinRequests) {
		return nil
	}
	var out []Finding
	if inc := b.After.JSErrorRate() - b.Before.JSErrorRate(); inc > t.MaxErrorRateIncrease {
		out = append(out, Finding{Code: release.HealthBrowserErrors, Service: s.Service, Value: inc, Threshold: t.MaxErrorRateIncrease,
			Message: fmt.Sprintf("%s: JavaScript errors per page view %.3f -> %.3f (+%.3f, limit +%.3g)", s.Service,
				b.Before.JSErrorRate(), b.After.JSErrorRate(), inc, t.MaxErrorRateIncrease)})
	}
	if b.After.Requests >= int64(t.MinRequests) {
		if inc := b.After.FailedRequestRate() - b.Before.FailedRequestRate(); inc > t.MaxErrorRateIncrease {
			out = append(out, Finding{Code: release.HealthBrowserFailedRequests, Service: s.Service, Value: inc, Threshold: t.MaxErrorRateIncrease,
				Message: fmt.Sprintf("%s: failed browser requests %s -> %s (+%s, limit +%s)", s.Service,
					pct(b.Before.FailedRequestRate()), pct(b.After.FailedRequestRate()), pct(inc), pct(t.MaxErrorRateIncrease))})
		}
	}
	if b.Before.PageLoadP95Ms > 0 && b.After.PageLoadP95Ms > 0 {
		if ratio := b.After.PageLoadP95Ms / b.Before.PageLoadP95Ms; ratio > t.MaxP95Ratio {
			out = append(out, Finding{Code: release.HealthBrowserPageLoad, Service: s.Service, Value: ratio, Threshold: t.MaxP95Ratio,
				Message: fmt.Sprintf("%s: p95 page load %s -> %s (%.2fx, limit %.2gx)", s.Service,
					ms(b.Before.PageLoadP95Ms), ms(b.After.PageLoadP95Ms), ratio, t.MaxP95Ratio)})
		}
	}
	return out
}
