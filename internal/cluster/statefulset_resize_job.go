package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/stsresize"
)

// The StatefulSet volume-resize pre-phase Job: how a declared volume size that
// GREW reaches a live StatefulSet when something other than forge applies it.
//
// A StatefulSet's volumeClaimTemplates are immutable, so raising the size an
// env declares makes the apply fail. Under forge's direct apply that failure is
// recovered after the fact (volume_resize_recovery.go). Under a reconciler
// there is no "after the fact": Flux dry-runs EVERY object of a Kustomization
// before it applies any, so one rejected StatefulSet stops the whole tree, and
// forge is not in the loop to repair it.
//
// So the render carries the repair. For each (cluster, namespace, delivery
// unit) that holds a StatefulSet with volumeClaimTemplates it emits a
// PRE-ROLLOUT Job, with the identity it runs as. The bundle orders a pre tree
// ahead of the rollout tree (release.ClassifyPhase, release.PlanTrees) and
// direct apply gates workloads on the same Jobs, so the one object gates both
// appliers, which is why it is a Job and not a controller or a hub-side call:
// every write it makes stays inside the identity the bundle itself declares.
//
// The Job (pkg/stsresize, Script) grows the claims of a StatefulSet whose
// template grew, then orphan-deletes the StatefulSet so the rollout creates it
// again with the new templates and adopts the running pods. It refuses a
// shrink, or any other change to the templates, and fails the pre tree on it.
// With nothing to do it is a no-op, and because its name carries a hash of the
// declared volumes it does not run again until one changes.
//
// WHEN IT IS NOT EMITTED: a local or ephemeral lifecycle, whose clusters are
// scratch that forge applies and the direct-apply recovery already converges
// (and whose k3d nodes may have no route to the Job's image); an env that
// opts out with Bundle.statefulset_resize.disabled; and a StatefulSet with no
// volumeClaimTemplates, which has nothing to resize.

// resizeJobOpts is the render-time input to [expandStatefulSetResizeJobs],
// decoded from `output.statefulset_resize` and `output.lifecycle`.
type resizeJobOpts struct {
	// Lifecycle is Bundle.lifecycle ("local", "ephemeral", or "").
	Lifecycle string
	// Disabled is Bundle.statefulset_resize.disabled.
	Disabled bool
	// Image is Bundle.statefulset_resize.image; "" is stsresize.DefaultImage.
	Image string
	// Namespace is the deploy namespace, for a StatefulSet that names none.
	Namespace string
	// ImagePullSecrets are the pull Secrets declared per (cluster, namespace)
	// group, as expandTierDeclarations reads them.
	ImagePullSecrets map[workloadGroupKey][]string
}

// enabled reports whether the env gets resize Jobs at all.
func (o resizeJobOpts) enabled() bool {
	return !o.Disabled && o.Lifecycle != "local" && o.Lifecycle != "ephemeral"
}

// resizeJobKey is one Job: the StatefulSets of a namespace on a cluster in a
// delivery unit. A unit gets its own because its Job gates THAT unit's rollout.
type resizeJobKey struct {
	cluster, namespace, unit string
}

// expandStatefulSetResizeJobs inserts, ahead of the first StatefulSet of each
// group, the objects of the Job that converges that group's volumes. It
// returns items unchanged when there is nothing to converge or the env does not
// get Jobs.
//
// Placed before the first StatefulSet, never after: the stream's order is the
// apply order within a phase, and a Namespace the group depends on already
// precedes the StatefulSet that lives in it.
func expandStatefulSetResizeJobs(items []any, opts resizeJobOpts) ([]any, error) {
	if !opts.enabled() {
		return items, nil
	}
	type group struct {
		first  int
		sets   []*appsv1.StatefulSet
		labels map[string]string
	}
	groups := map[resizeJobKey]*group{}
	var order []resizeJobKey
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok || m["kind"] != "StatefulSet" || m["apiVersion"] != "apps/v1" {
			continue
		}
		sts, err := decodeStatefulSet(m)
		if err != nil {
			return nil, fmt.Errorf("manifest item %d: %w", i, err)
		}
		if len(sts.Spec.VolumeClaimTemplates) == 0 {
			continue
		}
		if sts.Namespace == "" {
			sts.Namespace = opts.Namespace
		}
		if sts.Namespace == "" {
			// Nothing names the namespace this StatefulSet lands in, so a
			// namespace-scoped Job cannot be addressed to it. The apply's own
			// recovery still covers it.
			continue
		}
		key := resizeJobKey{
			cluster:   sts.Labels[ClusterRoutingLabel],
			namespace: sts.Namespace,
			unit:      sts.Labels[release.LabelDeliveryUnit],
		}
		g := groups[key]
		if g == nil {
			g = &group{first: i, labels: map[string]string{}}
			groups[key] = g
			order = append(order, key)
		}
		g.sets = append(g.sets, sts)
		if g.labels[deploy.LabelPartOf] == "" {
			g.labels[deploy.LabelPartOf] = sts.Labels[deploy.LabelPartOf]
		}
		if g.labels[deploy.LabelEnv] == "" {
			g.labels[deploy.LabelEnv] = sts.Labels[deploy.LabelEnv]
		}
	}
	if len(order) == 0 {
		return items, nil
	}

	emitted := map[int][]any{}
	for _, key := range order {
		g := groups[key]
		labels := map[string]string{}
		for k, v := range g.labels {
			if v != "" {
				labels[k] = v
			}
		}
		if key.cluster != "" {
			labels[ClusterRoutingLabel] = key.cluster
		}
		if key.unit != "" {
			labels[release.LabelDeliveryUnit] = key.unit
		}
		objs, err := stsresize.Build(stsresize.JobSpec{
			Namespace:        key.namespace,
			Unit:             key.unit,
			Image:            opts.Image,
			StatefulSets:     g.sets,
			Labels:           labels,
			ImagePullSecrets: opts.ImagePullSecrets[workloadGroupKey{cluster: key.cluster, namespace: key.namespace}],
		})
		if err != nil {
			return nil, fmt.Errorf("StatefulSet volume resize Job (cluster %q, namespace %s): %w", key.cluster, key.namespace, err)
		}
		docs, err := objs.Manifests()
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			emitted[g.first] = append(emitted[g.first], d)
		}
	}

	positions := make([]int, 0, len(emitted))
	for p := range emitted {
		positions = append(positions, p)
	}
	sort.Ints(positions)
	out := make([]any, 0, len(items)+len(emitted)*5)
	for i, it := range items {
		out = append(out, emitted[i]...)
		out = append(out, it)
	}
	return out, nil
}

// decodeStatefulSet reads a rendered StatefulSet document into the typed
// object. A document that does not decode is an error, not a skip: the Job's
// input would otherwise silently omit a StatefulSet the env declares.
func decodeStatefulSet(m map[string]any) (*appsv1.StatefulSet, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("StatefulSet does not encode: %w", err)
	}
	var sts appsv1.StatefulSet
	if err := json.Unmarshal(raw, &sts); err != nil {
		return nil, fmt.Errorf("StatefulSet %q does not decode: %w", fmt.Sprint(nestedName(m)), err)
	}
	return &sts, nil
}

func nestedName(m map[string]any) any {
	if md, ok := m["metadata"].(map[string]any); ok {
		return md["name"]
	}
	return nil
}

// decodeStatefulSetResize reads `output.statefulset_resize`: nil when the env
// declares none.
func decodeStatefulSetResize(raw any) (disabled bool, image string, err error) {
	if raw == nil {
		return false, "", nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return false, "", err
	}
	var v struct {
		Disabled bool   `json:"disabled"`
		Image    string `json:"image"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return false, "", fmt.Errorf("decode output.statefulset_resize: %w", err)
	}
	return v.Disabled, v.Image, nil
}

// resizeDefaultNamespace is the env's declared target namespace
// (`output.cluster_target.namespace`), the one a StatefulSet that names none
// lands in. "" when the env declares no target.
func resizeDefaultNamespace(out map[string]any) string {
	target, _ := out["cluster_target"].(map[string]any)
	ns, _ := target["namespace"].(string)
	return ns
}
