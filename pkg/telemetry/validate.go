package telemetry

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// Options is what Validate needs to know about the env beyond its targets.
type Options struct {
	// Env names the env in messages.
	Env string
	// Clusters are the kubectl contexts the env declares. A RuntimeCluster
	// target must name one of them. Nil skips that check (a caller with no
	// cluster inventory, such as a unit test).
	Clusters []string
}

// Problem is one thing wrong with one target.
type Problem struct {
	// Source and ServiceName identify the target (Target.Source,
	// Target.ServiceName).
	Source      string
	ServiceName string
	// Field is the declaration path the problem is about, e.g.
	// "metrics[0].allow".
	Field   string
	Message string
	// Warning marks an informational note (Warnings), never a reason to
	// refuse the env. Validate reports none.
	Warning bool
}

func (p Problem) Error() string {
	who := p.Source
	if p.Warning {
		who = "note: " + who
	}
	if p.ServiceName != "" {
		who += " (service " + p.ServiceName + ")"
	}
	if p.Field != "" {
		return fmt.Sprintf("telemetry %s: %s: %s", who, p.Field, p.Message)
	}
	return fmt.Sprintf("telemetry %s: %s", who, p.Message)
}

// ValidationError is every Problem Validate found, in target order.
type ValidationError struct {
	Env      string
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	if e.Env != "" {
		fmt.Fprintf(&b, "env %q: ", e.Env)
	}
	fmt.Fprintf(&b, "%d telemetry problem(s):", len(e.Problems))
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p.Error())
	}
	return b.String()
}

// Validate checks normalized targets for what the KCL schema cannot: RE2
// syntax, durations, a service_name used twice, a scrape or check name used
// twice or made only of digits (which an unnamed one's index could be), an
// unresolved port name, the
// attachment rules (a Manifests or Observability target names a selector or
// an address; a selector needs a cluster), a cluster the env does not
// declare, and the hosted rules (every scrape sets `allow`; no address, no
// bearer token, no profiles; only declared ports), and every declared
// dashboard against dashspec.ParseDashboard, its id unique in the env. It returns a
// *ValidationError naming every problem, or nil.
//
// The KCL checks are repeated where they are cheap, so a Target built
// outside the render (a KCL plugin caller, a test) is held to the same rules.
func Validate(targets []Target, opts Options) error {
	v := &validator{opts: opts, dashboardIDs: map[string]string{}}
	seen := map[string]string{}
	for _, t := range targets {
		v.t = t
		if t.ServiceName == "" {
			v.add("service_name", "is required")
		} else if prev, dup := seen[t.ServiceName]; dup {
			v.add("service_name", fmt.Sprintf("%q is also the service of %s: a service_name is unique within an env, because every signal is stamped with it", t.ServiceName, prev))
		} else {
			seen[t.ServiceName] = t.Source
		}
		v.target()
	}
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Env: opts.Env, Problems: v.problems}
}

// Warnings returns informational notes about targets that are valid but may
// not collect what they declare. Today there is one: a Manifests or
// Observability target that pulls (metrics, profiles or checks) with
// NetworkPolicy unset gets no NetworkPolicy from forge, so the collector
// reaches its pods only if their existing policies admit it.
func Warnings(targets []Target) []Problem {
	var out []Problem
	for _, t := range targets {
		if t.NetworkPolicy != nil || !t.Pulled() || (t.Attachment != AttachManifests && t.Attachment != AttachObservability) {
			continue
		}
		out = append(out, Problem{
			Source: t.Source, ServiceName: t.ServiceName, Field: "network_policy", Warning: true,
			Message: "unset, so forge renders no NetworkPolicy for these pods (it cannot know whether they are already policy-selected, and a new policy would deny their other ingress): " +
				"if a policy selects them, admit the collector on the scrape, pprof and check ports yourself, or set network_policy = True",
		})
	}
	return out
}

type validator struct {
	opts     Options
	t        Target
	problems []Problem
	// dashboardIDs is every declared dashboard id seen so far in the env,
	// with the target that declared it.
	dashboardIDs map[string]string
}

func (v *validator) add(field, msg string) {
	v.problems = append(v.problems, Problem{Source: v.t.Source, ServiceName: v.t.ServiceName, Field: field, Message: msg})
}

func (v *validator) target() {
	t := v.t
	switch t.Scope {
	case ScopeProject, ScopePlatform:
	default:
		v.add("scope", fmt.Sprintf("%q must be project or platform", t.Scope))
	}
	switch t.Attachment {
	case AttachWorkload, AttachHostInfra, AttachManifests, AttachObservability, AttachManagedDatabase:
	default:
		v.add("attachment", fmt.Sprintf("%q is not one of workload, hostinfra, manifests, observability, manageddatabase", t.Attachment))
	}
	v.runtime()
	if len(t.Selector) > 0 && t.Address != "" {
		v.add("selector", "selector and address are exclusive")
	}
	if (t.Attachment == AttachManifests || t.Attachment == AttachObservability) && len(t.Selector) == 0 && t.Address == "" {
		v.add("selector", "a "+string(t.Attachment)+" target needs a selector (or an address): forge cannot see its pods")
	}
	for k, val := range t.Selector {
		if k == "" || val == "" {
			v.add("selector", "keys and values must be non-empty")
			break
		}
	}
	if t.Attachment == AttachWorkload && t.Runtime == RuntimeCluster && len(t.Namespaces) == 0 {
		v.add("namespaces", "a workload target on a cluster needs its namespace: without one its selector matches same-named pods in every namespace, and their signals would be stamped as this service")
	}
	if t.Pulled() && t.Runtime != RuntimeHosted && len(t.Selector) == 0 && t.Address == "" {
		v.add("selector", "a scrape, pull or check needs pods (a selector) or an address to reach")
	}

	scrapeNames := map[string]int{}
	for i, m := range t.Metrics {
		f := fmt.Sprintf("metrics[%d]", i)
		v.entryName(f, "scrape", m.Name, i, scrapeNames)
		v.port(f, m.Port, m.PortName)
		v.duration(f+".interval", m.Interval, 10*time.Second)
		v.duration(f+".timeout", m.Timeout, 0)
		if iv, err := time.ParseDuration(m.Interval); err == nil {
			if to, err := time.ParseDuration(m.Timeout); err == nil && to > iv {
				v.add(f+".timeout", "must not exceed interval")
			}
		}
		v.re2(f+".allow", m.Allow, true)
		v.re2(f+".deny", m.Deny, true)
		v.re2(f+".counters", m.Counters, true)
		if !strings.HasPrefix(m.Path, "/") {
			v.add(f+".path", "must start with /")
		}
		v.scheme(f, m.Scheme, m.BearerToken, m.TLS)
		if m.SampleLimit < 1 || m.SampleLimit > 200000 {
			v.add(f+".sample_limit", "must be 1-200000")
		}
		if t.Runtime == RuntimeHosted {
			if m.Allow == "" {
				v.add(f+".allow", "is required on a hosted env: a scrape without an allowlist stores every series the exporter emits")
			}
			if m.BearerToken != nil {
				v.add(f+".bearer_token", "is not supported on a hosted env yet (the platform reads no tenant scrape credentials)")
			}
		}
	}
	for i, p := range t.Profiles {
		f := fmt.Sprintf("profiles[%d]", i)
		v.port(f, p.Port, p.PortName)
		v.duration(f+".interval", p.Interval, time.Minute)
		for _, ty := range p.Types {
			if !slices.Contains(PprofTypes, ty) {
				v.add(f+".types", fmt.Sprintf("%q is not one of %v", ty, PprofTypes))
			}
		}
		if p.CPUSeconds < 1 || p.CPUSeconds > 30 {
			v.add(f+".cpu_seconds", "must be 1-30")
		}
		v.scheme(f, p.Scheme, p.BearerToken, p.TLS)
	}
	if t.Runtime == RuntimeHosted && len(t.Profiles) > 0 {
		v.add("profiles", "are not supported on a hosted env yet (the platform pulls no tenant profiles); forge-built workloads push theirs")
	}
	checkNames := map[string]int{}
	for i, c := range t.Checks {
		f := fmt.Sprintf("checks[%d]", i)
		v.entryName(f, "check", c.Name, i, checkNames)
		v.port(f, c.Port, c.PortName)
		v.duration(f+".interval", c.Interval, 10*time.Second)
		if c.Kind != "tcp" && c.Kind != "http" {
			v.add(f+".kind", fmt.Sprintf("%q must be tcp or http", c.Kind))
		}
	}
	v.otlp()
	v.logs()
	v.dashboards()
}

// dashboards runs each declared dashboard through dashspec.ParseDashboard and
// refuses an id another target of the env already declares: every dashboard
// becomes one file named for its id.
func (v *validator) dashboards() {
	for i, raw := range v.t.Dashboards {
		f := fmt.Sprintf("dashboards[%d]", i)
		d, err := dashspec.ParseDashboard(f, raw)
		if err != nil {
			v.add(f, err.Error())
			continue
		}
		if prev, dup := v.dashboardIDs[d.ID]; dup {
			v.add(f+".id", fmt.Sprintf("%q is also a dashboard of %s: a dashboard id is unique within the project", d.ID, prev))
			continue
		}
		v.dashboardIDs[d.ID] = v.t.Source
	}
}

func (v *validator) runtime() {
	t := v.t
	switch t.Runtime {
	case RuntimeHost, RuntimeCompose:
		if len(t.Selector) > 0 {
			v.add("selector", "selects pods, which only a cluster has: name the cluster whose collector scrapes them, or use an address")
		}
	case RuntimeHosted:
	case RuntimeCluster:
		if t.Cluster == "" {
			v.add("cluster", "a cluster target needs the cluster whose collector reaches it (the env declares no cluster target to default it from)")
		} else if v.opts.Clusters != nil && !slices.Contains(v.opts.Clusters, t.Cluster) {
			v.add("cluster", fmt.Sprintf("%q is not a cluster this env declares (it declares: %s)", t.Cluster, describe(v.opts.Clusters)))
		}
	case RuntimeExternal:
		v.add("runtime", "the workload runs on forge.OnExternal, which your own tooling runs, so no forge collector reaches it: declare it in Observability.telemetry with its selector and cluster, or its address")
	case RuntimeBuildOnly:
		v.add("runtime", "the workload is forge.BuildOnly: it is built, never run, so there is nothing to observe")
	default:
		v.add("runtime", fmt.Sprintf("%q is not a runtime telemetry can be collected on", t.Runtime))
	}
	if t.Runtime == RuntimeHosted && t.Address != "" {
		v.add("address", "is not supported on a hosted env: the platform reaches only the workload's own pods")
	}
}

// port reports an unresolved name, an out-of-range number, and on hosted a
// port the workload does not declare.
func (v *validator) port(field string, port int, name string) {
	t := v.t
	switch {
	case port == 0 && name != "":
		v.add(field+".port", fmt.Sprintf("%q is not a port of %s (it declares: %s)", name, t.Source, describe(sortedKeys(t.Ports))))
		return
	case port < 1 || port > 65535:
		v.add(field+".port", fmt.Sprintf("%d must be 1-65535", port))
		return
	}
	if t.Runtime == RuntimeHosted {
		declared := false
		for _, p := range t.Ports {
			if p == port {
				declared = true
			}
		}
		if !declared {
			v.add(field+".port", fmt.Sprintf("%d is not a port the workload declares (declared: %s): a hosted env reaches only declared ports", port, describe(sortedKeys(t.Ports))))
		}
	}
}

var (
	rfc1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	allDigits    = regexp.MustCompile(`^[0-9]+$`)
)

// entryName checks the name of scrape or check i of the target. A name is
// an RFC-1123 label with at least one non-digit, unique among its kind: an
// unnamed entry is identified by its index (collector job and receiver
// names), so an all-digit name would share that namespace and could collide
// with it. seen maps the names so far to their index.
func (v *validator) entryName(field, kind, name string, i int, seen map[string]int) {
	if name == "" {
		return
	}
	switch {
	case !rfc1123Label.MatchString(name):
		v.add(field+".name", fmt.Sprintf("%q must be an RFC-1123 label", name))
	case allDigits.MatchString(name):
		v.add(field+".name", fmt.Sprintf("%q is all digits, which are reserved for an unnamed %s's index: give it a letter", name, kind))
	}
	if prev, dup := seen[name]; dup {
		v.add(field+".name", fmt.Sprintf("%q is also the name of %s %d: names are unique within a target", name, kind, prev))
		return
	}
	seen[name] = i
}

func (v *validator) duration(field, d string, floor time.Duration) {
	got, err := time.ParseDuration(d)
	if err != nil || got <= 0 {
		v.add(field, fmt.Sprintf("%q is not a positive duration (e.g. 30s, 1m)", d))
		return
	}
	if floor > 0 && got < floor {
		v.add(field, fmt.Sprintf("%q is below the %s floor", d, floor))
	}
}

// re2 reports a pattern that does not compile under RE2 (Go's regexp), as
// the collector will anchor it when anchored is set.
func (v *validator) re2(field, pattern string, anchored bool) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	p := pattern
	if anchored {
		p = "^(?:" + pattern + ")$"
	}
	re, err := regexp.Compile(p)
	if err != nil {
		v.add(field, fmt.Sprintf("%q is not an RE2 expression: %v", pattern, err))
		return nil
	}
	return re
}

func (v *validator) scheme(field, scheme string, bearer *SecretMount, tls *TLS) {
	if scheme != "http" && scheme != "https" {
		v.add(field+".scheme", fmt.Sprintf("%q must be http or https", scheme))
	}
	if bearer != nil && scheme != "https" {
		v.add(field+".bearer_token", "is sent only over https")
	}
	if tls != nil && scheme != "https" {
		v.add(field+".tls", "needs scheme https")
	}
	for _, m := range []struct {
		name  string
		mount *SecretMount
	}{{"bearer_token", bearer}, {"tls.ca", caOf(tls)}} {
		if m.mount != nil && (!safeSecretComponent(m.mount.Secret) || !safeSecretComponent(m.mount.Key)) {
			v.add(field+"."+m.name, "needs a secret name and key that are safe single path components (letters, digits, '.', '_' or '-', not '.' or '..')")
		}
	}
	if tls != nil && tls.InsecureSkipVerify && tls.CA != nil {
		v.add(field+".tls", "ca and insecure_skip_verify are exclusive")
	}
}

// safeSecretComponent uses the Secret data-key alphabet, excluding the two
// directory components. Reject both Unix and Windows separators and volumes.
func safeSecretComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func caOf(t *TLS) *SecretMount {
	if t == nil {
		return nil
	}
	return t.CA
}

func (v *validator) otlp() {
	o := v.t.OTLP
	if o == nil {
		return
	}
	for _, s := range o.Signals {
		if s != SignalTraces && s != SignalMetrics && s != SignalLogs {
			v.add("otlp.signals", fmt.Sprintf("%q must be traces, metrics or logs", s))
		}
	}
	if o.Protocol != "grpc" && o.Protocol != "http/protobuf" {
		v.add("otlp.protocol", fmt.Sprintf("%q must be grpc or http/protobuf", o.Protocol))
	}
	if o.SamplerRatio != nil && (*o.SamplerRatio <= 0 || *o.SamplerRatio > 1) {
		v.add("otlp.sampler_ratio", "must be in (0, 1]")
	}
	envKey := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for _, k := range sortedKeys(o.Env) {
		if !envKey.MatchString(k) {
			v.add("otlp.env", fmt.Sprintf("%q is not an env var name", k))
		}
		if ref := o.Env[k].Ref; ref != "" && !slices.Contains(OTLPRefKeys, ref) {
			v.add("otlp.env."+k, fmt.Sprintf("ref %q is not one of %v", ref, OTLPRefKeys))
		}
	}
}

func (v *validator) logs() {
	l := v.t.Logs
	if l == nil {
		return
	}
	switch l.Format {
	case "auto", "json", "logfmt", "regex", "text":
	default:
		v.add("logs.format", fmt.Sprintf("%q must be auto, json, logfmt, regex or text", l.Format))
	}
	if (l.Format == "regex") != (l.Regex != "") {
		v.add("logs.regex", "is set exactly when format is regex")
	}
	if re := v.re2("logs.regex", l.Regex, false); re != nil {
		named := 0
		for _, n := range re.SubexpNames() {
			if n != "" {
				named++
			}
		}
		if named == 0 {
			v.add("logs.regex", "has no named group ((?P<name>...)): each named group becomes an attribute, so an expression without one parses nothing")
		}
	}
	if l.Severity != nil {
		for _, sev := range sortedKeys(l.Severity.Mapping) {
			if !slices.Contains(Severities, sev) {
				v.add("logs.severity.mapping", fmt.Sprintf("%q is not one of %v", sev, Severities))
			}
		}
	}
	for i, r := range l.Rules {
		f := fmt.Sprintf("logs.rules[%d]", i)
		v.re2(f+".body_match", r.BodyMatch, false)
		v.re2(f+".value_match", r.ValueMatch, false)
		if !slices.Contains(Severities, r.Severity) {
			v.add(f+".severity", fmt.Sprintf("%q is not one of %v", r.Severity, Severities))
		}
		if r.FromSeverity != "" && !slices.Contains(Severities, r.FromSeverity) {
			v.add(f+".from_severity", fmt.Sprintf("%q is not one of %v", r.FromSeverity, Severities))
		}
		if r.BodyMatch == "" && (r.Field == "" || r.ValueMatch == "") {
			v.add(f, "needs body_match, or field + value_match")
		}
	}
}

func describe(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
