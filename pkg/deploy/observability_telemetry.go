package deploy

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/telemetry/collector"
)

// The node agent collects the env's declared telemetry (forge.Telemetry,
// pkg/telemetry) on its cluster: ObservabilityAgentSpec.Telemetry is the
// cluster's targets, and collector.Build is the one translator that turns
// them into collector config. The agent owns only the splicing:
//
//   - the Fragment's components and pipelines join the config whole
//     (Fragment.MergeInto, which refuses to overwrite one of the agent's);
//   - Logs.Preserve goes into filelog right after the `container` operator,
//     before forge's generic JSON stage (logLines), which would otherwise
//     consume the raw line the per-target parse reads;
//   - Logs.Processors run in the file-logs pipeline after k8sattributes,
//     which extracts the selector labels (K8sAttributesLabels) they match on;
//   - NodeMetricsProcessors run in metrics/node (kubeletstats) after
//     k8sattributes, so a target's pods' resource metrics carry its
//     service.name;
//   - the identity processor also runs in every pushed (OTLP) pipeline: they
//     share k8sattributes, which now extracts the selector labels on pushed
//     data too, and the identity processor is what deletes them.
//
// The agent is a per-node DaemonSet, so it pulls only its own node's pods
// (collector.DiscoverNodePods). It has no singleton, so a target it would
// have to pull at an ADDRESS (which every node could reach, and so every
// node would pull) is refused rather than collected N times or silently not
// at all: declare its pods by selector, or collect it with your own
// collector (Observability.telemetry_collector).

// TelemetryMountRoot is where the node agent mounts the Secrets declared
// telemetry reads (a bearer token, a CA): Secret <s> at TelemetryMountRoot/<s>,
// each referenced key a file, as collector.Options.MountRoot expects.
const TelemetryMountRoot = "/etc/forge/telemetry/secrets"

// AgentCollectorRef is the node agent as a NetworkPolicy peer: its namespace
// and the label its pods are selected by. A NetworkPolicy that admits it
// (collector.WorkloadIngressRule, collector.NetworkPolicies) admits nothing
// else.
func AgentCollectorRef(namespace string) collector.CollectorRef {
	return collector.CollectorRef{Namespace: namespace, PodLabels: map[string]string{LabelName: ObservabilityAgentName}}
}

// isolatedExporterName is the exporter isolated targets' metrics go to: the
// destination with a bounded in-memory queue that drops on overflow.
func (s ObservabilityAgentSpec) isolatedExporterName() string {
	return s.exporterName() + "_telemetry_isolated"
}

// telemetryResourceAttrs is the env's identity, stamped on a project-scoped
// target's signals: the same keys the forge SDK reads from
// OTEL_RESOURCE_ATTRIBUTES (kcl/render.k _otel_resource_attributes).
func telemetryResourceAttrs(ctx ObservabilityContext) map[string]string {
	out := map[string]string{}
	if ctx.Env != "" {
		out["deployment.environment.name"] = ctx.Env
	}
	if ctx.PartOf != "" {
		out["service.namespace"] = ctx.PartOf
	}
	return out
}

// checkTelemetry refuses what the node agent cannot collect as declared. A
// declaration it would drop silently is a signal nobody sees, so each of
// these is an error naming the fix.
func (s ObservabilityAgentSpec) checkTelemetry() error {
	var errs []error
	for _, t := range telemetry.Normalize(s.declaredTelemetry()) {
		id := fmt.Sprintf("telemetry %s (service %s)", t.Source, t.ServiceName)
		if t.Runtime != telemetry.RuntimeCluster {
			errs = append(errs, fmt.Errorf("%s: runtime %q is not a cluster: the node agent collects only its cluster's targets", id, t.Runtime))
			continue
		}
		if t.Address != "" && (t.Pulled() || t.Logs != nil) {
			errs = append(errs, fmt.Errorf("%s: address %s cannot be collected by forge's node agent: it is one collector per node and has no singleton to pull an address exactly once (and an address names no pod whose logs it tails). Select the pods (selector) instead, or collect it with your own collector (Observability.telemetry_collector)", id, t.Address))
		}
		if (len(t.Metrics) > 0 || len(t.Checks) > 0) && !s.Signals.Metrics {
			errs = append(errs, fmt.Errorf("%s: declares metrics or checks, but the env's observability ships no metrics (signals.metrics = False)", id))
		}
		if t.Logs != nil && !s.Signals.Logs {
			errs = append(errs, fmt.Errorf("%s: declares a log parse, but the env's observability ships no logs (signals.logs = False)", id))
		}
		if len(t.Profiles) > 0 && !s.Signals.Profiles {
			errs = append(errs, fmt.Errorf("%s: declares profiles, but the env's observability ships none to its destination: set Bundle.observability.signals.profiles = True (the destination must accept OTLP profiles), or drop the profiles", id))
		}
		for _, m := range secretMounts(t) {
			if m.Optional {
				errs = append(errs, fmt.Errorf("%s: secret %s key %s is optional, and the node agent cannot run without it: the collector reads every credentials file when it loads its config, so a missing one stops the whole agent. Make the reference required", id, m.Secret, m.Key))
			}
		}
	}
	return errors.Join(errs...)
}

// secretMounts is every credential t reads from a file.
func secretMounts(t telemetry.Target) []*telemetry.SecretMount {
	var out []*telemetry.SecretMount
	add := func(m *telemetry.SecretMount) {
		if m != nil {
			out = append(out, m)
		}
	}
	ca := func(tls *telemetry.TLS) *telemetry.SecretMount {
		if tls == nil {
			return nil
		}
		return tls.CA
	}
	for _, m := range t.Metrics {
		add(m.BearerToken)
		add(ca(m.TLS))
	}
	for _, p := range t.Profiles {
		add(p.BearerToken)
		add(ca(p.TLS))
	}
	return out
}

// telemetryFragment is the declared telemetry's collector config for this
// agent, nil when it declares none.
func (s ObservabilityAgentSpec) telemetryFragment(ctx ObservabilityContext) (*collector.Fragment, error) {
	if len(s.declaredTelemetry()) == 0 {
		return nil, nil
	}
	if err := s.checkTelemetry(); err != nil {
		return nil, err
	}
	// The destination as the isolated targets' exporter: same endpoint and
	// credentials, and collector.Build gives it the bounded in-memory queue.
	iso := s.exporter()
	delete(iso, "sending_queue")
	delete(iso, "retry_on_failure")
	f, err := collector.Build(s.declaredTelemetry(), collector.Options{
		Runtime:           telemetry.RuntimeCluster,
		Discovery:         collector.DiscoverNodePods,
		NodeNameEnv:       "K8S_NODE_NAME",
		MountRoot:         TelemetryMountRoot,
		ResourceAttrs:     telemetryResourceAttrs(ctx),
		EnableProfiles:    s.Signals.Profiles,
		Processors:        []string{"memory_limiter"},
		PostProcessors:    []string{"batch"},
		MetricsExporters:  []string{s.exporterName()},
		ProfilesExporters: []string{s.exporterName()},
		Isolated:          &collector.IsolatedExporter{Name: s.isolatedExporterName(), Config: iso},
	})
	if err != nil {
		return nil, err
	}
	// checkTelemetry refuses everything Build would skip, so a warning here
	// is a declaration the agent would not collect: fail closed.
	if len(f.Warnings) > 0 {
		var errs []error
		for _, w := range f.Warnings {
			errs = append(errs, errors.New(w))
		}
		return nil, errors.Join(errs...)
	}
	return &f, nil
}

// telemetrySecretVolumes mounts every Secret the declared telemetry reads,
// one volume per Secret at TelemetryMountRoot/<secret>, projecting only the
// keys a target references. NOT optional: the collector stats each file when
// it loads its config, so a pod that started without one would crash-loop
// the agent for every signal on the node. A Secret that is missing keeps the
// pod Pending, which names the Secret.
func (s ObservabilityAgentSpec) telemetrySecretVolumes() ([]corev1.Volume, []corev1.VolumeMount) {
	keys := map[string]map[string]bool{}
	for _, t := range telemetry.Normalize(s.declaredTelemetry()) {
		for _, m := range secretMounts(t) {
			if keys[m.Secret] == nil {
				keys[m.Secret] = map[string]bool{}
			}
			keys[m.Secret][m.Key] = true
		}
	}
	secrets := make([]string, 0, len(keys))
	for name := range keys {
		secrets = append(secrets, name)
	}
	sort.Strings(secrets)
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	for i, name := range secrets {
		var items []corev1.KeyToPath
		var ks []string
		for k := range keys[name] {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			items = append(items, corev1.KeyToPath{Key: k, Path: k})
		}
		vol := "telemetry-secret-" + strconv.Itoa(i)
		volumes = append(volumes, corev1.Volume{Name: vol, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: name,
			Items:      items,
		}}})
		mounts = append(mounts, corev1.VolumeMount{Name: vol, MountPath: TelemetryMountRoot + "/" + name, ReadOnly: true})
	}
	return volumes, mounts
}
