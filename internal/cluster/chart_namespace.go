package cluster

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Making a chart's rendered objects self-describing.
//
// `helm template -n <ns>` renders a chart FOR a namespace but does not oblige
// the chart to write it down: flux2 stamps 0 of its 40 objects. The deploy
// copes by passing `kubectl apply -n <ns>`, which leaves the render ambiguous
// — the same stream applied by hand with a different (or no) `-n` lands in the
// wrong namespace. stampChartNamespace writes the chart's namespace into every
// namespaced object whose scope forge can PROVE without a cluster, so the
// stream a render prints and the stream a deploy applies are one and the same
// and mean the same thing wherever they are applied.
//
// What it will not do is guess. A namespace written onto a cluster-scoped
// object is an apply error, and scope is not in the manifest: a chart's own
// CRDs define new kinds. So an object is stamped only when its kind is a known
// built-in namespaced kind, or its CRD is in hand and declares
// `spec.scope: Namespaced`. Anything else is left exactly as rendered, and the
// deploy's `-n` — which the apiserver resolves per object — stays the backstop
// for it. Cluster-scoped objects are never touched, so they carry no namespace.

// builtinNamespacedKinds are the namespaced kinds of the built-in API groups,
// keyed "<group>/<Kind>" (group "" is core).
var builtinNamespacedKinds = map[string]bool{
	"/ConfigMap": true, "/Endpoints": true, "/Event": true, "/LimitRange": true,
	"/PersistentVolumeClaim": true, "/Pod": true, "/PodTemplate": true,
	"/ReplicationController": true, "/ResourceQuota": true, "/Secret": true,
	"/Service": true, "/ServiceAccount": true,
	"apps/DaemonSet": true, "apps/Deployment": true, "apps/ReplicaSet": true, "apps/StatefulSet": true,
	"batch/CronJob": true, "batch/Job": true,
	"autoscaling/HorizontalPodAutoscaler": true,
	"policy/PodDisruptionBudget":          true,
	"networking.k8s.io/Ingress":           true, "networking.k8s.io/NetworkPolicy": true,
	"rbac.authorization.k8s.io/Role": true, "rbac.authorization.k8s.io/RoleBinding": true,
	"coordination.k8s.io/Lease":         true,
	"discovery.k8s.io/EndpointSlice":    true,
	"events.k8s.io/Event":               true,
	"storage.k8s.io/CSIStorageCapacity": true,
}

// chartKindKey is "<group>/<Kind>" for an apiVersion and kind.
func chartKindKey(apiVersion, kind string) string { return apiGroup(apiVersion) + "/" + kind }

// crdScopes maps "<group>/<Kind>" to whether that kind is namespaced, for every
// CustomResourceDefinition in the given streams.
func crdScopes(streams ...string) map[string]bool {
	out := map[string]bool{}
	for _, stream := range streams {
		for _, doc := range splitDocs(stream) {
			if !strings.Contains(doc, "CustomResourceDefinition") {
				continue
			}
			var crd struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Group string `yaml:"group"`
					Scope string `yaml:"scope"`
					Names struct {
						Kind string `yaml:"kind"`
					} `yaml:"names"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal([]byte(doc), &crd); err != nil || crd.Kind != "CustomResourceDefinition" {
				continue
			}
			if crd.Spec.Names.Kind == "" || (crd.Spec.Scope != "Namespaced" && crd.Spec.Scope != "Cluster") {
				continue
			}
			out[crd.Spec.Group+"/"+crd.Spec.Names.Kind] = crd.Spec.Scope == "Namespaced"
		}
	}
	return out
}

// stampChartNamespace writes namespace into metadata.namespace of every object
// in the `---` stream that is provably namespaced and names none. crds are the
// CRD streams whose scopes resolve custom kinds (the CRDs in manifests itself
// count too). An object that already names a namespace keeps it. An empty
// namespace returns the stream untouched.
func stampChartNamespace(manifests, namespace string, crds ...string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || strings.TrimSpace(manifests) == "" {
		return manifests
	}
	var scopes map[string]bool // resolved lazily: the CRD bundles are megabytes
	docs := splitDocs(manifests)
	changed := false
	for i, doc := range docs {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || m == nil {
			continue
		}
		kind, _ := m["kind"].(string)
		apiVersion, _ := m["apiVersion"].(string)
		meta, _ := m["metadata"].(map[string]any)
		if kind == "" || meta == nil {
			continue
		}
		if ns, _ := meta["namespace"].(string); ns != "" {
			continue
		}
		key := chartKindKey(apiVersion, kind)
		namespaced := builtinNamespacedKinds[key]
		if !namespaced {
			if scopes == nil {
				scopes = crdScopes(append([]string{manifests}, crds...)...)
			}
			namespaced = scopes[key]
		}
		if !namespaced {
			continue
		}
		meta["namespace"] = namespace
		b, err := yaml.Marshal(m)
		if err != nil {
			continue
		}
		docs[i] = strings.TrimRight(string(b), "\n")
		changed = true
	}
	if !changed {
		return manifests
	}
	return strings.Join(docs, docDelimiter)
}
