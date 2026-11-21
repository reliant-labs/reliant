package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/deploy"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/telemetry/collector"
)

// The NetworkPolicy half of declared telemetry (forge.Telemetry) on a
// cluster. Which collector pulls a cluster's targets is decided once, here:
// the env's own collector when it names one (Observability.telemetry_collector,
// rendered as output.telemetry_collector), else forge's node agent on that
// cluster (an ObservabilityAgent record in the stream), else none, and then
// nothing is admitted, because nothing pulls.
//
// Admission follows Target.AdmitCollector:
//
//   - a Workload target's rule joins forge's own per-workload policy
//     (deploy.Context.Telemetry), which selects those pods anyway;
//   - a Manifests or Observability target gets a standalone policy only when
//     it says network_policy = True (collector.NetworkPolicies), placed
//     beside its Manifests group's objects with the group's owner and
//     delivery unit, so it ships and prunes with them.

// envTelemetry is what the render declares: the normalized targets and the
// env's own collector, if any.
type envTelemetry struct {
	targets []telemetry.Target
	own     *collector.CollectorRef
	env     string
}

// decodeTelemetry reads output.telemetry and output.telemetry_collector. nil
// when the env declares no telemetry, which renders exactly as before.
func decodeTelemetry(out map[string]any) (*envTelemetry, error) {
	raw, ok := out["telemetry"]
	if !ok || raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("output.telemetry: %w", err)
	}
	targets, err := telemetry.Decode(b)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	e := &envTelemetry{targets: telemetry.Normalize(targets)}
	e.env, _ = out["env"].(string)
	if rawOwn, ok := out["telemetry_collector"]; ok && rawOwn != nil {
		b, err := json.Marshal(rawOwn)
		if err != nil {
			return nil, fmt.Errorf("output.telemetry_collector: %w", err)
		}
		var own struct {
			Namespace string            `json:"namespace"`
			PodLabels map[string]string `json:"pod_labels"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&own); err != nil {
			return nil, fmt.Errorf("decode output.telemetry_collector: %w", err)
		}
		if own.Namespace == "" || len(own.PodLabels) == 0 {
			return nil, errors.New("output.telemetry_collector needs a namespace and pod_labels")
		}
		e.own = &collector.CollectorRef{Namespace: own.Namespace, PodLabels: own.PodLabels}
	}
	return e, nil
}

// collectors maps each cluster (its forge.dev/cluster value) to the
// collector that pulls its targets.
func (e *envTelemetry) collectors(items []any) map[string]collector.CollectorRef {
	out := map[string]collector.CollectorRef{}
	if e == nil {
		return out
	}
	if e.own != nil {
		for _, t := range e.targets {
			if t.Runtime == telemetry.RuntimeCluster {
				out[t.Cluster] = *e.own
			}
		}
		return out
	}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok || m["apiVersion"] != deployv1alpha1.GroupVersion.String() || m["kind"] != deploy.ObservabilityAgentKind {
			continue
		}
		meta, _ := m["metadata"].(map[string]any)
		ns, _ := meta["namespace"].(string)
		labels, _ := meta["labels"].(map[string]any)
		c, _ := labels[ClusterRoutingLabel].(string)
		if ns != "" {
			out[c] = deploy.AgentCollectorRef(ns)
		}
	}
	return out
}

// workloads is the Context.Telemetry of one workload group: its cluster's
// Workload targets and their collector, nil when there are none or nothing
// pulls them.
func (e *envTelemetry) workloads(cluster string, refs map[string]collector.CollectorRef) *deploy.WorkloadTelemetry {
	if e == nil {
		return nil
	}
	ref, ok := refs[cluster]
	if !ok {
		return nil
	}
	var targets []telemetry.Target
	for _, t := range e.targets {
		if t.Attachment == telemetry.AttachWorkload && t.Runtime == telemetry.RuntimeCluster && t.Cluster == cluster {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return &deploy.WorkloadTelemetry{Collector: ref, Targets: targets}
}

// insertTelemetryPolicies adds the standalone NetworkPolicies Manifests,
// Observability and ManagedDatabase targets ask for (network_policy = True)
// to the stream: after the last object of the target's Manifests group or
// ManagedDatabase, carrying its owner and delivery unit, or at the end for
// an Observability target, a ManagedDatabase's CloudNativePG operator target
// (or an unnamed group, whose objects carry no owner to find them by).
func insertTelemetryPolicies(items []any, e *envTelemetry, refs map[string]collector.CollectorRef) ([]any, error) {
	if e == nil {
		return items, nil
	}
	clusters := make([]string, 0, len(refs))
	for c := range refs {
		clusters = append(clusters, c)
	}
	sort.Strings(clusters)
	after := map[int][]any{}
	var tail []any
	var errs []error
	for _, c := range clusters {
		for _, t := range e.targets {
			if t.Runtime != telemetry.RuntimeCluster || t.Cluster != c ||
				!standalonePolicyAttachment(t.Attachment) {
				continue
			}
			policies, skipped := collector.NetworkPolicies([]telemetry.Target{t}, refs[c])
			for _, s := range skipped {
				errs = append(errs, errors.New(s))
			}
			at := groupEnd(items, t.Source, c)
			for _, p := range policies {
				labels := p["metadata"].(map[string]any)["labels"].(map[string]any)
				if c != "" {
					labels[ClusterRoutingLabel] = c
				}
				if e.env != "" {
					labels[deploy.LabelEnv] = e.env
				}
				if at < 0 {
					tail = append(tail, p)
					continue
				}
				for _, k := range []string{WorkloadLabel, release.LabelDeliveryUnit} {
					if v, ok := itemLabels(items[at])[k]; ok {
						labels[k] = v
					}
				}
				after[at] = append(after[at], p)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if len(after) == 0 && len(tail) == 0 {
		return items, nil
	}
	out := make([]any, 0, len(items)+len(tail))
	for i, it := range items {
		out = append(out, it)
		out = append(out, after[i]...)
	}
	return append(out, tail...), nil
}

// standalonePolicyAttachment is whether a target of attachment a gets its
// own NetworkPolicy when it asks for one. A Workload's or HostInfra's rule
// joins the policy forge already renders for it instead (deploy.Context).
func standalonePolicyAttachment(a telemetry.Attachment) bool {
	switch a {
	case telemetry.AttachManifests, telemetry.AttachObservability, telemetry.AttachManagedDatabase:
		return true
	}
	return false
}

// groupEnd is the index of the last object on cluster c of the Manifests
// group source names ("manifests:<name>") or of the ManagedDatabase it names
// ("manageddatabase:<name>": the objects its record expanded into, grouped by
// name like every object forge renders), -1 when there is none.
func groupEnd(items []any, source, c string) int {
	group := func(l map[string]string) string { return l[WorkloadLabel] }
	name, ok := strings.CutPrefix(source, "manifests:")
	if !ok {
		name, ok = strings.CutPrefix(source, "manageddatabase:")
		group = ManifestGroup
	}
	if !ok || name == "" {
		return -1
	}
	at := -1
	for i, it := range items {
		l := itemLabels(it)
		if group(l) == name && l[ClusterRoutingLabel] == c {
			at = i
		}
	}
	return at
}

func itemLabels(it any) map[string]string {
	m, _ := it.(map[string]any)
	meta, _ := m["metadata"].(map[string]any)
	raw, _ := meta["labels"].(map[string]any)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
