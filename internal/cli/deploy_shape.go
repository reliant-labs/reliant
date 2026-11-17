package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// deployShapeOf is doctor's DeployShaper: it classifies one environment's raw
// render the way `forge env deploy <env>` does, so `forge ci validate-kcl`
// and `forge doctor --signal deploy` judge an env by the path that actually
// ships it.
//
// Nothing here is a second opinion. The destinations are destinationOf's
// votes (the `forge env status` vocabulary), the hosted selection and its
// refusal are buildDeployGroups' own (buildHostedGroup), and admission is
// deploytarget.PreflightHosted — the deploy's plan with placeholder
// digests. A rule added to any of them reaches CI with no change here.
//
// Hosting is per workload: an env's hosted PART (its OnHosted workloads,
// hosted databases, OnHosted frontends) is admitted by
// deploytarget.PreflightHosted — Workload.Validate(ProfileRestricted) per
// workload plus a restricted render of the set — and rendered into the
// objects the platform runs, into a placeholder namespace (the platform
// allocates the real one). They carry no hosted decoration (runtime class,
// pull policy, routes): that is platform policy the author does not declare
// and cannot change. The env's cluster part is judged by doctor from the
// expanded manifest stream.
func deployShapeOf(env string, render []byte) (doctor.DeployShape, error) {
	entities, err := parseKCLEntities(render)
	if err != nil {
		return doctor.DeployShape{}, fmt.Errorf("read the deploy contract: %w", err)
	}
	if !rendersDeployContract(render) {
		// A hand-written render with no `output` contract: nothing to
		// classify from, so doctor keeps the strict manifest reading.
		return doctor.DeployShape{}, nil
	}
	shape := doctor.DeployShape{Destinations: destinationKindsOf(entities)}
	if !entities.HasHosted() {
		return shape, nil
	}
	shape.Hosted = true

	// Only the HOSTED group is judged here: the cluster part of a mixed env
	// is judged by doctor from the expanded manifest stream, exactly as it
	// is applied. buildHostedGroup is the deploy path's own selection, so a
	// hosted item with no control_plane is refused here as the deploy would.
	hosted, err := buildHostedGroup(env, entities)
	if err != nil {
		shape.Refusal = err
		return shape, nil
	}
	items, perr := deploytarget.PreflightHosted(*hosted)
	if perr != nil {
		shape.Refusal = perr
		return shape, nil
	}
	shape.Workloads = len(items)

	objects, err := platformObjectsOf(items)
	if err != nil {
		// Admitted by the plan and still unrenderable is exactly the
		// state the platform would discover on its own, one workload at a
		// time, after the publish succeeded.
		shape.Refusal = err
		return shape, nil
	}
	shape.PlatformObjects = objects
	return shape, nil
}

// rendersDeployContract reports whether a render carries forge's `output`
// deploy contract. parseKCLEntities answers an empty entity set for a render
// without one, which is indistinguishable from an env that declares nothing
// — and the two must be judged differently.
func rendersDeployContract(render []byte) bool {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(render, &root); err != nil {
		return false
	}
	out, ok := root["output"]
	return ok && len(out) > 0 && string(out) != "null"
}

// destinationKindsOf is every destination kind the env deploys to, sorted —
// the per-kind set destinationOf collapses into "mixed". doctor needs the
// set: "does anything here go to a cluster" is the question an empty
// manifest stream has to answer.
func destinationKindsOf(e *KCLEntities) []string {
	kinds := destinationKindSet(e)
	out := make([]string, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// platformObjectsOf renders the admitted hosted items into the Kubernetes
// objects the platform runs for them, as one JSON list: the Workloads as ONE
// set through pkg/deploy.RenderWorkloads under ProfileRestricted (the call
// the control plane's Workload operator makes), and each ManagedDatabase
// through pkg/deploy.Render.
//
// workloadURL env vars are resolved to a placeholder first, exactly as the
// operator resolves them before it renders. PreflightHosted already proved
// every reference resolvable; the value the platform allocates is not
// knowable here and nothing downstream reads it.
func platformObjectsOf(items []deploytarget.HostedPreflightItem) ([]byte, error) {
	var (
		objs []any
		errs []string
		set  []v1alpha1.Workload
	)
	for _, it := range items {
		switch {
		case it.Workload != nil:
			spec := *it.Workload
			env, err := deploy.ResolveEnvWorkloadURLs(spec.Env, func(name string) (string, error) {
				return "https://" + name + ".hosted.invalid", nil
			})
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", it.Name, err))
				continue
			}
			spec.Env = env
			w := v1alpha1.Workload{Spec: spec}
			w.Name = it.Name
			set = append(set, w)
		case it.Database != nil:
			db := &v1alpha1.ManagedDatabase{Spec: *it.Database}
			db.Name = it.Name
			rendered, err := deploy.Render(db, deploy.Context{Namespace: deploytarget.HostedPreflightNamespace})
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", it.Name, err))
				continue
			}
			for _, r := range rendered {
				objs = append(objs, r.Object)
			}
		default:
			// A StaticSite renders no Kubernetes objects: its executor is
			// the release planner, not an apply.
		}
	}
	if len(set) > 0 {
		rendered, err := deploy.RenderWorkloads(set, v1alpha1.ProfileRestricted, deploy.Context{Namespace: deploytarget.HostedPreflightNamespace})
		if err != nil {
			errs = append(errs, err.Error())
		}
		for _, r := range rendered {
			objs = append(objs, r.Object)
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("the platform could not render %d admitted workload(s):\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return json.Marshal(objs)
}
