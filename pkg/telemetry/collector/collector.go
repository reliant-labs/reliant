// Package collector translates declared telemetry ([]telemetry.Target) into
// OpenTelemetry Collector configuration: the receivers, processors,
// extensions, exporters and pipelines that collect what the targets declare,
// for the pinned otelcol-contrib (0.162.0, kcl/base.k OTEL_COLLECTOR_IMAGE).
//
// It is the one translator. forge's cluster agent, the local telemetry agent,
// the hosted control plane and a project's own collector (through the KCL
// plugin) all render the same Targets through Build, so a signal collected in
// one place is collected the same way in every other. It is pure: no I/O, and
// the output is plain maps whose keys every YAML/JSON encoder sorts, so the
// same input always renders the same bytes.
//
// What Build returns, per signal:
//
//   - metrics: one `prometheus` receiver per target (Kubernetes pod discovery
//     by namespaces + selector on a cluster, scraping each running pod's IP on
//     the declared port whether or not the pod spec declares it; a static
//     address on a host), the allow/deny/drop_labels relabels and the sample and label
//     limits, credentials and CAs read from mounted files; checks are
//     `tcpcheck`/`httpcheck` receivers (through `receiver_creator` for pods,
//     one per pod, on the declared port whether or not the pod spec declares it).
//     Each target gets its own `metrics/telemetry_<service>` pipeline, whose
//     `transform/telemetry_target/<service>` stamps service.name (and the env's
//     resource attributes when the target's scope is project), converts the
//     declared `counters` gauges to cumulative sums, and moves a pod scrape's
//     namespace to the resource's k8s.namespace.name (leaving an exporter's
//     own `namespace` label as the exporter wrote it).
//   - profiles (alpha; only with Options.EnableProfiles): `pprof` receivers,
//     through `receiver_creator` + `k8s_observer` for pods, in a
//     `profiles/telemetry_<service>` pipeline.
//   - logs: nothing is a receiver, because logs are tailed by the caller's own
//     filelog. Fragment.Logs names the processors to splice into the caller's
//     logs pipeline (identity, then the per-target parse) and one stanza
//     operator that keeps the raw line for that parse.
//   - isolated targets: their pipelines export to Options.Isolated, an
//     exporter with a bounded in-memory queue that drops on overflow.
//
// NetworkPolicies and WorkloadIngressRule render the policy objects and rules
// that admit only the collector, on only the declared pull ports.
package collector

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// Names of what Build renders that a caller refers to.
const (
	// IdentityProcessor stamps service.name (and the env's resource
	// attributes) on the records of the pods or files a target names. The
	// caller runs it in its logs pipeline and its node-metrics pipeline, after
	// k8sattributes, and in every other pipeline that k8sattributes enriches
	// (it has trace, metric, log and profile statements), because it is what
	// deletes the selector labels K8sAttributesLabels extracts.
	IdentityProcessor = "transform/telemetry_identity"
	// LogsProcessor parses each target's lines as its LogParse declares. The
	// caller runs it in its logs pipeline, after IdentityProcessor.
	LogsProcessor = "transform/telemetry_logs"
	// ObserverExtension is the k8s_observer the pod-discovering
	// receiver_creators watch. The caller lists it in service.extensions
	// (Fragment.ServiceExtensions names it when it is rendered).
	ObserverExtension = "k8s_observer/telemetry"
	// OriginalAttribute is where Fragment.Logs.Preserve keeps a line's raw
	// text for LogsProcessor, which deletes it from every record.
	OriginalAttribute = "forge.telemetry.original"
	// LabelAttributePrefix prefixes the pod labels K8sAttributesLabels asks
	// k8sattributes to extract; IdentityProcessor deletes them after use, so
	// they are never stored.
	LabelAttributePrefix = "forge.telemetry.label."
	// NamespaceLabel is the target label a pod scrape stamps the pod's
	// namespace under, reserved so it never displaces an exporter's own
	// `namespace` label. The target's transform moves it to the resource
	// attribute k8s.namespace.name, so it is never stored.
	NamespaceLabel = "forge_k8s_namespace"
	// DefaultNodeNameEnv is the env var holding the node's name in a
	// per-node collector (the downward API's spec.nodeName).
	DefaultNodeNameEnv = "K8S_NODE_NAME"
	// DefaultIsolatedQueueSize and DefaultIsolatedRetryMaxElapsed bound the
	// isolated exporter: a few minutes of a small pipeline, then drop.
	DefaultIsolatedQueueSize       = 200
	DefaultIsolatedRetryMaxElapsed = "2m"
)

// Discovery is what a collector pulls from, which matters on a cluster:
// a per-node DaemonSet must reach only its node's pods, and something that
// every node could reach (an address) must be pulled by one collector only.
type Discovery string

const (
	// DiscoverAll pulls every pod and every address: a single collector per
	// cluster, or the local host agent. The default.
	DiscoverAll Discovery = ""
	// DiscoverNodePods pulls only the pods on this collector's node (a
	// DaemonSet): pod discovery filters on spec.nodeName from
	// Options.NodeNameEnv, and address targets are skipped (with a warning),
	// left to one DiscoverAddresses collector.
	DiscoverNodePods Discovery = "node-pods"
	// DiscoverAddresses pulls only address targets: the singleton beside a
	// DiscoverNodePods DaemonSet. It renders no logs processing (logs are
	// node-local).
	DiscoverAddresses Discovery = "addresses"
)

// Options is what Build needs to know about the collector it renders for.
type Options struct {
	// Runtime is where the collector runs: telemetry.RuntimeCluster or
	// telemetry.RuntimeHost (which also reaches RuntimeCompose targets, on
	// their published loopback ports). Build keeps the targets it reaches and
	// skips the rest, so a caller passes the env's whole list.
	Runtime telemetry.Runtime
	// Cluster keeps only the targets of this kubectl context, on
	// RuntimeCluster. Empty keeps every cluster target.
	Cluster string
	// Discovery is what this collector pulls from (see Discovery).
	Discovery Discovery
	// NodeNameEnv is the env var naming the node, for DiscoverNodePods.
	// Default DefaultNodeNameEnv.
	NodeNameEnv string
	// MountRoot is the directory credentials are mounted under: a target's
	// SecretMount is read from MountRoot/<SecretMount.Path> (on a cluster,
	// mount each Secret at MountRoot/<secret>). Required when a target
	// declares a bearer token or a CA.
	MountRoot string
	// ResourceAttrs are the env's identity (e.g. deployment.environment.name),
	// stamped on every signal of a scope=project target and never on a
	// scope=platform one.
	ResourceAttrs map[string]string
	// EnableProfiles renders the pprof pulls. Off, a target's profiles are
	// skipped with a warning: pprof and receiver_creator's profiles support
	// are alpha in the pinned collector, so a caller can keep them out. The
	// collector needs --feature-gates=service.profilesSupport for them.
	EnableProfiles bool
	// Processors run first in every pipeline Build renders (e.g.
	// memory_limiter). They must be defined by the caller.
	Processors []string
	// PostProcessors run last in every metrics pipeline (e.g. batch). They
	// must be defined by the caller. Profiles pipelines are never batched.
	PostProcessors []string
	// MetricsExporters receive every non-isolated target's metrics and
	// checks. Defined by the caller.
	MetricsExporters []string
	// ProfilesExporters receive every target's profiles. Defined by the
	// caller.
	ProfilesExporters []string
	// Isolated is the exporter isolated targets' metrics go to. Required
	// when a target is isolated.
	Isolated *IsolatedExporter
}

// IsolatedExporter is the exporter Build renders for isolated targets: the
// caller's exporter config with a bounded in-memory queue that drops on
// overflow, so monitoring the telemetry pipeline never wedges on it.
type IsolatedExporter struct {
	// Name is the exporter's component ID, e.g. "otlphttp/telemetry_isolated".
	Name string
	// Config is the exporter's config, minus sending_queue and
	// retry_on_failure, which Build sets.
	Config map[string]any
	// QueueSize is the queue's size in requests. Default
	// DefaultIsolatedQueueSize.
	QueueSize int
	// RetryMaxElapsed is how long a batch is retried before it is dropped.
	// Default DefaultIsolatedRetryMaxElapsed.
	RetryMaxElapsed string
}

// Fragment is the collector configuration Build renders. Extensions,
// Receivers, Processors, Exporters and Pipelines are whole components and
// pipelines, merged into a config as they are (MergeInto). Logs and
// NodeMetricsProcessors name processors (defined in Processors) the caller
// splices into pipelines it already owns.
type Fragment struct {
	Extensions map[string]any
	Receivers  map[string]any
	Processors map[string]any
	Exporters  map[string]any
	Pipelines  map[string]any
	// ServiceExtensions are the Extensions to list in service.extensions.
	ServiceExtensions []string
	// Logs is what the caller splices into its logs pipeline.
	Logs Logs
	// NodeMetricsProcessors are processors for the caller's node-metrics
	// pipeline (kubeletstats), after k8sattributes: they stamp a target's
	// service.name on its pods' resource metrics.
	NodeMetricsProcessors []string
	// K8sAttributesLabels are entries for k8sattributes' extract.labels: the
	// pod labels the targets' selectors name, which IdentityProcessor matches
	// on (and deletes). Empty when no cluster target has a selector.
	K8sAttributesLabels []any
	// Warnings name what Build skipped and why.
	Warnings []string
}

// Logs is the logs half of a Fragment.
type Logs struct {
	// Preserve is one stanza operator for the caller's filelog receiver: it
	// copies a candidate line's raw text to OriginalAttribute, so
	// LogsProcessor parses the line as written even after the generic stage
	// (forge's JSON parse) consumed it. Insert it where the record's body is
	// the line and its service is known: after the `container` operator on a
	// cluster, after the file-name-to-service.name move on a host. Nil when
	// no target parses logs.
	Preserve map[string]any
	// Processors are LogsProcessor's place in the caller's logs pipeline, in
	// order: IdentityProcessor, then LogsProcessor. Empty when there is
	// nothing to do.
	Processors []string
}

// Build renders the collector configuration for the targets this collector
// reaches. targets are normalized here (telemetry.Normalize is idempotent)
// and validated (telemetry.Validate); a target that is invalid, or an Options
// that lacks what a target needs (exporters, a mount root, an isolated
// exporter), is an error, never a partial config.
func Build(targets []telemetry.Target, opts Options) (Fragment, error) {
	if opts.Runtime != telemetry.RuntimeCluster && opts.Runtime != telemetry.RuntimeHost {
		return Fragment{}, fmt.Errorf("collector: Options.Runtime %q must be %q or %q", opts.Runtime, telemetry.RuntimeCluster, telemetry.RuntimeHost)
	}
	switch opts.Discovery {
	case DiscoverAll, DiscoverNodePods, DiscoverAddresses:
	default:
		return Fragment{}, fmt.Errorf("collector: Options.Discovery %q is not one of %q, %q, %q", opts.Discovery, DiscoverAll, DiscoverNodePods, DiscoverAddresses)
	}
	if opts.NodeNameEnv == "" {
		opts.NodeNameEnv = DefaultNodeNameEnv
	}
	targets = telemetry.Normalize(targets)
	if err := telemetry.Validate(targets, telemetry.Options{}); err != nil {
		return Fragment{}, fmt.Errorf("collector: %w", err)
	}

	b := &builder{opts: opts, f: Fragment{
		Extensions: map[string]any{},
		Receivers:  map[string]any{},
		Processors: map[string]any{},
		Exporters:  map[string]any{},
		Pipelines:  map[string]any{},
	}}
	var selected []telemetry.Target
	for _, t := range targets {
		if b.reaches(t) {
			selected = append(selected, t)
		}
	}
	for _, t := range selected {
		if err := b.target(t); err != nil {
			return Fragment{}, err
		}
	}
	if opts.Discovery != DiscoverAddresses {
		b.logs(selected)
	}
	if b.isolatedUsed {
		if err := b.isolatedExporter(); err != nil {
			return Fragment{}, err
		}
	}
	if b.observerUsed {
		b.f.Extensions[ObserverExtension] = b.observer()
		b.f.ServiceExtensions = append(b.f.ServiceExtensions, ObserverExtension)
	}
	for _, name := range sortedKeys(b.authExtensions) {
		b.f.Extensions[name] = b.authExtensions[name]
		b.f.ServiceExtensions = append(b.f.ServiceExtensions, name)
	}
	return b.f, nil
}

type builder struct {
	opts           Options
	f              Fragment
	isolatedUsed   bool
	observerUsed   bool
	authExtensions map[string]any
}

func (b *builder) warn(format string, args ...any) {
	b.f.Warnings = append(b.f.Warnings, fmt.Sprintf(format, args...))
}

// reaches reports whether this collector collects t.
func (b *builder) reaches(t telemetry.Target) bool {
	switch b.opts.Runtime {
	case telemetry.RuntimeHost:
		return t.Runtime == telemetry.RuntimeHost || t.Runtime == telemetry.RuntimeCompose
	case telemetry.RuntimeCluster:
		return t.Runtime == telemetry.RuntimeCluster && (b.opts.Cluster == "" || t.Cluster == b.opts.Cluster)
	}
	return false
}

// pods reports whether t's pulls discover pods (rather than an address).
func (b *builder) pods(t telemetry.Target) bool {
	return b.opts.Runtime == telemetry.RuntimeCluster && len(t.Selector) > 0
}

// pulls reports whether this collector performs t's pulls, by Discovery.
func (b *builder) pulls(t telemetry.Target) bool {
	if b.opts.Runtime != telemetry.RuntimeCluster {
		return true
	}
	switch b.opts.Discovery {
	case DiscoverNodePods:
		return len(t.Selector) > 0
	case DiscoverAddresses:
		return len(t.Selector) == 0
	}
	return true
}

// target renders t's pulled signals: its metrics and checks pipeline and its
// profiles pipeline.
func (b *builder) target(t telemetry.Target) error {
	if !t.Pulled() {
		return nil
	}
	if !b.pulls(t) {
		if b.opts.Discovery == DiscoverNodePods {
			b.warn("telemetry %s (service %s): address %s is pulled by the DiscoverAddresses collector, not this per-node one", t.Source, t.ServiceName, t.Address)
		}
		return nil
	}
	svc := t.ServiceName
	// Keep target IDs in a separate namespace from the shared processors,
	// including for services named identity or logs.
	transform := "transform/telemetry_target/" + svc
	var metricRecv []string
	if len(t.Metrics) > 0 {
		recv, err := b.prometheus(t)
		if err != nil {
			return err
		}
		name := "prometheus/telemetry_" + svc
		b.f.Receivers[name] = recv
		metricRecv = append(metricRecv, name)
	}
	metricRecv = append(metricRecv, b.checks(t)...)

	var profileRecv []string
	if len(t.Profiles) > 0 {
		if !b.opts.EnableProfiles {
			b.warn("telemetry %s (service %s): profiles skipped: Options.EnableProfiles is off", t.Source, svc)
		} else {
			recv, err := b.profiles(t)
			if err != nil {
				return err
			}
			profileRecv = recv
		}
	}
	if len(metricRecv) == 0 && len(profileRecv) == 0 {
		return nil
	}

	b.f.Processors[transform] = b.targetTransform(t, len(metricRecv) > 0, len(profileRecv) > 0)
	if len(metricRecv) > 0 {
		exporters := b.opts.MetricsExporters
		if t.Isolated {
			if b.opts.Isolated == nil {
				return fmt.Errorf("collector: telemetry %s (service %s) is isolated, and Options.Isolated names no exporter for it", t.Source, svc)
			}
			b.isolatedUsed = true
			exporters = []string{b.opts.Isolated.Name}
		}
		if len(exporters) == 0 {
			return errors.New("collector: Options.MetricsExporters is empty, and telemetry " + t.Source + " (service " + svc + ") pulls metrics")
		}
		b.f.Pipelines["metrics/telemetry_"+svc] = map[string]any{
			"receivers":  strs(metricRecv...),
			"processors": strs(concat(b.opts.Processors, []string{transform}, b.opts.PostProcessors)...),
			"exporters":  strs(exporters...),
		}
	}
	if len(profileRecv) > 0 {
		if len(b.opts.ProfilesExporters) == 0 {
			return errors.New("collector: Options.ProfilesExporters is empty, and telemetry " + t.Source + " (service " + svc + ") pulls profiles")
		}
		b.f.Pipelines["profiles/telemetry_"+svc] = map[string]any{
			"receivers":  strs(profileRecv...),
			"processors": strs(concat(b.opts.Processors, []string{transform})...),
			"exporters":  strs(b.opts.ProfilesExporters...),
		}
	}
	return nil
}

// identityStatements set t's service.name, and the env's resource
// attributes for a project-scoped target, in the resource context.
func (b *builder) identityStatements(t telemetry.Target) []string {
	out := []string{fmt.Sprintf(`set(attributes["service.name"], %s)`, ottlString(t.ServiceName))}
	if t.Scope == telemetry.ScopeProject {
		for _, k := range sortedKeys(b.opts.ResourceAttrs) {
			out = append(out, fmt.Sprintf(`set(attributes[%s], %s)`, ottlString(k), ottlString(b.opts.ResourceAttrs[k])))
		}
	}
	return out
}

// targetTransform is transform/telemetry_target/<service>: identity for every
// signal of t's own pipelines, and the counters conversion.
func (b *builder) targetTransform(t telemetry.Target, metrics, profiles bool) map[string]any {
	identity := map[string]any{"context": "resource", "statements": strs(lits(b.identityStatements(t))...)}
	out := map[string]any{"error_mode": "ignore"}
	if metrics {
		groups := []any{identity}
		var counters []string
		for _, m := range t.Metrics {
			if m.Counters != "" {
				counters = append(counters, "(?:"+m.Counters+")")
			}
		}
		if len(counters) > 0 {
			re := "^(?:" + strings.Join(counters, "|") + ")$"
			groups = append(groups, map[string]any{"context": "metric", "statements": strs(lit(
				`convert_gauge_to_sum("cumulative", true) where metric.type == METRIC_DATA_TYPE_GAUGE and IsMatch(metric.name, ` + ottlString(re) + `)`))})
		}
		if b.pods(t) && len(t.Metrics) > 0 {
			groups = append(groups, map[string]any{"context": "datapoint", "statements": strs(lits(namespaceStatements())...)})
		}
		out["metric_statements"] = groups
	}
	if profiles {
		out["profile_statements"] = []any{identity}
	}
	return out
}

func (b *builder) isolatedExporter() error {
	iso := b.opts.Isolated
	if iso.Name == "" {
		return errors.New("collector: Options.Isolated.Name is empty")
	}
	cfg := map[string]any{}
	for k, v := range iso.Config {
		cfg[k] = v
	}
	size := iso.QueueSize
	if size <= 0 {
		size = DefaultIsolatedQueueSize
	}
	retry := iso.RetryMaxElapsed
	if retry == "" {
		retry = DefaultIsolatedRetryMaxElapsed
	}
	cfg["sending_queue"] = map[string]any{"enabled": true, "queue_size": size, "num_consumers": 1, "block_on_overflow": false}
	cfg["retry_on_failure"] = map[string]any{"enabled": true, "initial_interval": "5s", "max_interval": "30s", "max_elapsed_time": retry}
	b.f.Exporters[iso.Name] = cfg
	return nil
}

func (b *builder) observer() map[string]any {
	o := map[string]any{"auth_type": "serviceAccount", "observe_pods": true, "observe_nodes": false, "observe_services": false}
	if b.opts.Discovery == DiscoverNodePods {
		o["node"] = "${env:" + b.opts.NodeNameEnv + "}"
	}
	return o
}

// mountFile is the file a SecretMount is read from.
func (b *builder) mountFile(t telemetry.Target, field string, m *telemetry.SecretMount) (string, error) {
	if b.opts.MountRoot == "" {
		return "", fmt.Errorf("collector: telemetry %s (service %s): %s is a mounted file, and Options.MountRoot names no directory to read it from", t.Source, t.ServiceName, field)
	}
	return lit(strings.TrimSuffix(b.opts.MountRoot, "/") + "/" + m.Path), nil
}

// MergeInto adds the fragment's whole components and pipelines to a
// collector config document (the map a collector YAML decodes to), and its
// ServiceExtensions to service.extensions. A component or pipeline name the
// document already has is an error: the fragment never overwrites the
// caller's own. Logs and NodeMetricsProcessors are not spliced: where they go
// in the caller's pipelines is the caller's decision.
func (f Fragment) MergeInto(cfg map[string]any) error {
	for _, sec := range []struct {
		key string
		m   map[string]any
	}{{"extensions", f.Extensions}, {"receivers", f.Receivers}, {"processors", f.Processors}, {"exporters", f.Exporters}} {
		if err := mergeSection(cfg, []string{sec.key}, sec.m); err != nil {
			return err
		}
	}
	if err := mergeSection(cfg, []string{"service", "pipelines"}, f.Pipelines); err != nil {
		return err
	}
	if len(f.ServiceExtensions) == 0 {
		return nil
	}
	service := section(cfg, []string{"service"})
	have := map[string]bool{}
	var list []any
	if cur, ok := service["extensions"].([]any); ok {
		list = cur
	} else if cur, ok := service["extensions"].([]string); ok {
		list = strs(cur...)
	}
	for _, e := range list {
		have[fmt.Sprint(e)] = true
	}
	for _, e := range f.ServiceExtensions {
		if !have[e] {
			list = append(list, e)
		}
	}
	service["extensions"] = list
	return nil
}

func section(cfg map[string]any, path []string) map[string]any {
	cur := cfg
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return cur
}

func mergeSection(cfg map[string]any, path []string, add map[string]any) error {
	if len(add) == 0 {
		return nil
	}
	dst := section(cfg, path)
	for _, k := range sortedKeys(add) {
		if _, dup := dst[k]; dup {
			return fmt.Errorf("collector: %s %q is already in the config", strings.Join(path, "."), k)
		}
		dst[k] = add[k]
	}
	return nil
}

func strs(v ...string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
