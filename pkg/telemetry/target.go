// Package telemetry is the normalized contract for declared telemetry: how a
// system forge did not build — an image-only workload, a raw manifest group, a
// host-run engine, a Helm chart — is observed. A project declares it in KCL as
// forge.Telemetry (kcl/telemetry.k); the render resolves every declaration
// against where it attaches into `output.telemetry`, and this package decodes
// that list into []Target.
//
// A Target is everything a collector needs and nothing it has to infer: the
// service.name every signal is stamped with, where the target runs (Runtime,
// Cluster, Namespaces, Selector or Address), and the four signals — Metrics
// (Prometheus pull), OTLP (push), Logs (typed parse rules), Profiles (pprof
// pull) — plus reachability Checks. Every consumer reads the same Targets:
// forge's cluster agent, the local stack, the hosted control plane, and a
// project's own collector through the KCL plugin. None of them sees KCL.
//
// The pipeline is
//
//	Decode(output.telemetry) -> Normalize -> Validate
//
// Decode is strict (an unknown key is an error, so the KCL render and this
// contract cannot drift silently). Normalize resolves named ports against the
// attached entity's Ports, applies every default, and turns secret
// references into mount descriptors; it never fails. Validate reports, all at
// once, what KCL cannot check: RE2 syntax, a service_name used twice in an
// env, an unresolved port name, a cluster the env does not declare, and the
// hosted rules.
//
// The JSON field names are the contract. They are stable: a field may be
// added, never renamed or repurposed.
package telemetry

import (
	"encoding/json"
	"sort"
)

// Attachment is where a declaration attaches, which decides what forge infers
// about its target.
type Attachment string

const (
	// AttachWorkload is Workload.telemetry: the target is that workload.
	AttachWorkload Attachment = "workload"
	// AttachHostInfra is HostInfra.telemetry: the target is that host-run engine.
	AttachHostInfra Attachment = "hostinfra"
	// AttachManifests is Manifests.telemetry: pods of a raw group, by selector.
	AttachManifests Attachment = "manifests"
	// AttachObservability is Observability.telemetry: anything else, by
	// selector and cluster, or by address.
	AttachObservability Attachment = "observability"
	// AttachManagedDatabase is a forge.ManagedDatabase's telemetry: its
	// declared one, or the default forge renders for it (kcl/
	// database_telemetry.k), whose target is the database's CloudNativePG
	// instances (pods `cnpg.io/cluster: <name>`). The default CloudNativePG
	// operator target of a cluster holding such databases attaches here too.
	// Like Manifests, forge renders no NetworkPolicy for it unless asked:
	// one selecting the instances would deny every other Postgres caller.
	AttachManagedDatabase Attachment = "manageddatabase"
)

// Origin is who declared a target.
type Origin string

const (
	// OriginDeclared is a target the project declared: a forge.Telemetry on
	// a Workload, HostInfra, Manifests group, Observability, or a
	// ManagedDatabase's own telemetry (an extended default included). It is
	// the zero value and is never written.
	OriginDeclared Origin = ""
	// OriginDefault is a target forge rendered on its own, with no line of
	// the project's: a forge.ManagedDatabase's default telemetry and the
	// CloudNativePG operator target that comes with it. It is collected
	// like any other; what changes is what forge SAYS about it: a person
	// deploying is not warned that a default goes uncollected where the
	// control plane does not collect hosted telemetry yet, because they
	// asked for nothing.
	OriginDefault Origin = "default"
)

// Runtime is where the target runs, and so which collector reaches it.
type Runtime string

const (
	// RuntimeHost is a host process (forge.OnHost, HostInfra, or an
	// Observability address with no cluster), reached on Address.
	RuntimeHost Runtime = "host"
	// RuntimeCompose is a docker-compose service, reached on its published
	// loopback ports.
	RuntimeCompose Runtime = "compose"
	// RuntimeCluster is a Kubernetes cluster forge or the project runs a
	// collector on.
	RuntimeCluster Runtime = "cluster"
	// RuntimeHosted is Reliant hosting (forge.OnHosted): the platform
	// collects, under stricter rules (see Validate).
	RuntimeHosted Runtime = "hosted"
	// RuntimeExternal is a workload the project's own tooling runs
	// (forge.OnExternal). Refused: forge reaches nothing it runs.
	RuntimeExternal Runtime = "external"
	// RuntimeBuildOnly is a workload that is built and never run. Refused.
	RuntimeBuildOnly Runtime = "build-only"
)

// Scope is what the data is.
type Scope string

const (
	// ScopeProject data is stamped with the env's identity.
	ScopeProject Scope = "project"
	// ScopePlatform data carries no env identity: a shared database, a bus,
	// the telemetry pipeline itself. It grants no access.
	ScopePlatform Scope = "platform"
)

// Signal names, as Target.Signals reports them.
const (
	SignalMetrics  = "metrics"
	SignalLogs     = "logs"
	SignalTraces   = "traces"
	SignalProfiles = "profiles"
	SignalChecks   = "checks"
)

// Target is one observed system, normalized: the contract every collector
// consumes.
type Target struct {
	// ServiceName is the service.name every signal of this system carries.
	// Unique within an env.
	ServiceName string `json:"service_name"`
	Scope       Scope  `json:"scope"`
	// Source names the declaration: "workload:<name>", "hostinfra:<name>",
	// "manifests:<name>" ("manifests" for an unnamed group),
	// "observability", "manageddatabase:<name>", or
	// "manageddatabase-operator:<cluster>" for a cluster's default
	// CloudNativePG operator target.
	Source     string     `json:"source"`
	Attachment Attachment `json:"attachment"`
	// Origin is OriginDefault for a target forge rendered with no
	// declaration of the project's; empty (OriginDeclared) otherwise.
	Origin  Origin  `json:"origin,omitempty"`
	Runtime Runtime `json:"runtime"`
	// Cluster is the kubectl context whose collector reaches the target, on
	// RuntimeCluster. Empty elsewhere.
	Cluster string `json:"cluster,omitempty"`
	// Namespaces bounds pod discovery. Empty with a Selector: every namespace.
	Namespaces []string `json:"namespaces"`
	// Selector is the pod labels that must all match, exactly. Exclusive
	// with Address.
	Selector map[string]string `json:"selector"`
	// Address is one fixed host (a Service DNS name, or 127.0.0.1 for a host
	// process). Exclusive with Selector.
	Address string `json:"address,omitempty"`
	// Ports are the attached entity's named ports (a Workload's, or a
	// HostInfra's server/monitor/ui/metrics). Empty for Manifests and
	// Observability, whose ports are numbers.
	Ports map[string]int `json:"ports"`
	// Container is the container whose logs Logs parses; empty means every
	// container of a matching pod.
	Container string `json:"container,omitempty"`
	// Isolated asks for its own pipeline with a bounded in-memory queue that
	// drops on overflow, never sharing back-pressure with workload telemetry.
	Isolated bool `json:"isolated"`
	// NetworkPolicy is whether a collector admits itself with a
	// NetworkPolicy on only the scrape, pprof and check ports. It is
	// tri-state because a NetworkPolicy that selects pods no policy selected
	// before flips them to default-deny for ALL other ingress, which would
	// black-hole a raw system's real traffic:
	//
	//   - nil ("auto") on AttachWorkload/AttachHostInfra: add the collector's
	//     ingress rule to the policy forge already renders for that workload
	//     (it exists either way, so nothing new is denied).
	//   - nil on AttachManifests/AttachObservability: render NO policy. forge
	//     cannot know whether those pods are already policy-selected; the
	//     project owns admission (Warnings notes that the collector needs it).
	//   - nil on AttachManagedDatabase: render NO policy either. A policy
	//     selecting the instances would deny every other Postgres caller.
	//   - true: render it. false: never.
	//
	// AdmitCollector resolves it.
	NetworkPolicy *bool           `json:"network_policy,omitempty"`
	Metrics       []MetricsScrape `json:"metrics"`
	OTLP          *OTLPPush       `json:"otlp,omitempty"`
	Logs          *LogParse       `json:"logs,omitempty"`
	Profiles      []PprofScrape   `json:"profiles"`
	Checks        []HealthCheck   `json:"checks"`
	// Dashboard is the signal-generic system dashboard's settings.
	Dashboard SystemDashboard `json:"dashboard"`
	// Dashboards are the system's own dashboards, each one forge.dashboards/v1
	// object exactly as a dashboard file carries it (pkg/dashspec). Validate
	// runs each through Options.ValidateDashboard (forge passes
	// dashspec.ParseDashboard, the validator a release cut runs); they are
	// kept raw so that validator, with its line and column, is the one judge
	// of them.
	Dashboards []json.RawMessage `json:"dashboards"`
	// DefaultAlerts arms <Service>TargetDown and <Service>CheckFailing.
	DefaultAlerts bool `json:"default_alerts"`
}

// MetricsScrape is one Prometheus-exposition endpoint the collector pulls.
type MetricsScrape struct {
	// Name distinguishes several scrapes of one target (a job-name suffix):
	// an RFC-1123 label with a non-digit, unique within the target. An
	// unnamed scrape's suffix is its index, which no name can equal.
	Name string `json:"name,omitempty"`
	// Port is the container (or host) port. After Normalize it is set for
	// every resolvable PortName; 0 with a PortName is a name the attached
	// entity does not declare, which Validate reports.
	Port int `json:"port"`
	// PortName is the declared port name the scrape named, if it named one.
	PortName string              `json:"port_name,omitempty"`
	Path     string              `json:"path"`
	Params   map[string][]string `json:"params"`
	Scheme   string              `json:"scheme"`
	// Interval and Timeout are Go durations (ms, s, m, h).
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
	// Allow is an RE2, matched anchored against exposition names: the names
	// kept, plus `up`. Empty keeps every name.
	Allow string `json:"allow,omitempty"`
	// Deny is an RE2, anchored: names dropped after Allow.
	Deny string `json:"deny,omitempty"`
	// Counters is an RE2, anchored: gauges converted to cumulative sums.
	Counters    string   `json:"counters,omitempty"`
	DropLabels  []string `json:"drop_labels"`
	SampleLimit int      `json:"sample_limit"`
	// Nil uses the default; zero explicitly disables the Prometheus limit.
	LabelLimit            *int `json:"label_limit"`
	LabelValueLengthLimit *int `json:"label_value_length_limit"`
	// BearerToken is sent as Authorization: Bearer, read from a mounted file.
	BearerToken *SecretMount `json:"bearer_token,omitempty"`
	TLS         *TLS         `json:"tls,omitempty"`
}

// TLS configures an https scrape or pull.
type TLS struct {
	InsecureSkipVerify bool         `json:"insecure_skip_verify"`
	CA                 *SecretMount `json:"ca,omitempty"`
	ServerName         string       `json:"server_name,omitempty"`
}

// SecretMount is a credential a collector reads from a file, never its value.
// On a cluster, Secret is a Secret in the COLLECTOR's namespace; StoreKey
// names the env secret store's value it is rendered from, when it is one.
type SecretMount struct {
	Secret   string `json:"secret"`
	Key      string `json:"key"`
	StoreKey string `json:"store_key,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	// Path is where the consumer mounts it, relative to its secrets
	// directory: "<secret>/<key>" (a Secret volume mounted at
	// <dir>/<secret> projects each key as a file).
	Path string `json:"path"`
}

// OTLPPush is a system that pushes OTLP itself.
type OTLPPush struct {
	// Signals is what it pushes: traces, metrics, logs.
	Signals []string `json:"signals"`
	// Protocol is "grpc" or "http/protobuf".
	Protocol     string   `json:"protocol"`
	SamplerRatio *float64 `json:"sampler_ratio,omitempty"`
	// Env is the system's own env keys, each a literal or an OTLP fact.
	Env map[string]OTLPEnvValue `json:"env"`
}

// OTLPEnvValue is one OTLPPush env value: a literal (Ref empty), or the OTLP
// fact Ref names (one of OTLPRefKeys) with the Value it resolved to on this
// target's runtime. Value is empty when the runtime resolves it itself
// (hosted) or has none; a consumer then fills it or leaves the key out.
type OTLPEnvValue struct {
	Ref   string `json:"ref,omitempty"`
	Value string `json:"value,omitempty"`
}

// LogParse is how this system's log lines become records.
type LogParse struct {
	// Format is "auto", "json", "logfmt", "regex" or "text".
	Format string `json:"format"`
	// Regex is an RE2 with named groups (Format "regex"): each group becomes
	// an attribute.
	Regex     string        `json:"regex,omitempty"`
	Timestamp *LogTimestamp `json:"timestamp,omitempty"`
	Severity  *LogSeverity  `json:"severity,omitempty"`
	// Body, TraceID and SpanID are field paths into the parsed record.
	Body    string `json:"body,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`
	// Drop is attribute paths removed after parsing.
	Drop []string `json:"drop"`
	// Rules set the severity, in order; the first match wins.
	Rules []LogSeverityRule `json:"rules"`
}

// LogTimestamp names the field holding a record's time.
type LogTimestamp struct {
	Field string `json:"field"`
	// Layout is a strptime layout, or "rfc3339", "epoch_s", "epoch_ms",
	// "epoch_ns".
	Layout string `json:"layout"`
}

// LogSeverity names the field holding the level, and maps its values to
// severities (members of Severities).
type LogSeverity struct {
	Field string `json:"field"`
	// Mapping is severity -> the field values meaning it.
	Mapping map[string][]string `json:"mapping"`
}

// LogSeverityRule sets Severity on a record whose body matches BodyMatch, or
// whose Field matches ValueMatch (both RE2; both must hold when both are
// set), optionally only while it is at FromSeverity.
type LogSeverityRule struct {
	BodyMatch    string `json:"body_match,omitempty"`
	Field        string `json:"field,omitempty"`
	ValueMatch   string `json:"value_match,omitempty"`
	FromSeverity string `json:"from_severity,omitempty"`
	Severity     string `json:"severity"`
}

// PprofScrape pulls net/http/pprof profiles into the env's profiles store.
type PprofScrape struct {
	Port       int    `json:"port"`
	PortName   string `json:"port_name,omitempty"`
	PathPrefix string `json:"path_prefix"`
	// Types are PprofTypes members: cpu is a CPUSeconds capture every
	// Interval, the rest are snapshots.
	Types       []string     `json:"types"`
	Interval    string       `json:"interval"`
	CPUSeconds  int          `json:"cpu_seconds"`
	Scheme      string       `json:"scheme"`
	BearerToken *SecretMount `json:"bearer_token,omitempty"`
	TLS         *TLS         `json:"tls,omitempty"`
}

// HealthCheck is reachability when there is nothing to scrape.
type HealthCheck struct {
	// Name distinguishes several checks of one target (a receiver-name
	// suffix): an RFC-1123 label with a non-digit, unique within the
	// target. An unnamed check's suffix is its index, which no name can equal.
	Name string `json:"name,omitempty"`
	// Kind is "tcp" (connect) or "http" (a GET; 2xx is healthy).
	Kind     string `json:"kind"`
	Port     int    `json:"port"`
	PortName string `json:"port_name,omitempty"`
	Path     string `json:"path"`
	Scheme   string `json:"scheme"`
	Interval string `json:"interval"`
}

// SystemDashboard is the generated, signal-generic dashboard's settings.
type SystemDashboard struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title,omitempty"`
}

// Signals names the signals t declares, sorted: metrics, logs, traces (an
// OTLP push of traces; metrics and logs pushes count as those signals),
// profiles and checks.
func (t Target) Signals() []string {
	set := map[string]bool{}
	if len(t.Metrics) > 0 {
		set[SignalMetrics] = true
	}
	if t.Logs != nil {
		set[SignalLogs] = true
	}
	if t.OTLP != nil {
		for _, s := range t.OTLP.Signals {
			set[s] = true
		}
	}
	if len(t.Profiles) > 0 {
		set[SignalProfiles] = true
	}
	if len(t.Checks) > 0 {
		set[SignalChecks] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Pulled reports whether a collector must reach the target: it declares a
// scrape, a profile pull or a check (a push or a log parse needs none).
func (t Target) Pulled() bool {
	return len(t.Metrics) > 0 || len(t.Profiles) > 0 || len(t.Checks) > 0
}

// AdmitCollector resolves NetworkPolicy: whether a collector should admit
// itself into the target's pods with a NetworkPolicy rule. Auto (nil) is true
// only where forge already renders the pods' policy (a Workload or HostInfra
// target), and false for Manifests and Observability targets, whose pods
// forge does not own.
func (t Target) AdmitCollector() bool {
	if t.NetworkPolicy != nil {
		return *t.NetworkPolicy
	}
	return t.Attachment == AttachWorkload || t.Attachment == AttachHostInfra
}
