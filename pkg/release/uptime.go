package release

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/netguard"
)

// Uptime check sources: where a [ShapeUptimeCheck] came from.
const (
	// UptimeSourceUser is a check the author declared (forge.UptimeCheck).
	UptimeSourceUser = "user"
	// UptimeSourceDefault is a check forge derived: one per public frontend
	// and per public HTTP route of a hosted env.
	UptimeSourceDefault = "default"
)

// Bounds of a check's schedule. The floor keeps one check from becoming a
// load generator against the endpoint it watches; the ceiling keeps a check
// from being so sparse that a consecutive-failure alert could never fire.
const (
	MinUptimeInterval = 30 * time.Second
	MaxUptimeInterval = 10 * time.Minute
	MinUptimeTimeout  = time.Second
	MaxUptimeTimeout  = time.Minute
	// MaxUptimeExpectBody bounds the body substring a check looks for.
	MaxUptimeExpectBody = 256
)

// DefaultUptimeExpectStatus is what a check with no declared statuses accepts:
// any 2xx or 3xx. A redirect is an answer, not an outage.
var DefaultUptimeExpectStatus = []string{"2xx", "3xx"}

var (
	uptimeNameRE   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	uptimeStatusRE = regexp.MustCompile(`^[1-5]([0-9]{2}|xx)$`)
	targetNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// ShapeUptimeCheck is one outside-in HTTP check of an env: what to request, how
// often, and what counts as up. The control plane's prober reads it from the
// env's recorded shape, so the declaration is the only place a check lives.
//
// A check has exactly one target: URL, an absolute http(s) address, or
// Target, the name of a platform-hosted frontend or workload whose hostname
// only the control plane knows (the platform allocates it). It carries no credential:
// the prober dials only public addresses, and a URL that names a private,
// loopback or cluster-internal host is refused here and again at dial time
// (pkg/netguard).
//
// Regions are not recorded: v1 probes from the platform's own cluster.
type ShapeUptimeCheck struct {
	// Name is unique within the env. It tags every metric the prober writes.
	Name string `json:"name"`
	// URL is the absolute address to request. Empty exactly when Target is set.
	URL string `json:"url,omitempty"`
	// Target is a hosted frontend's or workload's name: the control plane
	// resolves its platform hostname, and requests Path on it.
	Target string `json:"target,omitempty"`
	// Path is requested on Target's hostname ("/" when empty). Not valid with URL.
	Path string `json:"path,omitempty"`
	// Method is GET or HEAD.
	Method string `json:"method"`
	// IntervalSeconds and TimeoutSeconds schedule the check.
	IntervalSeconds int `json:"interval_seconds"`
	TimeoutSeconds  int `json:"timeout_seconds"`
	// ExpectStatus lists the statuses that mean up: "200", or a class
	// ("2xx"). Empty means [DefaultUptimeExpectStatus].
	ExpectStatus []string `json:"expect_status,omitempty"`
	// ExpectBody is a substring the response body must contain. Empty means
	// the body is not read. Needs Method GET.
	ExpectBody string `json:"expect_body,omitempty"`
	// Source is [UptimeSourceUser] or [UptimeSourceDefault].
	Source string `json:"source"`
}

// Interval is IntervalSeconds as a duration.
func (c ShapeUptimeCheck) Interval() time.Duration {
	return time.Duration(c.IntervalSeconds) * time.Second
}

// Timeout is TimeoutSeconds as a duration.
func (c ShapeUptimeCheck) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// StatusOK reports whether an HTTP status counts as up for this check.
func (c ShapeUptimeCheck) StatusOK(status int) bool {
	want := c.ExpectStatus
	if len(want) == 0 {
		want = DefaultUptimeExpectStatus
	}
	for _, w := range want {
		if strings.HasSuffix(w, "xx") {
			if status/100 == int(w[0]-'0') {
				return true
			}
			continue
		}
		if fmt.Sprint(status) == w {
			return true
		}
	}
	return false
}

func (c ShapeUptimeCheck) validate() error {
	if !uptimeNameRE.MatchString(c.Name) {
		return fmt.Errorf("%w: uptime check name %q (lowercase letters, digits, '-' and '_', starting with a letter, at most 63 characters)", ErrInvalid, c.Name)
	}
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: uptime check %q: %s", ErrInvalid, c.Name, fmt.Sprintf(format, args...))
	}
	switch {
	case c.URL == "" && c.Target == "":
		return bad("names no target: set url or target")
	case c.URL != "" && c.Target != "":
		return bad("has both url and target: a check has one target")
	case c.URL != "" && c.Path != "":
		return bad("path is for a target; a url carries its own path")
	case c.Target != "" && !targetNameRE.MatchString(c.Target):
		return bad("target %q is not a workload or frontend name", c.Target)
	case c.Path != "" && !strings.HasPrefix(c.Path, "/"):
		return bad("path %q must start with '/'", c.Path)
	}
	if c.URL != "" {
		if err := ValidateUptimeURL(c.URL); err != nil {
			return bad("%v", err)
		}
	}
	switch c.Method {
	case "GET", "HEAD":
	default:
		return bad("method %q (expected GET or HEAD)", c.Method)
	}
	if iv := c.Interval(); iv < MinUptimeInterval || iv > MaxUptimeInterval {
		return bad("interval %s is outside %s..%s", iv, MinUptimeInterval, MaxUptimeInterval)
	}
	if to := c.Timeout(); to < MinUptimeTimeout || to > MaxUptimeTimeout || to >= c.Interval() {
		return bad("timeout %s must be %s..%s and shorter than the interval", to, MinUptimeTimeout, MaxUptimeTimeout)
	}
	for _, s := range c.ExpectStatus {
		if !uptimeStatusRE.MatchString(s) {
			return bad("expect_status %q (a status like 200, or a class like 2xx)", s)
		}
	}
	if len(c.ExpectBody) > MaxUptimeExpectBody {
		return bad("expect_body is longer than %d bytes", MaxUptimeExpectBody)
	}
	if c.ExpectBody != "" && c.Method != "GET" {
		return bad("expect_body needs method GET: a HEAD response has no body")
	}
	switch c.Source {
	case UptimeSourceUser, UptimeSourceDefault:
	default:
		return bad("source %q (expected %s or %s)", c.Source, UptimeSourceUser, UptimeSourceDefault)
	}
	return nil
}

// ValidateUptimeURL checks a check's url: absolute http(s), a host, no
// credential, and a host that is not obviously private. The last is the same
// predicate the prober's dialer enforces (pkg/netguard), applied early so the
// author hears about a bad address when it is declared; it resolves no name,
// so the dial-time guard remains the boundary.
func ValidateUptimeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q: the scheme must be http or https", raw)
	}
	if u.User != nil {
		return fmt.Errorf("url %q carries a credential: a check sends none", raw)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("url %q has no host", raw)
	}
	if reason, refused := netguard.RefusedHost(u.Hostname()); refused {
		return fmt.Errorf("url %q: %s. A check probes public endpoints only", raw, reason)
	}
	return nil
}

// canonicalUptimeChecks is the checks sorted by name, with each check's
// statuses spelled the way the default is: a producer that lists the default
// statuses explicitly hashes the same as one that leaves them out.
func canonicalUptimeChecks(in []ShapeUptimeCheck) []ShapeUptimeCheck {
	if len(in) == 0 {
		return nil
	}
	out := make([]ShapeUptimeCheck, len(in))
	for i, c := range in {
		c.ExpectStatus = sortedStrings(c.ExpectStatus)
		if len(c.ExpectStatus) == len(DefaultUptimeExpectStatus) && c.ExpectStatus[0] == DefaultUptimeExpectStatus[0] && c.ExpectStatus[1] == DefaultUptimeExpectStatus[1] {
			c.ExpectStatus = nil
		}
		if c.Path == "/" {
			c.Path = ""
		}
		out[i] = c
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func validateUptimeChecks(checks []ShapeUptimeCheck) error {
	seen := map[string]bool{}
	for _, c := range checks {
		if seen[c.Name] {
			return fmt.Errorf("%w: shape names uptime check %q twice", ErrInvalid, c.Name)
		}
		seen[c.Name] = true
		if err := c.validate(); err != nil {
			return err
		}
	}
	return nil
}

// The metrics the uptime prober writes into the env's telemetry, and the
// attributes they carry. The prober (control plane), the UptimeCheckFailing and
// CertificateExpiring alert defaults (internal/obsdash) and any dashboard read
// these names, so they are declared once, here.
//
// All three are gauges. They carry the resource attribute
// deployment.environment.name (the env's name, which every other rule scopes
// by) and the metric attribute [UptimeAttrCheck].
const (
	// UptimeMetricUp is 1 when the last probe passed (the status and the body
	// were as expected), 0 when it failed. A failed probe also carries
	// [UptimeAttrError]; one that got a response carries
	// [UptimeAttrStatus].
	UptimeMetricUp = "uptime.check.up"
	// UptimeMetricDuration is the last probe's wall time in seconds, from
	// request to the end of the response (or to the failure).
	UptimeMetricDuration = "uptime.check.duration"
	// UptimeMetricCertExpiry is the seconds until the certificate the endpoint
	// presented expires (negative once it has). Written for https checks
	// whenever a certificate was seen, including one that failed verification.
	UptimeMetricCertExpiry = "tls.cert.expiry_seconds"

	// UptimeAttrCheck is the check's name.
	UptimeAttrCheck = "check"
	// UptimeAttrStatus is the HTTP status of the last response, on
	// [UptimeMetricUp].
	UptimeAttrStatus = "http.response.status_code"
	// UptimeAttrError names how a probe failed when it got no acceptable
	// response: timeout, dns, connect, tls, refused (a destination the SSRF
	// guard will not dial), status or body.
	UptimeAttrError = "error.type"
)
