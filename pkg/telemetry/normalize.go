package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
)

// The vocabulary kcl/telemetry.k declares, held equal to it by
// TestKCLVocabularyMatches.
var (
	// Severities are the OpenTelemetry severities a log record can be given
	// (TELEMETRY_SEVERITIES).
	Severities = []string{"trace", "debug", "info", "warn", "error", "fatal"}
	// OTLPRefKeys are the OTLP facts an OTLPEnvValue.Ref names
	// (OTLP_REF_KEYS).
	OTLPRefKeys = []string{
		"endpoint", "traces_endpoint", "metrics_endpoint", "logs_endpoint",
		"grpc_address", "protocol", "insecure", "service_name",
		"resource_attributes", "traces_sampler", "traces_sampler_arg",
	}
	// PprofTypes are the profile types a PprofScrape can pull (PPROF_TYPES).
	PprofTypes = []string{"cpu", "heap", "allocs", "goroutine", "mutex", "block"}
)

// Defaults Normalize applies, the same as kcl/telemetry.k's.
const (
	DefaultMetricsPath           = "/metrics"
	DefaultScrapeInterval        = "30s"
	DefaultScrapeTimeout         = "10s"
	DefaultSampleLimit           = 5000
	DefaultLabelLimit            = 30
	DefaultLabelValueLengthLimit = 256
	DefaultPprofPathPrefix       = "/debug/pprof"
	DefaultPprofInterval         = "5m"
	DefaultPprofCPUSeconds       = 5
	DefaultCheckInterval         = "30s"
	DefaultOTLPProtocol          = "http/protobuf"
	DefaultLogFormat             = "auto"
	DefaultLogTimestampLayout    = "rfc3339"
)

// Decode reads a render's `output.telemetry` list. It is strict: a key this
// package does not know is an error, because a field the render emits and the
// contract drops is a signal nobody collects. Null or empty input is no
// targets. The result is not normalized; see Normalize.
func Decode(data []byte) ([]Target, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var out []Target
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode output.telemetry: %w", err)
	}
	return out, nil
}

// Normalize returns targets with every default applied, every port name the
// attached entity declares resolved to its number, every nil list or map made
// empty, and every SecretMount's Path set. It never fails and is idempotent:
// a port name with no match keeps Port 0 for Validate to report. The input is
// not modified.
func Normalize(targets []Target) []Target {
	out := make([]Target, len(targets))
	for i, t := range targets {
		out[i] = normalizeTarget(t)
	}
	return out
}

func normalizeTarget(t Target) Target {
	if t.Scope == "" {
		t.Scope = ScopeProject
	}
	t.Namespaces = cloneStrings(t.Namespaces)
	t.Selector = cloneMap(t.Selector)
	ports := make(map[string]int, len(t.Ports))
	for k, v := range t.Ports {
		ports[k] = v
	}
	t.Ports = ports
	if t.NetworkPolicy != nil {
		np := *t.NetworkPolicy
		t.NetworkPolicy = &np
	}

	metrics := make([]MetricsScrape, len(t.Metrics))
	for i, m := range t.Metrics {
		m.Port = resolvePort(m.Port, m.PortName, ports)
		m.Path = orDefault(m.Path, DefaultMetricsPath)
		m.Scheme = orDefault(m.Scheme, "http")
		m.Interval = orDefault(m.Interval, DefaultScrapeInterval)
		m.Timeout = orDefault(m.Timeout, DefaultScrapeTimeout)
		if m.SampleLimit == 0 {
			m.SampleLimit = DefaultSampleLimit
		}
		m.LabelLimit = normalizeLimit(m.LabelLimit, DefaultLabelLimit)
		m.LabelValueLengthLimit = normalizeLimit(m.LabelValueLengthLimit, DefaultLabelValueLengthLimit)
		params := make(map[string][]string, len(m.Params))
		for k, v := range m.Params {
			params[k] = cloneStrings(v)
		}
		m.Params = params
		m.DropLabels = cloneStrings(m.DropLabels)
		m.BearerToken = normalizeMount(m.BearerToken)
		m.TLS = normalizeTLS(m.TLS)
		metrics[i] = m
	}
	t.Metrics = metrics

	profiles := make([]PprofScrape, len(t.Profiles))
	for i, p := range t.Profiles {
		p.Port = resolvePort(p.Port, p.PortName, ports)
		p.PathPrefix = orDefault(p.PathPrefix, DefaultPprofPathPrefix)
		if len(p.Types) == 0 {
			p.Types = []string{"cpu", "heap"}
		} else {
			p.Types = cloneStrings(p.Types)
		}
		p.Interval = orDefault(p.Interval, DefaultPprofInterval)
		if p.CPUSeconds == 0 {
			p.CPUSeconds = DefaultPprofCPUSeconds
		}
		p.Scheme = orDefault(p.Scheme, "http")
		p.BearerToken = normalizeMount(p.BearerToken)
		p.TLS = normalizeTLS(p.TLS)
		profiles[i] = p
	}
	t.Profiles = profiles

	checks := make([]HealthCheck, len(t.Checks))
	for i, c := range t.Checks {
		c.Port = resolvePort(c.Port, c.PortName, ports)
		c.Kind = orDefault(c.Kind, "tcp")
		c.Path = orDefault(c.Path, "/")
		c.Scheme = orDefault(c.Scheme, "http")
		c.Interval = orDefault(c.Interval, DefaultCheckInterval)
		checks[i] = c
	}
	t.Checks = checks

	dashboards := make([]json.RawMessage, len(t.Dashboards))
	for i, d := range t.Dashboards {
		dashboards[i] = append(json.RawMessage{}, d...)
	}
	t.Dashboards = dashboards

	if t.OTLP != nil {
		o := *t.OTLP
		if len(o.Signals) == 0 {
			o.Signals = []string{SignalTraces}
		} else {
			o.Signals = cloneStrings(o.Signals)
		}
		o.Protocol = orDefault(o.Protocol, DefaultOTLPProtocol)
		if o.SamplerRatio != nil {
			r := *o.SamplerRatio
			o.SamplerRatio = &r
		}
		env := make(map[string]OTLPEnvValue, len(o.Env))
		for k, v := range o.Env {
			env[k] = v
		}
		o.Env = env
		t.OTLP = &o
	}

	if t.Logs != nil {
		l := *t.Logs
		l.Format = orDefault(l.Format, DefaultLogFormat)
		if l.Timestamp != nil {
			ts := *l.Timestamp
			ts.Layout = orDefault(ts.Layout, DefaultLogTimestampLayout)
			l.Timestamp = &ts
		}
		if l.Severity != nil {
			s := *l.Severity
			m := make(map[string][]string, len(s.Mapping))
			for k, v := range s.Mapping {
				m[k] = cloneStrings(v)
			}
			s.Mapping = m
			l.Severity = &s
		}
		l.Drop = cloneStrings(l.Drop)
		l.Rules = append([]LogSeverityRule{}, l.Rules...)
		t.Logs = &l
	}
	return t
}

// resolvePort is the numeric port: port when set, else the declared port
// named name, else 0.
func resolvePort(port int, name string, ports map[string]int) int {
	if port != 0 || name == "" {
		return port
	}
	return ports[name]
}

func normalizeMount(m *SecretMount) *SecretMount {
	if m == nil {
		return nil
	}
	c := *m
	// Invalid references remain diagnosable by Validate, but never acquire a
	// usable path outside the collector's secrets directory.
	c.Path = ""
	if safeSecretComponent(c.Secret) && safeSecretComponent(c.Key) {
		c.Path = path.Join(c.Secret, c.Key)
	}
	return &c
}

func normalizeLimit(value *int, fallback int) *int {
	if value != nil {
		fallback = *value
	}
	return &fallback
}

func normalizeTLS(t *TLS) *TLS {
	if t == nil {
		return nil
	}
	c := *t
	c.CA = normalizeMount(c.CA)
	return &c
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func cloneStrings(s []string) []string {
	return append([]string{}, s...)
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
