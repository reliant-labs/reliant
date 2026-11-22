package collector

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// CollectorRef names the collector a NetworkPolicy admits: the namespace it
// runs in and the labels of its pods.
type CollectorRef struct {
	Namespace string
	PodLabels map[string]string
}

// PulledPorts are the ports a collector reaches t on: every scrape, profile
// pull and check port, sorted and unique. A named port resolves against
// t.Ports.
func PulledPorts(t telemetry.Target) []int {
	t = telemetry.Normalize([]telemetry.Target{t})[0]
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if p > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, m := range t.Metrics {
		add(m.Port)
	}
	for _, p := range t.Profiles {
		add(p.Port)
	}
	for _, c := range t.Checks {
		add(c.Port)
	}
	sort.Ints(out)
	return out
}

// IngressRule is one NetworkPolicy ingress rule admitting the collector c,
// and nothing else, on t's pulled ports. Nil when t pulls nothing.
func IngressRule(t telemetry.Target, c CollectorRef) map[string]any {
	ports := PulledPorts(t)
	if len(ports) == 0 {
		return nil
	}
	var pp []any
	for _, p := range ports {
		pp = append(pp, map[string]any{"protocol": "TCP", "port": p})
	}
	peer := map[string]any{
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": c.Namespace}},
	}
	if len(c.PodLabels) > 0 {
		peer["podSelector"] = map[string]any{"matchLabels": stringMap(c.PodLabels)}
	}
	return map[string]any{"from": []any{peer}, "ports": pp}
}

// WorkloadIngressRule is the rule forge adds to its own NetworkPolicy for a
// Workload target whose Target.AdmitCollector holds (auto or true), so the
// policy that isolates the workload's pods still lets the collector pull
// them. It never asks for a policy of its own: ok is false for any other
// attachment or runtime, for NetworkPolicy false, and when t pulls nothing.
func WorkloadIngressRule(t telemetry.Target, c CollectorRef) (rule map[string]any, ok bool) {
	if t.Attachment != telemetry.AttachWorkload || t.Runtime != telemetry.RuntimeCluster || !t.AdmitCollector() {
		return nil, false
	}
	rule = IngressRule(t, c)
	return rule, rule != nil
}

// NetworkPolicies renders the standalone NetworkPolicy objects the targets
// ask for: one per (target, namespace) for each cluster target whose
// NetworkPolicy is explicitly true, selecting its pods and admitting only
// the collector c on only its pulled ports.
//
// Only an explicit true renders one, because a NetworkPolicy that selects a
// pod no other policy selected makes the pod deny every ingress the policies
// do not allow: a policy forge invented for a system it cannot see into
// would cut that system's own traffic. Unset (auto) renders none here: a
// Workload's rule joins forge's own policy (WorkloadIngressRule), and a
// Manifests or Observability target's admission is the project's.
//
// skipped names the targets that asked for a policy and cannot have one: no
// selector (an address), or no namespace to put it in.
func NetworkPolicies(targets []telemetry.Target, c CollectorRef) (policies []map[string]any, skipped []string) {
	for _, t := range telemetry.Normalize(targets) {
		if t.NetworkPolicy == nil || !*t.NetworkPolicy || t.Runtime != telemetry.RuntimeCluster {
			continue
		}
		rule := IngressRule(t, c)
		switch {
		case rule == nil:
			continue
		case len(t.Selector) == 0:
			skipped = append(skipped, fmt.Sprintf("telemetry %s (service %s): network_policy needs pods to select; an address has none", t.Source, t.ServiceName))
			continue
		case len(t.Namespaces) == 0:
			skipped = append(skipped, fmt.Sprintf("telemetry %s (service %s): network_policy needs the namespaces its pods run in", t.Source, t.ServiceName))
			continue
		}
		for _, ns := range t.Namespaces {
			policies = append(policies, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "NetworkPolicy",
				"metadata": map[string]any{
					"name":      policyName(t.ServiceName),
					"namespace": ns,
					"labels": map[string]any{
						"app.kubernetes.io/managed-by": "forge",
						"forge.dev/telemetry-service":  labelValue(t.ServiceName),
					},
				},
				"spec": map[string]any{
					"podSelector": map[string]any{"matchLabels": stringMap(t.Selector)},
					"policyTypes": strs("Ingress"),
					"ingress":     []any{rule},
				},
			})
		}
	}
	sort.SliceStable(policies, func(i, j int) bool {
		mi, mj := policies[i]["metadata"].(map[string]any), policies[j]["metadata"].(map[string]any)
		if mi["namespace"] != mj["namespace"] {
			return mi["namespace"].(string) < mj["namespace"].(string)
		}
		return mi["name"].(string) < mj["name"].(string)
	})
	return policies, skipped
}

var notDNS = regexp.MustCompile(`[^a-z0-9-]+`)

// policyName includes the original service's digest: punctuation folding and
// truncation can map distinct services to the same readable DNS label prefix.
func policyName(svc string) string {
	const prefix = "telemetry-"
	sum := sha256.Sum256([]byte(svc))
	suffix := fmt.Sprintf("-%x", sum[:8])
	name := prefix + strings.Trim(notDNS.ReplaceAllString(strings.ToLower(svc), "-"), "-")
	if limit := 63 - len(suffix); len(name) > limit {
		name = name[:limit]
	}
	return strings.TrimRight(name, "-") + suffix
}

// labelValue is svc cut to a label value's 63 characters.
func labelValue(svc string) string {
	if len(svc) > 63 {
		return strings.TrimRight(svc[:63], "-_.")
	}
	return svc
}

func stringMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
