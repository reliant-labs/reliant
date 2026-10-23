package release

import (
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

func projectionMap(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}
func projectionString(m map[string]any, keys ...string) string {
	for len(keys) > 1 {
		m = projectionMap(m, keys[0])
		keys = keys[1:]
	}
	s, _ := m[keys[0]].(string)
	return s
}

// ObjectExternalIdentity is the shared planning projection of destructive
// external identity fields. Both normal bundle producers and ingest use it.
func ObjectExternalIdentity(kind string, body any) map[string]string {
	m, _ := body.(map[string]any)
	id := map[string]string{}
	put := func(k, v string) {
		if v != "" && id[k] == "" {
			id[k] = v
		}
	}
	switch kind {
	case "Service":
		sp := projectionMap(m, "spec")
		put(IdentityType, projectionString(sp, "type"))
		put(IdentityLoadBalancerIP, projectionString(sp, "loadBalancerIP"))
		put(IdentityLoadBalancerClass, projectionString(sp, "loadBalancerClass"))
	case "EnvoyProxy":
		put(IdentityLoadBalancerIP, projectionString(m, "spec", "provider", "kubernetes", "envoyService", "loadBalancerIP"))
	case "Ingress", "Gateway":
		ann := projectionMap(projectionMap(m, "metadata"), "annotations")
		for _, key := range []string{"kubernetes.io/ingress.global-static-ip-name", "kubernetes.io/ingress.regional-static-ip-name", "networking.gke.io/static-ip"} {
			put(IdentityStaticIP, projectionString(ann, key))
		}
	}
	if len(id) == 0 {
		return nil
	}
	return id
}

// ProjectSnapshotShape independently derives every planning object from exact
// approved resource bytes, with binding-derived immutable pins. No producer
// supplied hash, LB identity, image list or tree annotation is trusted.
func ProjectSnapshotShape(s Snapshot) (Shape, error) {
	if s.Declaration == nil {
		return Shape{}, snapshotBad("snapshot lacks recorded declaration")
	}
	shape := s.Declaration.Canonical()
	shape.Objects = nil
	stateful := map[string]bool{}
	for _, w := range s.StatefulWorkloads {
		stateful[w] = true
	}
	for _, r := range s.Resources {
		pins := map[string]string{}
		for _, binding := range s.Bindings {
			for _, owner := range r.Owners {
				if binding.Consumer == owner {
					a := s.Artifacts[binding.Artifact]
					if a.Kind == KindOCI {
						pins[owner.Cluster+"/"+owner.Workload+"/"+binding.Slot] = a.Content
					}
				}
			}
		}

		var body map[string]any
		if err := yaml.Unmarshal(r.Spec, &body); err != nil {
			return Shape{}, err
		}
		metadata := projectionMap(body, "metadata")
		labels := projectionMap(metadata, "labels")
		workload := projectionString(labels, "forge.dev/workload")
		if workload == "" {
			workload = projectionString(labels, "app.kubernetes.io/name")
		}
		hash, configHash, err := ObjectHashes(body, pins)
		if err != nil {
			return Shape{}, err
		}
		tree := ""
		if !FlatCluster(r.Key.Cluster) {
			tree = strings.TrimPrefix(r.Tree, BundleClusterPath(r.Key.Cluster)+"/")
		}
		shape.Objects = append(shape.Objects, ShapeObject{Cluster: r.Key.Cluster, APIVersion: projectionString(body, "apiVersion"), Kind: r.Key.Kind, Namespace: r.Key.Namespace, Name: r.Key.Name, Workload: workload, Hash: hash, ConfigHash: configHash, Images: ImagesIn(string(r.Spec), pins), Stateful: stateful[workload], Identity: ObjectExternalIdentity(r.Key.Kind, body), Tree: tree})
	}
	shape = shape.Canonical()
	if err := shape.Validate(); err != nil {
		return Shape{}, err
	}
	return shape, nil
}

// SnapshotDelivery derives full cluster indexes, readiness/dependency graph,
// timeouts and budgets from approved objects and delivery-unit declarations.
// These actuator semantics cannot be weakened without changing snapshot input.
func SnapshotDelivery(s Snapshot) ([]BundleClusterTree, []BundleTree, error) {
	counts := map[string]int{}
	var objects []TreeObject
	for _, r := range s.Resources {
		var body map[string]any
		if err := yaml.Unmarshal(r.Spec, &body); err != nil {
			return nil, nil, err
		}
		ann := projectionMap(projectionMap(body, "metadata"), "annotations")
		annotations := map[string]string{}
		for k, v := range ann {
			if text, ok := v.(string); ok {
				annotations[k] = text
			}
		}
		phase, err := ClassifyPhase(r.Key.Kind, r.Key.Name, annotations)
		if err != nil {
			return nil, nil, err
		}
		if string(phase) != r.Phase {
			return nil, nil, snapshotBad("resource phase differs from exact bytes")
		}
		counts[r.Key.Cluster]++
		if FlatCluster(r.Key.Cluster) {
			if r.Tree != BundleClusterPath(r.Key.Cluster) {
				return nil, nil, snapshotBad("flat destination tree mismatch")
			}
			continue
		}
		unit := path.Base(path.Dir(r.Tree))
		if r.Tree != BundleTreePath(r.Key.Cluster, unit, phase) {
			return nil, nil, snapshotBad("resource delivery placement mismatch")
		}
		labels := projectionMap(projectionMap(body, "metadata"), "labels")
		declared := projectionString(labels, LabelDeliveryUnit)
		if declared == "" {
			declared = DefaultDeliveryUnit
		}
		if unit != declared {
			return nil, nil, snapshotBad("resource tree differs from declared delivery unit")
		}
		spec := projectionMap(body, "spec")
		field := "progressDeadlineSeconds"
		if r.Key.Kind == "Job" {
			field = "activeDeadlineSeconds"
		}
		deadline := 0
		switch n := spec[field].(type) {
		case int:
			deadline = n
		case int64:
			deadline = int(n)
		case float64:
			deadline = int(n)
		}
		objects = append(objects, TreeObject{Cluster: r.Key.Cluster, Unit: unit, Phase: phase, Kind: r.Key.Kind, Namespace: r.Key.Namespace, Name: r.Key.Name, DeadlineSeconds: deadline})
	}
	var clusters []BundleClusterTree
	for cluster, count := range counts {
		clusters = append(clusters, BundleClusterTree{Cluster: cluster, Path: BundleClusterPath(cluster), Documents: count})
	}
	sortSet(clusters)
	trees, err := PlanTrees(objects, s.DeliveryUnits)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot delivery: %w", err)
	}
	return clusters, trees, nil
}
