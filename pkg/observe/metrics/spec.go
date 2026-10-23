// Package metrics is forge's declared metrics registry: a typed layer over the
// OpenTelemetry metrics API in which every instrument is DECLARED before it is
// created.
//
// A declaration carries the facts a reader of the telemetry needs and a
// compiler cannot check on its own: the name, the instrument type, a UCUM unit,
// a description, the attribute keys (optionally with the closed set of values
// each may take) and how stable the metric is. The registry
//
//   - returns the standard OpenTelemetry instruments (metric.Int64Counter and
//     friends), so recording stays `c.Add(ctx, 1, metric.WithAttributes(...))`
//     and everything that already understands OTel — the Prometheus /metrics
//     endpoint, the OTLP exporter — is unchanged;
//   - enforces the declaration at record time: an attribute key the metric did
//     not declare, or a value outside an enumerated set, is never exported as
//     given. In development it panics, so the mistake is found where it was
//     made; in production it is corrected (the key dropped, the value replaced
//     by OtherValue) and counted on forge.metrics.rejected_attributes, so a
//     stray user id can never become a new time series per user;
//   - can dump the whole declared set as JSON (Schema), which is what
//     `<bin> metrics schema --json`, GET /debug/metrics/schema and
//     `forge obs metrics declared` print and what the custom-metrics dashboard is
//     generated from.
//
// What it does not do: observable (callback) instruments, and instruments that
// third-party libraries create on their own (the Go runtime metrics, the SQL
// pool metrics). Those are listed in the schema through Describe — a
// declaration without an instrument — so the schema is complete.
package metrics

import (
	"fmt"
	"regexp"
	"strings"
)

// Type is the instrument kind of a metric.
type Type string

// The instrument kinds. Values are the schema's spelling.
const (
	Counter       Type = "counter"
	UpDownCounter Type = "updowncounter"
	Histogram     Type = "histogram"
	Gauge         Type = "gauge"
)

// Stability says how far a consumer may rely on a metric.
type Stability string

// The stability levels.
const (
	// Stable metrics keep their name, unit and attribute keys.
	Stable Stability = "stable"
	// Experimental metrics may change; the default for a new declaration.
	Experimental Stability = "experimental"
	// Deprecated metrics are still emitted and will be removed.
	Deprecated Stability = "deprecated"
)

// OtherValue replaces a value outside an attribute's enumerated set, in
// production, so the series count stays bounded by the declaration.
const OtherValue = "_other"

// Attribute is one attribute key a metric may carry.
type Attribute struct {
	// Key is the attribute key, lower snake case with optional dots.
	Key string `json:"key"`
	// Values, when non-empty, is the closed set of string values the key may
	// take. Leave it empty for a key whose values are bounded by something
	// other than a list (a route, an rpc procedure).
	Values []string `json:"values,omitempty"`
}

// Spec declares one metric. The instrument constructors on Registry fill in
// Type; a Spec passed to Describe carries its own.
type Spec struct {
	// Name is the metric name: lower snake case, dot-separated segments
	// allowed (orders_created, rpc.server.calls).
	Name string `json:"name"`
	// Type is set by the constructor that creates the instrument; do not set
	// it yourself unless you call Describe.
	Type Type `json:"type"`
	// Unit is a UCUM unit: "s", "By", "{order}", "1" for a dimensionless ratio.
	Unit string `json:"unit"`
	// Description says what the metric measures, for a human.
	Description string `json:"description"`
	// Attributes are the only attribute keys the metric accepts.
	Attributes []Attribute `json:"attributes"`
	// Stability defaults to Experimental.
	Stability Stability `json:"stability"`
	// Since is the service version that introduced the metric (v1.4.0).
	Since string `json:"since"`

	// Buckets overrides the histogram's default bucket boundaries. It is not
	// part of the schema: boundaries are an aggregation choice, not an
	// identity.
	Buckets []float64 `json:"-"`
}

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	keyRe   = nameRe
	sinceRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){1,2}([-+][0-9A-Za-z.\-]+)?$`)
)

// maxNameLen is OpenTelemetry's instrument-name limit.
const maxNameLen = 255

// Validate reports why the declaration is not acceptable, or nil. It
// normalises nothing; Stability "" is accepted here and defaults to
// Experimental when the spec is registered.
func (s Spec) Validate() error {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	if s.Name == "" {
		bad("name is required")
	} else {
		if len(s.Name) > maxNameLen {
			bad("name is %d characters, over the %d limit", len(s.Name), maxNameLen)
		}
		if !nameRe.MatchString(s.Name) {
			bad("name %q must be lower snake case, dot-separated segments allowed (orders_created, rpc.server.calls)", s.Name)
		}
	}
	switch s.Type {
	case Counter, UpDownCounter, Histogram, Gauge:
	case "":
		bad("type is required")
	default:
		bad("type %q is not one of counter, updowncounter, histogram, gauge", s.Type)
	}
	if s.Unit == "" {
		bad(`unit is required: a UCUM unit such as "s", "By" or "{order}", or "1" for a dimensionless value`)
	} else if !validUCUM(s.Unit) {
		bad("unit %q is not a valid UCUM expression", s.Unit)
	}
	if strings.TrimSpace(s.Description) == "" {
		bad("description is required")
	}
	switch s.Stability {
	case "", Stable, Experimental, Deprecated:
	default:
		bad("stability %q is not one of stable, experimental, deprecated", s.Stability)
	}
	if s.Since == "" {
		bad("since is required: the service version that introduced the metric")
	} else if !sinceRe.MatchString(s.Since) {
		bad("since %q is not a version (v1.4.0)", s.Since)
	}

	seen := map[string]bool{}
	for _, a := range s.Attributes {
		if !keyRe.MatchString(a.Key) {
			bad("attribute key %q must be lower snake case, dot-separated segments allowed", a.Key)
			continue
		}
		if seen[a.Key] {
			bad("attribute key %q is declared twice", a.Key)
		}
		seen[a.Key] = true
		vals := map[string]bool{}
		for _, v := range a.Values {
			switch {
			case v == "":
				bad("attribute %q lists an empty value", a.Key)
			case v == OtherValue:
				bad("attribute %q lists %q, which is reserved for out-of-set values", a.Key, OtherValue)
			case vals[v]:
				bad("attribute %q lists value %q twice", a.Key, v)
			}
			vals[v] = true
		}
	}
	if len(s.Buckets) > 0 && s.Type != Histogram {
		bad("buckets apply to histograms only")
	}
	for i := 1; i < len(s.Buckets); i++ {
		if s.Buckets[i] <= s.Buckets[i-1] {
			bad("buckets must be strictly increasing")
			break
		}
	}

	if len(errs) == 0 {
		return nil
	}
	label := s.Name
	if label == "" {
		label = "(unnamed)"
	}
	return fmt.Errorf("metric %s: %s", label, strings.Join(errs, "; "))
}

// normalized returns s with defaults applied and slices copied, so a caller
// mutating its Spec later cannot change what was registered.
func (s Spec) normalized() Spec {
	out := s
	if out.Stability == "" {
		out.Stability = Experimental
	}
	out.Attributes = make([]Attribute, len(s.Attributes))
	for i, a := range s.Attributes {
		out.Attributes[i] = Attribute{Key: a.Key, Values: append([]string(nil), a.Values...)}
	}
	out.Buckets = append([]float64(nil), s.Buckets...)
	return out
}

// sameDeclaration reports whether a and b declare the same metric. Only the
// declared identity counts; buckets are an aggregation choice.
func sameDeclaration(a, b Spec) bool {
	if a.Name != b.Name || a.Type != b.Type || a.Unit != b.Unit ||
		a.Description != b.Description || a.Stability != b.Stability || a.Since != b.Since ||
		len(a.Attributes) != len(b.Attributes) {
		return false
	}
	for i := range a.Attributes {
		x, y := a.Attributes[i], b.Attributes[i]
		if x.Key != y.Key || len(x.Values) != len(y.Values) {
			return false
		}
		for j := range x.Values {
			if x.Values[j] != y.Values[j] {
				return false
			}
		}
	}
	return true
}

// validUCUM checks a UCUM unit expression: atoms (m, By, s, [in_i], %),
// integer factors ("1"), {annotations}, the . and / operators, parentheses and
// integer exponents (m2, s-1). Atoms are checked against the common subset of
// the UCUM table in ucum.go, with metric and binary prefixes, because the typos
// worth catching ("seconds", "bytes", "req/sec") are all syntactically
// plausible atoms. A count of a thing is spelled as an annotation: "{order}".
func validUCUM(u string) bool {
	p := &ucumParser{s: u}
	if !p.expr() {
		return false
	}
	return p.i == len(p.s)
}

type ucumParser struct {
	s string
	i int
}

func (p *ucumParser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

// expr := ['/'] term (('.'|'/') term)*
func (p *ucumParser) expr() bool {
	if p.peek() == '/' {
		p.i++
	}
	if !p.term() {
		return false
	}
	for p.peek() == '.' || p.peek() == '/' {
		p.i++
		if !p.term() {
			return false
		}
	}
	return true
}

// term := (annotation | '(' expr ')' | number | atom [exponent]) annotation?
func (p *ucumParser) term() bool {
	switch c := p.peek(); {
	case c == '{':
		return p.annotation()
	case c == '(':
		p.i++
		if !p.expr() || p.peek() != ')' {
			return false
		}
		p.i++
	case c >= '0' && c <= '9':
		for p.peek() >= '0' && p.peek() <= '9' {
			p.i++
		}
	default:
		if !p.atom() {
			return false
		}
		p.exponent()
	}
	if p.peek() == '{' {
		return p.annotation()
	}
	return true
}

func (p *ucumParser) atom() bool {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '%', c == '_':
			p.i++
		case c == '[':
			end := strings.IndexByte(p.s[p.i:], ']')
			if end < 2 {
				return false
			}
			p.i += end + 1
		default:
			return p.i > start && knownAtom(p.s[start:p.i])
		}
	}
	return p.i > start && knownAtom(p.s[start:p.i])
}

func (p *ucumParser) exponent() {
	j := p.i
	if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
		j++
	}
	k := j
	for k < len(p.s) && p.s[k] >= '0' && p.s[k] <= '9' {
		k++
	}
	if k > j {
		p.i = k
	}
}

func (p *ucumParser) annotation() bool {
	end := strings.IndexByte(p.s[p.i:], '}')
	if end < 2 {
		return false
	}
	body := p.s[p.i+1 : p.i+end]
	for i := 0; i < len(body); i++ {
		if body[i] < 0x21 || body[i] > 0x7e || body[i] == '{' {
			return false
		}
	}
	p.i += end + 1
	return true
}
