package cluster

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

// The pin annotation: a store-backed Secret pinned to version N rolls the
// workloads that read it when the pin moves to N+1, and only after N+1 has
// synced.
//
// A consumer's pod template carries release.AnnotationSecretPins, one
// "<secret>/<NAME>=<version>" per pinned name it reads, sorted and joined. Moving
// a pin changes the value, so the pod template changes, so the controller rolls
// the workload. That is the entire "reloader": no controller watches Secrets
// (another cluster-wide Secret watcher, and one that would fight Flux over pod
// templates), and nothing here restarts anything.
//
// WHY THE ROLL IS SAFE. The annotation moves with the ExternalSecret in the same
// stream, and the secret stages (secret_order.go) hold every workload until that
// ExternalSecret reports it has synced THIS spec. So a Deployment's new pod
// template reaches the cluster only after the Secret holds version N+1; the
// pods the roll starts read N+1. Stamped by Go in the one extraction hook
// (extractManifestsIn), so `forge env render`, the shape, the bundle and the
// direct apply all see the same pod templates and the same digests.
//
// WHAT CARRIES IT. Deployment, StatefulSet, DaemonSet and CronJob. A one-shot
// Job is deliberately left alone: its name is a hash of its spec, so a stamp
// would rename and RE-RUN a migration for a rotation; the pre-rollout gate
// already holds it until the ExternalSecret has synced, so it reads the current
// value without being re-run.

// pinnedKinds are the kinds whose pod template takes the annotation, with the
// path to that template.
var pinnedKinds = map[string][]string{
	"Deployment":  {"spec", "template"},
	"StatefulSet": {"spec", "template"},
	"DaemonSet":   {"spec", "template"},
	"CronJob":     {"spec", "jobTemplate", "spec", "template"},
}

// secretKey locates a Secret the way a pod resolves it: its namespace and name.
type secretKey struct{ namespace, name string }

// stampSecretPins sets release.AnnotationSecretPins on the pod template of every
// workload in items that reads a Secret an ExternalSecret in items pins. It
// returns items unchanged when nothing is pinned.
func stampSecretPins(items []any) []any {
	pins := pinnedSecrets(items)
	if len(pins) == 0 {
		return items
	}
	for _, it := range items {
		doc, _ := it.(map[string]any)
		kind, _ := doc["kind"].(string)
		path, ok := pinnedKinds[kind]
		if !ok {
			continue
		}
		consumed := consumedPins(doc, pins)
		if len(consumed) == 0 {
			continue
		}
		setPodTemplateAnnotation(doc, path, release.AnnotationSecretPins, strings.Join(consumed, ","))
	}
	return items
}

// pinnedSecrets maps each Secret an ExternalSecret writes to its pinned names,
// as "<secret>/<NAME>=<version>". An ExternalSecret that pins nothing is a
// floating read and contributes nothing: there is no version to move.
func pinnedSecrets(items []any) map[secretKey][]string {
	out := map[secretKey][]string{}
	for _, it := range items {
		doc, _ := it.(map[string]any)
		apiVersion, _ := doc["apiVersion"].(string)
		if kind, _ := doc["kind"].(string); kind != "ExternalSecret" || !release.IsESOKind(apiVersion, kind) {
			continue
		}
		md, _ := doc["metadata"].(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		name, _ := md["name"].(string)
		ns, _ := md["namespace"].(string)
		if target, _ := spec["target"].(map[string]any); target != nil {
			if t, _ := target["name"].(string); t != "" {
				name = t
			}
		}
		data, _ := spec["data"].([]any)
		for _, d := range data {
			ref, _ := d.(map[string]any)["remoteRef"].(map[string]any)
			version, _ := ref["version"].(string)
			key, _ := ref["key"].(string)
			if version == "" || key == "" {
				continue
			}
			store := key[strings.LastIndex(key, "/")+1:]
			k := secretKey{ns, name}
			out[k] = append(out[k], name+"/"+store+"="+version)
		}
	}
	return out
}

// consumedPins is the sorted, de-duplicated pins of every Secret doc reads.
func consumedPins(doc map[string]any, pins map[secretKey][]string) []string {
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil
	}
	md, _ := doc["metadata"].(map[string]any)
	ns, _ := md["namespace"].(string)
	seen := map[string]bool{}
	var out []string
	for ref := range CollectManifestRefs(string(b), RefPlacement{Namespace: ns}).Secrets {
		for _, pin := range pins[secretKey{ref.Namespace, ref.Name}] {
			if !seen[pin] {
				seen[pin] = true
				out = append(out, pin)
			}
		}
	}
	sort.Strings(out)
	return out
}

// setPodTemplateAnnotation sets one annotation on the pod template at path,
// creating the maps it needs. A document whose template is missing is left
// alone: the apply will reject it with a better message than this could.
func setPodTemplateAnnotation(doc map[string]any, path []string, key, value string) {
	cur := doc
	for _, step := range path {
		next, ok := cur[step].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	md, _ := cur["metadata"].(map[string]any)
	if md == nil {
		md = map[string]any{}
		cur["metadata"] = md
	}
	ann, _ := md["annotations"].(map[string]any)
	if ann == nil {
		ann = map[string]any{}
		md["annotations"] = ann
	}
	ann[key] = value
}
