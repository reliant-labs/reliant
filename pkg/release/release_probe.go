package release

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Release-probe kinds: what a [ShapeReleaseProbe] targets.
const (
	// ReleaseProbeKindFrontend is a hosted static frontend (forge.Frontend).
	ReleaseProbeKindFrontend = "frontend"
	// ReleaseProbeKindWorkload is a hosted workload that exposes a port.
	ReleaseProbeKindWorkload = "workload"
)

// Release-probe sources: where a [ShapeReleaseProbe] came from, as
// UptimeSource* are for uptime checks.
const (
	// ReleaseProbeSourceUser is a probe the author declared (forge.Probe).
	ReleaseProbeSourceUser = "user"
	// ReleaseProbeSourceDefault is a probe forge derived: the root of every
	// hosted static frontend that declares none.
	ReleaseProbeSourceDefault = "default"
)

// Bounds of a release probe, the same as forge.Probe's KCL checks.
const (
	MaxReleaseProbePaths   = 5
	MaxReleaseProbePathLen = 256
	MinReleaseProbeRate    = 1
	MaxReleaseProbeRate    = 12
	MinReleaseProbeTimeout = time.Second
	MaxReleaseProbeTimeout = 30 * time.Second
)

// The release-probe contract: the names the control plane's prober, its edge
// (workspace-proxy) and its release-health judge share with forge's workloads
// and queries. Declared once, here, as the UptimeMetric* names are.
const (
	// ProbeHeader is the signed header the prober sets on every probe request
	// and the platform edge verifies, then deletes:
	// "v=1, k=<kid>, e=<env_id>, t=<unix seconds>, n=<nonce>, s=<sig>".
	ProbeHeader = "Forge-Probe"
	// ProbeCacheBustParam is the query parameter the prober appends to a
	// static frontend's path (the header's nonce), so the request misses the
	// CDN cache and reaches the origin that verifies it. Never sent to a
	// workload.
	ProbeCacheBustParam = "_fprobe"
	// ProbeEdgeHeader is what the edge stamps ("1") on a verified probe it
	// proxies to a hosted workload. A workload honours it only when it trusts
	// the Reliant edge, and always deletes it before its handler runs.
	ProbeEdgeHeader = "X-Reliant-Probe"
	// ProbeAttr is the span and log attribute, value true, a workload writes
	// while serving a probe, so readers keep probes out of real traffic.
	ProbeAttr = "forge.probe"
	// ProbeSource is the reliant.source of the prober's own metrics.
	ProbeSource = "probe"

	// ProbeMetricRequests counts probe requests: a monotonic DELTA Sum, unit
	// {request}.
	ProbeMetricRequests = "reliant.probe.requests"
	// ProbeMetricDuration times probe requests whose outcome is ok or failed:
	// a DELTA Histogram, in seconds.
	ProbeMetricDuration = "reliant.probe.duration"
	// ProbeAttrPath is the path a probe requested, without the cache-bust
	// parameter.
	ProbeAttrPath = "probe.path"
	// ProbeAttrOutcome is one of ProbeOutcome*.
	ProbeAttrOutcome = "probe.outcome"
	// ProbeAttrStatusClass is the response's class, "1xx" to "5xx"; absent
	// when there was no response.
	ProbeAttrStatusClass = "status_class"
	// ProbeAttrError is the transport error's type (the uptime prober's error
	// set); absent when there was a response.
	ProbeAttrError = "error.type"
)

// Probe outcomes: the values of [ProbeAttrOutcome].
const (
	// ProbeOutcomeOK: the status is one the probe expects.
	ProbeOutcomeOK = "ok"
	// ProbeOutcomeFailed: any other status but a throttle, or a transport
	// error that a retry did not clear.
	ProbeOutcomeFailed = "failed"
	// ProbeOutcomeThrottled: 429, or 503 with Retry-After. Neither a success
	// nor a failure.
	ProbeOutcomeThrottled = "throttled"
	// ProbeOutcomeDegraded: a failure recorded while the prober's own canary
	// was failing, so it is the prober's fault as likely as the target's.
	// Never judged a failure.
	ProbeOutcomeDegraded = "degraded"
)

// ShapeReleaseProbe is one target of an env's release probes (forge.Probe):
// steady synthetic traffic the control plane sends to a hosted frontend or
// workload, so a release can be judged when real traffic is too thin. The
// control plane's prober reads it from the env's recorded shape; the
// declaration is the whole truth, and no default is ever derived for a shape
// that carries none.
//
// The target is a name, never an address: the control plane resolves its
// platform hostname. A probe carries no body, cookie or credential.
type ShapeReleaseProbe struct {
	// Target is the hosted frontend's or workload's name. Unique in the env.
	Target string `json:"target"`
	// Kind is [ReleaseProbeKindFrontend] or [ReleaseProbeKindWorkload].
	Kind string `json:"kind"`
	// Paths are requested on the target's hostname in turn, in this order.
	Paths []string `json:"paths"`
	// Method is GET or HEAD.
	Method string `json:"method"`
	// RatePerMin is the requests a minute across all paths, before the
	// control plane's cap.
	RatePerMin int `json:"rate_per_min"`
	// TimeoutSeconds bounds one request: connect, TLS, headers and body.
	TimeoutSeconds int `json:"timeout_seconds"`
	// ExpectStatus lists the statuses that count as success: "200", or a
	// class ("2xx"). Empty means [DefaultUptimeExpectStatus].
	ExpectStatus []string `json:"expect_status,omitempty"`
	// Source is [ReleaseProbeSourceUser] or [ReleaseProbeSourceDefault].
	Source string `json:"source"`
}

// StatusOK reports whether an HTTP status counts as success for this probe.
func (p ShapeReleaseProbe) StatusOK(status int) bool {
	return ShapeUptimeCheck{ExpectStatus: p.ExpectStatus}.StatusOK(status)
}

// Interval is the time between two requests to the target: a minute divided
// by RatePerMin. Zero for a probe with no rate.
func (p ShapeReleaseProbe) Interval() time.Duration {
	if p.RatePerMin <= 0 {
		return 0
	}
	return time.Minute / time.Duration(p.RatePerMin)
}

// Timeout is TimeoutSeconds as a duration.
func (p ShapeReleaseProbe) Timeout() time.Duration {
	return time.Duration(p.TimeoutSeconds) * time.Second
}

func (p ShapeReleaseProbe) validate() error {
	if !targetNameRE.MatchString(p.Target) {
		return fmt.Errorf("%w: release probe target %q is not a workload or frontend name", ErrInvalid, p.Target)
	}
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: release probe %q: %s", ErrInvalid, p.Target, fmt.Sprintf(format, args...))
	}
	switch p.Kind {
	case ReleaseProbeKindFrontend, ReleaseProbeKindWorkload:
	default:
		return bad("kind %q (expected %s or %s)", p.Kind, ReleaseProbeKindFrontend, ReleaseProbeKindWorkload)
	}
	if n := len(p.Paths); n < 1 || n > MaxReleaseProbePaths {
		return bad("name 1 to %d paths, not %d", MaxReleaseProbePaths, n)
	}
	for i, path := range p.Paths {
		switch {
		case slices.Contains(p.Paths[:i], path):
			return bad("names path %q twice", path)
		case !strings.HasPrefix(path, "/"):
			return bad("path %q must start with '/'", path)
		case strings.Contains(path, "?"):
			return bad("path %q has a query", path)
		case strings.Contains(path, "#"):
			return bad("path %q has a fragment", path)
		case strings.Contains(path, ".."):
			return bad("path %q has '..'", path)
		case strings.Contains(path, "//"):
			return bad("path %q has an empty segment ('//')", path)
		case slices.Contains(strings.Split(path, "/"), "."):
			return bad("path %q has a '.' segment", path)
		case strings.ContainsRune(path, '%'):
			return bad("path %q is percent-encoded", path)
		case strings.IndexFunc(path, notProbePathChar) >= 0:
			return bad("path %q has a character outside A-Z a-z 0-9 and -._~!$&'()*+,;=:@/", path)
		case len(path) > MaxReleaseProbePathLen:
			return bad("path is longer than %d characters", MaxReleaseProbePathLen)
		}
	}
	switch p.Method {
	case "GET", "HEAD":
	default:
		return bad("method %q (expected GET or HEAD)", p.Method)
	}
	if p.RatePerMin < MinReleaseProbeRate || p.RatePerMin > MaxReleaseProbeRate {
		return bad("rate_per_min %d must be %d to %d", p.RatePerMin, MinReleaseProbeRate, MaxReleaseProbeRate)
	}
	if to := p.Timeout(); to < MinReleaseProbeTimeout || to > MaxReleaseProbeTimeout {
		return bad("timeout %s must be %s to %s", to, MinReleaseProbeTimeout, MaxReleaseProbeTimeout)
	}
	if p.TimeoutSeconds*p.RatePerMin >= 60 {
		return bad("timeout %s must be shorter than one interval (%s at %d a minute)", p.Timeout(), p.Interval(), p.RatePerMin)
	}
	for _, s := range p.ExpectStatus {
		if !uptimeStatusRE.MatchString(s) {
			return bad("expect_status %q (a status like 200, or a class like 2xx)", s)
		}
	}
	switch p.Source {
	case ReleaseProbeSourceUser, ReleaseProbeSourceDefault:
	default:
		return bad("source %q (expected %s or %s)", p.Source, ReleaseProbeSourceUser, ReleaseProbeSourceDefault)
	}
	return nil
}

// notProbePathChar reports a rune a release-probe path may not hold. A path
// is a plain origin-relative URL path: RFC 3986 pchars and '/', with no
// percent-encoding, so every HTTP client, proxy and URL parser reads it the
// same way and the request stays on the target's host. A backslash, a
// control character, a space or a non-ASCII rune is refused.
func notProbePathChar(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return false
	}
	return !strings.ContainsRune("-._~!$&'()*+,;=:@/", r)
}

// validateReleaseProbes applies forge.Probe's rules to each probe, and
// refuses a target probed twice: the prober runs one job per (env, target).
func validateReleaseProbes(probes []ShapeReleaseProbe) error {
	seen := map[string]bool{}
	for _, p := range probes {
		if seen[p.Target] {
			return fmt.Errorf("%w: shape names release probe target %q twice", ErrInvalid, p.Target)
		}
		seen[p.Target] = true
		if err := p.validate(); err != nil {
			return err
		}
	}
	return nil
}

// canonicalReleaseProbes is the probes sorted by target, with each probe's
// statuses spelled the way the default is: a producer that lists the default
// statuses explicitly hashes the same as one that leaves them out. Paths keep
// their order, which is the request rotation.
func canonicalReleaseProbes(in []ShapeReleaseProbe) []ShapeReleaseProbe {
	if len(in) == 0 {
		return nil
	}
	out := make([]ShapeReleaseProbe, len(in))
	for i, p := range in {
		p.Paths = slices.Clone(p.Paths)
		p.ExpectStatus = sortedStrings(p.ExpectStatus)
		if slices.Equal(p.ExpectStatus, DefaultUptimeExpectStatus) {
			p.ExpectStatus = nil
		}
		out[i] = p
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// withoutProbeFindings returns h with the release-probe finding codes removed
// from FailOn, for a reader that predates them: its validation refuses a
// code it does not know, and it never reports one. A copy when anything is
// removed: h is not modified.
func withoutProbeFindings(h *ShapeReleaseHealth) *ShapeReleaseHealth {
	probe := func(c string) bool { return c == HealthProbeFailed || c == HealthProbeLatency }
	if h == nil || !slices.ContainsFunc(h.FailOn, probe) {
		return h
	}
	out := *h
	out.FailOn = slices.DeleteFunc(slices.Clone(h.FailOn), probe)
	return &out
}
