package deploy

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

// The observability collector, rendered two ways from ONE declaration:
//
//   - the NODE AGENT: a DaemonSet plus a node-local Service on a cluster.
//     Apps push OTLP to it; it also tails container stdout and reads kubelet
//     stats, enriches with Kubernetes metadata and exports to the env's
//     destination. RenderObservabilityAgent.
//   - the LOCAL FORWARDER: the same pipelines without the Kubernetes parts,
//     for `forge env up` when the env's destination is not the local
//     ClickStack. It tails the files `forge env up` already writes.
//     ForwarderConfig.
//
// Neither learns whose data a record is. k8sattributes ENRICHES (pod,
// namespace, node, workload labels); authentication is the destination's
// headers, read from a Secret. Attributing a record to an owner is the
// receiving platform's job, never this renderer's.
//
// This is NOT a workload and never passes through RenderWorkloads, so the
// Restricted profile's kind allowlist (RestrictedKinds) is untouched: the
// hosted platform renders its own agent and forge emits only the OTEL_*
// contract for hosted workloads.

// ClientIdentityKeyPattern matches the resource attributes a client must not
// be allowed to set on the node agent's OTLP input: Kubernetes identity
// (k8s.*), which the node supplies, and the hosting platform's own namespace
// (reliant.*). The node agent deletes them before k8sattributes runs.
// service.* and deployment.* are the app's own and are kept.
const ClientIdentityKeyPattern = `^(k8s|reliant)\..*`

// stripIdentityStatement is the OTTL form: a string literal, so the regex's
// backslash is doubled.
var stripIdentityStatement = `delete_matching_keys(resource.attributes, "` + strings.ReplaceAll(ClientIdentityKeyPattern, `\`, `\\`) + `")`

// ObservabilityAgentKind is the kind of the forge.dev/v1alpha1 record kcl/render.k
// emits, one per cluster that runs a forge workload.
const ObservabilityAgentKind = "ObservabilityAgent"

// Names of the objects the node agent renders.
const (
	ObservabilityAgentName = "otel-agent"
	// agentStorageDir is the host directory holding the filelog checkpoints
	// and the exporter's persistent queue, so a restart neither re-reads nor
	// drops what was in flight.
	agentStorageDir = "/var/lib/forge-otel-agent"
	// agentTokenDir is where the bearer-token Secret is mounted, as a whole
	// directory: a file mounted with subPath is never refreshed by the kubelet.
	agentTokenDir = "/etc/otel-auth"
	// agentTokenFile is the token's file name inside agentTokenDir.
	agentTokenFile = "token"
	// agentHeadersEnv is the container environment variable the collector
	// config reads the destination's headers from.
	agentHeadersEnv = "FORGE_OTLP_HEADERS"
	// ForwarderHeadersEnv is the same variable for the local forwarder.
	ForwarderHeadersEnv = agentHeadersEnv
)

// ObservabilitySecretKey names one key of a Secret.
type ObservabilitySecretKey struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// ObservabilitySignals says which signals the collector ships.
type ObservabilitySignals struct {
	Traces   bool `json:"traces"`
	Metrics  bool `json:"metrics"`
	Logs     bool `json:"logs"`
	Profiles bool `json:"profiles"`
}

// ObservabilityAgentSpec is the declaration both renderers take. It is the
// `spec` of the ObservabilityAgent record kcl/render.k emits.
type ObservabilityAgentSpec struct {
	// Endpoint is the destination's OTLP base URL; Protocol is
	// "http/protobuf" or "grpc".
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol"`
	// HeadersSecret holds the destination's request headers as a JSON object
	// of header name to value. Nil sends none.
	HeadersSecret *ObservabilitySecretKey `json:"headersSecret,omitempty"`
	// TokenSecret holds a bearer token as the key's whole value (no JSON, no
	// scheme). The node agent mounts it as a file and sends it as
	// "Authorization: Bearer <token>" through the collector's bearertokenauth
	// extension, which re-reads the file when it changes: a Secret that is
	// renewed in place (kubectl, a control plane) takes effect with no restart.
	// Mutually exclusive with HeadersSecret, which is read once at start.
	TokenSecret *ObservabilitySecretKey `json:"tokenSecret,omitempty"`
	Signals     ObservabilitySignals    `json:"signals"`
	// Image is the collector image; MemoryLimitMiB bounds the agent.
	Image          string `json:"image,omitempty"`
	MemoryLimitMiB int    `json:"memoryLimitMiB,omitempty"`
}

var (
	endpointRE = regexp.MustCompile(`^https?://[^/ ]+(:[0-9]+)?(/[^ ]*)?$`)
	imageRE    = regexp.MustCompile(`^[^\s]+$`)
)

func (s ObservabilityAgentSpec) validate() error {
	var errs []error
	if !endpointRE.MatchString(s.Endpoint) {
		errs = append(errs, fmt.Errorf("endpoint %q is not an absolute http(s) URL", s.Endpoint))
	}
	if s.Protocol != "http/protobuf" && s.Protocol != "grpc" {
		errs = append(errs, fmt.Errorf("protocol %q must be http/protobuf or grpc", s.Protocol))
	}
	if s.HeadersSecret != nil && (s.HeadersSecret.Name == "" || s.HeadersSecret.Key == "") {
		errs = append(errs, errors.New("headersSecret needs both name and key"))
	}
	if s.TokenSecret != nil && (s.TokenSecret.Name == "" || s.TokenSecret.Key == "") {
		errs = append(errs, errors.New("tokenSecret needs both name and key"))
	}
	if s.HeadersSecret != nil && s.TokenSecret != nil {
		errs = append(errs, errors.New("headersSecret and tokenSecret both set the Authorization header: declare one"))
	}
	return errors.Join(errs...)
}

// bearerAuthName is the bearertokenauth extension's component id.
const bearerAuthName = "bearertokenauth"

// bearerExtension is the extension that reads the token file. The kubelet
// refreshes a Secret volume by swapping a symlink; the extension re-reads the
// file when that happens (verified against contrib 0.162 in the F2 smoke), so
// a renewed token reaches the next export with no restart. The retry keeps the
// agent up, exporting without a token, until the Secret exists, so a first
// deploy never waits on whatever mints the token.
func bearerExtension() map[string]any {
	return map[string]any{
		"filename": agentTokenDir + "/" + agentTokenFile,
		"retry_on_failure": map[string]any{
			"enabled":     true,
			"interval":    "10s",
			"max_retries": 0,
		},
	}
}

// exporterName is the destination exporter's component id.
func (s ObservabilityAgentSpec) exporterName() string {
	if s.Protocol == "grpc" {
		return "otlp/destination"
	}
	return "otlphttp/destination"
}

func (s ObservabilityAgentSpec) exporter() map[string]any {
	e := map[string]any{
		"endpoint": s.Endpoint,
		"sending_queue": map[string]any{
			"enabled":       true,
			"queue_size":    2000,
			"storage":       "file_storage",
			"num_consumers": 4,
		},
		"retry_on_failure": map[string]any{
			"enabled":          true,
			"initial_interval": "5s",
			"max_interval":     "30s",
			"max_elapsed_time": "300s",
		},
	}
	if s.HeadersSecret != nil {
		e["headers"] = "${env:" + agentHeadersEnv + "}"
	}
	if s.TokenSecret != nil {
		e["auth"] = map[string]any{"authenticator": bearerAuthName}
	}
	if s.Protocol == "grpc" {
		if strings.HasPrefix(s.Endpoint, "http://") {
			e["tls"] = map[string]any{"insecure": true}
		}
	}
	return e
}

// logLines is the filelog operator chain for the JSON lines forge's runtime
// writes: level and timestamp promoted, trace_id/span_id moved onto the
// record so a log and its trace join.
func logLines() []any {
	return []any{
		map[string]any{
			"type": "json_parser", "if": `body matches "^\\s*\\{"`, "parse_from": "body", "on_error": "send_quiet",
			"timestamp": map[string]any{"parse_from": "attributes.time", "layout_type": "gotime", "layout": "2006-01-02T15:04:05.999999999Z07:00"},
			"severity":  map[string]any{"parse_from": "attributes.level"},
		},
		map[string]any{
			"type": "trace_parser", "if": "attributes.trace_id != nil",
			"trace_id": map[string]any{"parse_from": "attributes.trace_id"},
			"span_id":  map[string]any{"parse_from": "attributes.span_id"},
		},
		map[string]any{"type": "remove", "if": "attributes.trace_id != nil", "field": "attributes.trace_id"},
		map[string]any{"type": "remove", "if": "attributes.span_id != nil", "field": "attributes.span_id"},
		map[string]any{"type": "remove", "if": "attributes.time != nil", "field": "attributes.time"},
		map[string]any{"type": "remove", "if": "attributes.level != nil", "field": "attributes.level"},
		map[string]any{"type": "move", "if": "attributes.msg != nil", "from": "attributes.msg", "to": "body"},
	}
}

func strs(v ...string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// pipelines returns the service.pipelines map for the signals enabled,
// given the receivers each signal reads and the processors in front of the
// destination. A signal off is a pipeline not rendered.
func (s ObservabilityAgentSpec) pipelines(traceRecv, metricRecv, logRecv, profileRecv, pre []string, batch bool) map[string]any {
	withBatch := func(p []string) []string {
		if batch {
			return append(append([]string{}, p...), "batch")
		}
		return p
	}
	exp := strs(s.exporterName())
	out := map[string]any{}
	if s.Signals.Traces {
		// No sampling here, on purpose: the ratio is head-sampled in the SDK, so
		// a trace reaches the collector whole or not at all. A per-collector
		// decision would split a trace that spans nodes.
		out["traces"] = map[string]any{"receivers": strs(traceRecv...), "processors": strs(withBatch(pre)...), "exporters": exp}
	}
	if s.Signals.Metrics {
		out["metrics"] = map[string]any{"receivers": strs(metricRecv...), "processors": strs(withBatch(pre)...), "exporters": exp}
	}
	if s.Signals.Logs && len(logRecv) > 0 {
		out["logs"] = map[string]any{"receivers": strs(logRecv...), "processors": strs(withBatch(pre)...), "exporters": exp}
	}
	if s.Signals.Profiles {
		// Not batched: a profile is large and already self-contained.
		out["profiles"] = map[string]any{"receivers": strs(profileRecv...), "processors": strs(pre...), "exporters": exp}
	}
	return out
}

func (s ObservabilityAgentSpec) common(receivers, processors map[string]any) {
	receivers["otlp"] = map[string]any{"protocols": map[string]any{
		"grpc": map[string]any{"endpoint": "0.0.0.0:4317"},
		"http": map[string]any{"endpoint": "0.0.0.0:4318"},
	}}
	processors["batch"] = map[string]any{"send_batch_size": 8192, "timeout": "5s"}
}

// AgentConfig is the node agent's collector configuration, as YAML.
func (s ObservabilityAgentSpec) AgentConfig(namespace string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	receivers := map[string]any{}
	processors := map[string]any{}
	s.common(receivers, processors)
	limit := s.MemoryLimitMiB
	if limit <= 0 {
		limit = 256
	}
	processors["memory_limiter"] = map[string]any{"check_interval": "1s", "limit_mib": limit * 80 / 100, "spike_limit_mib": limit * 20 / 100}
	// Deleted from everything a client sends over OTLP, BEFORE k8sattributes.
	// k8sattributes never overwrites an attribute that is already set, and on a
	// lookup miss it falls back to the resource's own k8s.namespace.name for
	// its namespace-label extraction: a sender that misses the pod cache could
	// otherwise choose which namespace it is enriched as. Only OTLP pipelines
	// run this; filelog and kubeletstats derive k8s.* from the node itself.
	processors["transform/strip_client_identity"] = map[string]any{
		"error_mode":         "ignore",
		"trace_statements":   []any{map[string]any{"context": "resource", "statements": strs(stripIdentityStatement)}},
		"metric_statements":  []any{map[string]any{"context": "resource", "statements": strs(stripIdentityStatement)}},
		"log_statements":     []any{map[string]any{"context": "resource", "statements": strs(stripIdentityStatement)}},
		"profile_statements": []any{map[string]any{"context": "resource", "statements": strs(stripIdentityStatement)}},
	}
	processors["k8sattributes"] = map[string]any{
		"auth_type": "serviceAccount",
		"filter":    map[string]any{"node_from_env_var": "K8S_NODE_NAME"},
		"extract": map[string]any{
			"metadata": strs("k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.node.name", "k8s.deployment.name", "k8s.container.name"),
			"labels": []any{
				map[string]any{"tag_name": "service.name", "key": "app.kubernetes.io/name", "from": "pod"},
				map[string]any{"tag_name": "service.namespace", "key": "app.kubernetes.io/part-of", "from": "pod"},
				map[string]any{"tag_name": "deployment.environment.name", "key": "forge.dev/env", "from": "pod"},
			},
		},
		// Enrichment only. OTLP from a pod is associated by the resource's
		// pod uid when the SDK set one, else by the connection's source IP;
		// nothing here decides whose telemetry it is.
		"pod_association": []any{
			map[string]any{"sources": []any{map[string]any{"from": "resource_attribute", "name": "k8s.pod.uid"}}},
			map[string]any{"sources": []any{map[string]any{"from": "connection"}}},
		},
	}
	receivers["kubeletstats"] = map[string]any{
		"collection_interval":  "30s",
		"auth_type":            "serviceAccount",
		"endpoint":             "https://${env:K8S_NODE_IP}:10250",
		"insecure_skip_verify": true,
	}
	logOps := append([]any{map[string]any{"type": "container", "id": "container-parser"}}, logLines()...)
	receivers["filelog"] = map[string]any{
		"include":           strs("/var/log/pods/*/*/*.log"),
		"exclude":           strs("/var/log/pods/" + namespace + "_*/*/*.log"),
		"start_at":          "end",
		"include_file_path": true,
		"storage":           "file_storage",
		"operators":         logOps,
	}
	exporters := map[string]any{s.exporterName(): s.exporter()}
	extensions := map[string]any{
		"health_check": map[string]any{"endpoint": "0.0.0.0:13133"},
		"file_storage": map[string]any{"directory": "/var/lib/otel-agent", "create_directory": true},
	}
	extNames := []string{"health_check", "file_storage"}
	if s.TokenSecret != nil {
		extensions[bearerAuthName] = bearerExtension()
		extNames = append(extNames, bearerAuthName)
	}
	cfg := map[string]any{
		"extensions": extensions,
		"receivers":  receivers,
		"processors": processors,
		"exporters":  exporters,
		"service": map[string]any{
			"extensions": strs(extNames...),
			"pipelines":  s.agentPipelines(),
		},
	}
	return marshalConfig(cfg)
}

// agentPipelines splits what clients PUSH (OTLP: traces, metrics, profiles)
// from what the node itself produces (container stdout, kubelet stats). The
// pushed pipelines strip client-supplied identity before enrichment; the node
// ones must not, because their k8s.* attributes are the trustworthy kind.
func (s ObservabilityAgentSpec) agentPipelines() map[string]any {
	pushed := []string{"memory_limiter", "transform/strip_client_identity", "k8sattributes"}
	node := []string{"memory_limiter", "k8sattributes"}
	all := s.pipelines([]string{"otlp"}, []string{"otlp"}, nil, []string{"otlp"}, pushed, true)
	if s.Signals.Metrics {
		// kubeletstats is pulled, not pushed: its own pipeline, same exporter.
		all["metrics/node"] = map[string]any{
			"receivers": strs("kubeletstats"), "processors": strs(append(append([]string{}, node...), "batch")...), "exporters": strs(s.exporterName()),
		}
	}
	if s.Signals.Logs {
		all["logs"] = map[string]any{
			"receivers": strs("filelog"), "processors": strs(append(append([]string{}, node...), "batch")...), "exporters": strs(s.exporterName()),
		}
	}
	return all
}

// ForwarderConfig is the local forwarder's collector configuration: OTLP in
// on 4317/4318, the files `forge env up` writes under /var/log/forge in, and
// the destination out. There is no Kubernetes here to enrich from.
func (s ObservabilityAgentSpec) ForwarderConfig() (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if s.TokenSecret != nil {
		return "", errors.New("destination.token_secret is a file mounted into the cluster node agent and the local forwarder has none: use headers_secret for a local env")
	}
	receivers := map[string]any{}
	processors := map[string]any{}
	s.common(receivers, processors)
	processors["memory_limiter"] = map[string]any{"check_interval": "1s", "limit_mib": 200, "spike_limit_mib": 50}
	logOps := append([]any{
		map[string]any{"type": "regex_parser", "regex": `^/var/log/forge/(?P<env>[^/]+)/(?P<service>[^/]+)\.log$`, "parse_from": `attributes["log.file.path"]`},
		map[string]any{"type": "move", "from": "attributes.service", "to": `resource["service.name"]`},
		map[string]any{"type": "move", "from": "attributes.env", "to": `resource["deployment.environment.name"]`},
		map[string]any{"type": "remove", "field": `attributes["log.file.path"]`},
	}, logLines()...)
	receivers["filelog"] = map[string]any{
		"include": strs("/var/log/forge/*/*.log"),
		"exclude": strs(
			"/var/log/forge/*/*.[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T*.log",
			// The browser SDK ships the console itself; tailing the mirror
			// file would store every line twice.
			"/var/log/forge/*/frontend_*.log"),
		"start_at":          "end",
		"include_file_path": true,
		"include_file_name": false,
		"operators":         logOps,
	}
	exp := s.exporter()
	// No persistent queue locally: there is no host directory to keep it in.
	delete(exp["sending_queue"].(map[string]any), "storage")
	cfg := map[string]any{
		"extensions": map[string]any{"health_check": map[string]any{"endpoint": "0.0.0.0:13133"}},
		"receivers":  receivers,
		"processors": processors,
		"exporters":  map[string]any{s.exporterName(): exp},
		"service": map[string]any{
			"extensions": strs("health_check"),
			"pipelines": s.pipelines(
				[]string{"otlp"}, []string{"otlp"}, []string{"filelog"}, []string{"otlp"},
				[]string{"memory_limiter"}, true),
		},
	}
	return marshalConfig(cfg)
}

func marshalConfig(cfg map[string]any) (string, error) {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal collector config: %w", err)
	}
	return string(b), nil
}

// CollectorArgs are the collector's command-line args. The feature gate is
// what lets the profiles pipeline load.
func (s ObservabilityAgentSpec) CollectorArgs(configPath string) []string {
	args := []string{"--config=" + configPath}
	if s.Signals.Profiles {
		args = append(args, "--feature-gates=service.profilesSupport")
	}
	return args
}

// ObservabilityContext is the placement of one agent.
type ObservabilityContext struct {
	// Namespace the agent runs in. It is created here, because the agent
	// needs hostPath volumes the Restricted pod-security level forbids, so
	// the namespace must not inherit the env's restricted one.
	Namespace string
	PartOf    string
	Env       string
}

// RenderObservabilityAgent renders the node agent: Namespace, ServiceAccount,
// ClusterRole + Binding, ConfigMap, DaemonSet and the node-local Service.
// Pure and deterministic.
//
// WHAT FILELOG NEEDS. Container stdout lives in /var/log/pods on the node,
// root-readable only, so the pod mounts that directory read-only as a
// hostPath and runs as root with every capability dropped and a read-only
// root filesystem. A second hostPath (agentStorageDir) holds checkpoints and
// the exporter's queue. hostPath is forbidden by pod-security "baseline", so
// the namespace is labelled privileged: this is why the agent is its own
// namespace and why it is never rendered by the Restricted (hosted) profile.
func RenderObservabilityAgent(s ObservabilityAgentSpec, ctx ObservabilityContext) ([]*unstructured.Unstructured, error) {
	if ctx.Namespace == "" {
		return nil, errors.New("observability agent has no namespace")
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("observability agent: %w", err)
	}
	if s.Image == "" || !imageRE.MatchString(s.Image) {
		return nil, fmt.Errorf("observability agent: image %q is required", s.Image)
	}
	cfg, err := s.AgentConfig(ctx.Namespace)
	if err != nil {
		return nil, err
	}
	limit := s.MemoryLimitMiB
	if limit <= 0 {
		limit = 256
	}
	name, ns := ObservabilityAgentName, ctx.Namespace
	labels := managedLabels(name, ctx.PartOf)
	selector := map[string]string{LabelName: name}

	nsObj := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{
			LabelManagedBy: "forge",
			// hostPath needs more than "restricted" or "baseline" allow.
			"pod-security.kubernetes.io/enforce": "privileged",
			"pod-security.kubernetes.io/audit":   "privileged",
			"pod-security.kubernetes.io/warn":    "privileged",
		}},
	}
	sa := &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: objectMeta(name, ns, labels)}
	role := &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: objectMeta(ns+"-"+name, "", labels),
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods", "namespaces", "nodes"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"apps"}, Resources: []string{"replicasets", "deployments"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"nodes/stats"}, Verbs: []string{"get"}},
		},
	}
	binding := &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: objectMeta(ns+"-"+name, "", labels),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: ns + "-" + name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: ns}},
	}
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: objectMeta(name, ns, labels),
		Data:       map[string]string{"config.yaml": cfg},
	}

	env := []corev1.EnvVar{
		{Name: "K8S_NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
		{Name: "K8S_NODE_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"}}},
		// The collector honours GOMEMLIMIT; keep it under the container limit.
		{Name: "GOMEMLIMIT", Value: strconv.Itoa(limit*80/100) + "MiB"},
	}
	if s.HeadersSecret != nil {
		env = append(env, corev1.EnvVar{Name: agentHeadersEnv, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: s.HeadersSecret.Name}, Key: s.HeadersSecret.Key},
		}})
	}
	volumeMounts := []corev1.VolumeMount{
		{Name: "config", MountPath: "/etc/otel", ReadOnly: true},
		{Name: "pod-logs", MountPath: "/var/log/pods", ReadOnly: true},
		{Name: "storage", MountPath: "/var/lib/otel-agent"},
	}
	hostPathPods, hostPathStore := corev1.HostPathDirectory, corev1.HostPathDirectoryOrCreate
	readOnly, noEscalate, root := true, false, int64(0)
	volumes := []corev1.Volume{
		{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
		{Name: "pod-logs", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/log/pods", Type: &hostPathPods}}},
		{Name: "storage", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: agentStorageDir, Type: &hostPathStore}}},
	}
	if s.TokenSecret != nil {
		// A whole-directory Secret volume (never subPath) so the kubelet
		// refreshes the token in place. optional: the agent starts, and
		// retries the file, before whatever renews the token has created
		// the Secret.
		optional := true
		volumes = append(volumes, corev1.Volume{Name: "otlp-token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: s.TokenSecret.Name,
			Optional:   &optional,
			Items:      []corev1.KeyToPath{{Key: s.TokenSecret.Key, Path: agentTokenFile}},
		}}})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "otlp-token", MountPath: agentTokenDir, ReadOnly: true})
	}
	podLabels := copyLabels(labels)
	ds := &appsv1.DaemonSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
		ObjectMeta: objectMeta(name, ns, labels),
		Spec: appsv1.DaemonSetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: selector},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: name,
					// Same default as every forge pod: no service-link env.
					EnableServiceLinks: new(false),
					// Every node, tainted ones included: a node without the
					// agent is a node whose pods have nowhere local to send.
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					Containers: []corev1.Container{{
						Name:  "collector",
						Image: s.Image,
						Args:  s.CollectorArgs("/etc/otel/config.yaml"),
						Env:   env,
						Ports: []corev1.ContainerPort{
							{Name: "otlp-grpc", ContainerPort: 4317, Protocol: corev1.ProtocolTCP},
							{Name: "otlp-http", ContainerPort: 4318, Protocol: corev1.ProtocolTCP},
							{Name: "health", ContainerPort: 13133, Protocol: corev1.ProtocolTCP},
						},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(13133)}}},
						LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(13133)}}, InitialDelaySeconds: 10},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse(strconv.Itoa(limit/2) + "Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(strconv.Itoa(limit) + "Mi")},
						},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:                &root,
							AllowPrivilegeEscalation: &noEscalate,
							ReadOnlyRootFilesystem:   &readOnly,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						VolumeMounts: volumeMounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
	local := corev1.ServiceInternalTrafficPolicyLocal
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(name, ns, labels),
		Spec: corev1.ServiceSpec{
			Selector:              selector,
			InternalTrafficPolicy: &local,
			Ports: []corev1.ServicePort{
				{Name: "otlp-grpc", Port: 4317, TargetPort: intstr.FromString("otlp-grpc"), Protocol: corev1.ProtocolTCP},
				{Name: "otlp-http", Port: 4318, TargetPort: intstr.FromString("otlp-http"), Protocol: corev1.ProtocolTCP},
			},
		},
	}

	out, err := toUnstructured([]runtime.Object{nsObj, sa, role, binding, cm, ds, svc})
	if err != nil {
		return nil, err
	}
	stampEnv(out, ctx.Env)
	return out, nil
}
