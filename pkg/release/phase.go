package release

// The deploy PHASE classifier: the one place that decides whether an object
// lands before the release's workloads, with them, or after them.
//
// Two appliers need the same answer. forge's direct apply
// (internal/cluster/prerollout.go) orders one cluster's stream by it, and a
// Flux-applied bundle expresses it as three trees chained by dependsOn
// (bundle_trees.go). If they classified differently, the pre-rollout gate
// would hold under `forge env deploy` and silently not hold under Flux: new
// pods would serve against an old schema, which is the incident the gate
// exists for. So both call [ClassifyPhase].
//
// THE RULES (unchanged from the direct apply they were lifted from):
//
//   - a standalone Job declares its phase with [AnnotationDeployPhase]; absent
//     means pre-rollout, and an unknown value is an ERROR (a typo'd
//     "pre-rolout" must not silently pick a side);
//   - a pre-rollout Job is [PhasePre], a post-rollout Job is [PhasePost];
//   - every kind that runs the release's code (Deployment, StatefulSet,
//     DaemonSet, ReplicaSet, ReplicationController, Pod, CronJob) is
//     [PhaseRollout];
//   - everything else (Namespaces, CRDs, Roles, ConfigMaps, Services,
//     NetworkPolicies, the ServiceAccount a Job's pod needs) is the SUPPORT a
//     pre-rollout Job rests on, so it is also [PhasePre]. Direct apply sends
//     the support objects first and then the Jobs; a tree sends them in one
//     SSA batch, where kustomize-controller applies Namespaces and CRDs ahead
//     of the rest on its own.

import "fmt"

// Phase places an object relative to the release's workloads.
type Phase string

const (
	// PhasePre is applied, and its Jobs must COMPLETE, before any workload.
	PhasePre Phase = "pre"
	// PhaseRollout is the workloads themselves.
	PhaseRollout Phase = "rollout"
	// PhasePost is applied with the workloads and awaited after their
	// rollout: a Job that needs this release's workloads serving.
	PhasePost Phase = "post"
)

// Phases is the closed set, in apply order.
var Phases = []Phase{PhasePre, PhaseRollout, PhasePost}

// Valid reports whether p is one of [Phases].
func (p Phase) Valid() bool {
	for _, k := range Phases {
		if p == k {
			return true
		}
	}
	return false
}

// AnnotationDeployPhase is the Job metadata annotation that places a one-shot
// Job relative to the workloads. It lives on the Job's metadata, never its pod
// template: the Job's name carries a hash of the pod spec, and declaring a
// phase must not rename (and so re-run) a Job that already completed.
const AnnotationDeployPhase = "forge.dev/deploy-phase"

// The values [AnnotationDeployPhase] accepts.
const (
	DeployPhasePreRollout  = "pre-rollout"
	DeployPhasePostRollout = "post-rollout"
)

// rolloutWorkloadKinds are the kinds that run the release's code: what a
// pre-rollout Job must precede. CronJob is here because a CronJob that fires
// between the apply and the migration runs new code against the old schema
// just as surely as a Deployment does.
var rolloutWorkloadKinds = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true,
	"ReplicationController": true, "Pod": true, "CronJob": true,
}

// RolloutWorkloadKind reports whether kind runs the release's code.
func RolloutWorkloadKind(kind string) bool { return rolloutWorkloadKinds[kind] }

// ClassifyPhase is the phase of one object, from its kind and its metadata
// annotations. name is used only in the error.
//
// An unknown [AnnotationDeployPhase] value is an error naming the Job and the
// accepted values; callers refuse before applying anything.
func ClassifyPhase(kind, name string, annotations map[string]string) (Phase, error) {
	if kind == "Job" {
		declared, ok := annotations[AnnotationDeployPhase]
		switch {
		case !ok, declared == DeployPhasePreRollout:
			return PhasePre, nil
		case declared == DeployPhasePostRollout:
			return PhasePost, nil
		default:
			return "", fmt.Errorf("one-shot Job %q declares %s=%q: want %q (the default: completes before any workload is applied) or %q (runs after the workloads roll out); nothing was applied",
				name, AnnotationDeployPhase, declared, DeployPhasePreRollout, DeployPhasePostRollout)
		}
	}
	if rolloutWorkloadKinds[kind] {
		return PhaseRollout, nil
	}
	return PhasePre, nil
}
