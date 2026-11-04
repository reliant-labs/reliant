package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jinzhu/inflection"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/serverkit/drainbudget"
)

// RenderWorkloads renders an environment's whole workload SET into the
// Kubernetes objects that run it. It is the ONE Kubernetes renderer (ADR 0002
// §4): the CLI calls it for Cluster-bound workloads under ProfileFull, and
// the control plane's operator calls it for Hosted ones under
// ProfileRestricted.
//
// It takes the set rather than one workload because two of its policies are
// relations BETWEEN workloads: a job's `before` becomes an initContainer on
// the workloads it gates, and a `before` that names nothing is refused. A
// per-workload entry point could not see either.
//
// It is the port of kcl/workloads/expand.k (with capabilities.k, render.k and
// lib/{rbac,netpol,labels}.k), which it replaces. Every policy cites the KCL
// it came from. Where it deliberately differs, the comment says so, and
// .scratch/parity.md lists every difference.
//
// Per kind:
//
//	service   Deployment + Service (+ PDB, PVC) + ServiceAccount [+ RBAC] [+ NetworkPolicy]
//	worker    Deployment (+ PDB, PVC) + ServiceAccount [+ RBAC] [+ NetworkPolicy]
//	operator  Deployment [+ Service when it declares ports] (+ PDB, PVC) + ServiceAccount
//	          + Role + RoleBinding (config-read defaults, leases derived)
//	          + ClusterRole + ClusterRoleBinding (CRD rules derived)
//	job       standalone: Job + ServiceAccount [+ RBAC] [+ NetworkPolicy];
//	          with `before`: nothing of its own, an initContainer on each gated pod
//	cron      CronJob + ServiceAccount [+ RBAC] [+ NetworkPolicy]
//	tool      nothing (built into the image, never scheduled)
//
// [+ RBAC] is a Role + RoleBinding (the config-read defaults + namespacedRBAC)
// whenever either tier is declared, plus, under the Full profile, a
// ClusterRole + ClusterRoleBinding carrying ONLY clusterRBAC. Both bind the
// workload's one ServiceAccount (see identity).
// [+ NetworkPolicy] is the per-workload ingress policy: always under
// ProfileRestricted, and under ProfileFull only when Context.Network is set.
//
// Every spec is validated under p FIRST, all-or-nothing: no object is
// emitted for a set with any error, and every error is reported together.
//
// It is PURE and DETERMINISTIC. Same input, byte-identical output. Two
// executors apply its output against the same objects, and a renderer that
// disagreed with itself would make them fight forever.
//
// What it does NOT do, because it is hosted policy the operator composes
// around the result: RuntimeClass and node isolation, the registry
// allowlist, the image pull secret, the SecretRef customer-prefix rule,
// hostname routes, quota and the shape band. It also does not render the
// Namespace. The namespace belongs to the environment (hosted: to the
// platform), not to any workload in it.
func RenderWorkloads(ws []v1alpha1.Workload, p v1alpha1.Profile, ctx Context) ([]*unstructured.Unstructured, error) {
	if err := ctx.validate(); err != nil {
		return nil, err
	}
	set, err := prepareSet(ws, p)
	if err != nil {
		return nil, err
	}
	var objs []runtime.Object
	for _, w := range set.list {
		o, err := set.render(w, ctx)
		if err != nil {
			return nil, fmt.Errorf("workload %s: %w", w.name, err)
		}
		objs = append(objs, o...)
	}
	if ctx.Network != nil {
		objs = append(objs, envNetworkPolicies(ctx.Namespace, *ctx.Network)...)
	}
	out, err := toUnstructured(objs)
	if err != nil {
		return nil, err
	}
	stampEnv(out, ctx.Env)
	stampVersion(out, ctx.Version)
	if p == v1alpha1.ProfileRestricted {
		// Defense in depth behind Validate. The profile refuses every field
		// that would produce these, so this can only fire on a renderer bug.
		// That is exactly when it must fire, rather than hand a hosted
		// workload Kubernetes RBAC.
		for _, o := range out {
			if !RestrictedKinds[o.GetKind()] {
				return nil, fmt.Errorf("renderer produced %s/%s under the restricted profile, which may only emit %v: refusing the whole set", o.GetKind(), o.GetName(), sortedKindNames(RestrictedKinds))
			}
		}
	}
	return out, nil
}

// RestrictedKinds is every object kind RenderWorkloads may emit under
// ProfileRestricted: the objects that reach only the hosted user's own pods. No
// RBAC kind is in it, ever. A gating job is an initContainer inside a
// Deployment or Job, so it needs no kind of its own.
var RestrictedKinds = map[string]bool{
	"Deployment":            true,
	"Service":               true,
	"ServiceAccount":        true,
	"PersistentVolumeClaim": true,
	"NetworkPolicy":         true,
	"PodDisruptionBudget":   true,
	"Job":                   true,
}

func sortedKindNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PVCName is the claim backing a workload's storageGiB. The name is exported
// because an observer must read back the claim this package emitted.
func PVCName(workloadName string) string { return workloadName + "-data" }

// Defaults the renderer owns (the spec's own defaults are v1alpha1's).
const (
	// DefaultServicePort is the `http` port a service that declares none
	// listens on (kcl/workloads/expand.k:502, schema.k:154-155): serverkit's
	// standard mux.
	DefaultServicePort int32 = 8080
	// graceMarginSeconds is expand.k:252's margin above the app's drain.
	graceMarginSeconds = 5
	// Job lifecycle (expand.k:720-725). There is deliberately NO
	// ttlSecondsAfterFinished: a finished Job IS the record that this spec
	// already ran. Once the TTL controller deletes it, the next apply — a
	// GitOps reconciler, the hosted operator, or `forge env deploy` — sees the
	// Job missing and creates it again, re-running a migration every TTL
	// period. Superseded Jobs are removed by the spec-hash name plus prune,
	// never by a timer.
	jobBackoffLimit int32 = 6

	// Rollout settings a Deployment states explicitly (see the render below):
	// Kubernetes' own defaults. progressDeadlineSeconds is also what a
	// control plane derives a rollout's health budget from. They are spelled
	// here, and again in pkg/release for a Deployment a project hand-writes
	// into forge.Manifests; TestRolloutDefaultsAgree pins that the two agree.
	defaultRolloutPercent = "25%"
	// DefaultProgressDeadlineSeconds is the Deployment progressDeadlineSeconds
	// forge renders.
	DefaultProgressDeadlineSeconds int32 = 600
)

// gatedKinds are the kinds a job may gate: capabilities.k CAP_GATED
// (BROADCAST_GATED_KINDS, capabilities.k:273-275). cron is excluded because
// a CronJob pod fires on a SCHEDULE, so an initContainer there would re-run
// the one-shot on every tick (capabilities.k:188-190); tool has no pod.
var gatedKinds = map[v1alpha1.WorkloadKind]bool{
	v1alpha1.KindService: true, v1alpha1.KindWorker: true, v1alpha1.KindJob: true, v1alpha1.KindOperator: true,
}

// workload is one prepared entry: its name and its DEFAULTED spec.
type workload struct {
	name string
	kind v1alpha1.WorkloadKind
	spec v1alpha1.WorkloadSpec
}

func (w *workload) gatingJob() bool { return w.kind == v1alpha1.KindJob && len(w.spec.Before) > 0 }

func (w *workload) broadcast() bool { return slices.Contains(w.spec.Before, v1alpha1.BeforeAll) }

type workloadSet struct {
	list    []*workload
	byName  map[string]*workload
	profile v1alpha1.Profile
}

// prepareSet validates every spec under p and the cross-workload rules, and
// returns the defaulted set. All errors are collected (validate.go's
// all-or-nothing convention).
func prepareSet(ws []v1alpha1.Workload, p v1alpha1.Profile) (*workloadSet, error) {
	set := &workloadSet{byName: map[string]*workload{}, profile: p}
	var errs []error
	for i := range ws {
		name := ws[i].Name
		spec := ws[i].Spec
		fail := func(err error) { errs = append(errs, fmt.Errorf("workload %s: %w", name, err)) }
		if _, dup := set.byName[name]; dup {
			fail(errors.New("declared twice in this environment"))
			continue
		}
		// The admission entrypoint: the name rules (DNS label, a standalone
		// job's 52-character cap, sidecar names), then every spec rule
		// including the gating-job pod fields. Each is reported once, here.
		if err := ws[i].Validate(p); err != nil {
			fail(err)
		}
		// A workloadURL is a reference, and a pod can only carry a value.
		// Rendering it as an empty variable would start a workload whose
		// CORS_ORIGINS silently admits nothing. The caller resolves it
		// first (ResolveEnvWorkloadURLs), where the target's URL is known.
		for _, env := range append([][]v1alpha1.EnvVar{spec.Env}, sidecarEnvs(spec)...) {
			for _, e := range env {
				if e.WorkloadURL != nil {
					fail(fmt.Errorf("env var %s holds an unresolved workloadURL %q: resolve it (deploy.ResolveEnvWorkloadURLs) before rendering", e.Name, e.WorkloadURL.Name))
				}
			}
		}
		w := &workload{name: name, spec: spec.WithDefaults()}
		w.kind = w.spec.EffectiveKind()
		// A service that declares no ports serves the standard mux on
		// `http` :8080 (expand.k:502). NOT exposed: expand.k's default
		// carried expose=True, but expose now asks for a PUBLIC hostname
		// (Port.Expose), and a service that declared nothing must not
		// become public by omission.
		if w.kind == v1alpha1.KindService && len(w.spec.Ports) == 0 {
			w.spec.Ports = []v1alpha1.Port{{Name: v1alpha1.DefaultHTTPPortName, Port: DefaultServicePort, Protocol: v1alpha1.ProtocolTCP}}
		}
		set.byName[name] = w
		set.list = append(set.list, w)
	}
	errs = append(errs, set.validateBefore()...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return set, nil
}

func sidecarEnvs(s v1alpha1.WorkloadSpec) [][]v1alpha1.EnvVar {
	out := make([][]v1alpha1.EnvVar, 0, len(s.Sidecars))
	for _, c := range s.Sidecars {
		out = append(out, c.Env)
	}
	return out
}

// validateBefore ports kcl/workloads/render.k:147-162 (a `before` naming a
// workload that does not exist is refused, BEFORE_ALL exempt) and adds the
// checks the KCL could not make and silently dropped instead:
//
//   - a job naming ITSELF (schema.k:379);
//   - a job naming a kind that cannot be gated. expand.k:798 rendered cron
//     without gating and tool as nothing, so `before = ["nightly"]`
//     rendered, passed, and ordered nothing;
//   - a cycle between jobs, which is not an ordering.
func (set *workloadSet) validateBefore() []error {
	var errs []error
	names := make([]string, 0, len(set.list))
	for _, w := range set.list {
		names = append(names, w.name)
	}
	for _, j := range set.list {
		if j.kind != v1alpha1.KindJob {
			continue
		}
		for _, d := range j.spec.Before {
			if d == v1alpha1.BeforeAll {
				continue
			}
			t, ok := set.byName[d]
			switch {
			case d == j.name:
				errs = append(errs, fmt.Errorf("workload %s: before names the job itself", j.name))
			case !ok:
				errs = append(errs, fmt.Errorf("workload %s: before names %q, which does not exist in this environment (declared: %s); fix the name, use [%q] to gate every workload, or drop before to render a standalone Job", j.name, d, strings.Join(names, ", "), v1alpha1.BeforeAll))
			case !gatedKinds[t.kind]:
				errs = append(errs, fmt.Errorf("workload %s: before names %q, a %s, which cannot be gated: a cron fires on a schedule, so gating it would re-run the job on every tick, and a tool is never scheduled", j.name, d, t.kind))
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}
	// Cycle detection over job→job gating edges.
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var visit func(j *workload, path []string) error
	visit = func(j *workload, path []string) error {
		switch state[j.name] {
		case visiting:
			return fmt.Errorf("jobs gate each other in a cycle (%s -> %s), which is not an ordering", strings.Join(path, " -> "), j.name)
		case done:
			return nil
		}
		state[j.name] = visiting
		for _, t := range set.list {
			if t.kind == v1alpha1.KindJob && jobGates(j, t) {
				if err := visit(t, append(path, j.name)); err != nil {
					return err
				}
			}
		}
		state[j.name] = done
		return nil
	}
	for _, j := range set.list {
		if j.kind == v1alpha1.KindJob && state[j.name] == unvisited {
			if err := visit(j, nil); err != nil {
				return []error{err}
			}
		}
	}
	return nil
}

// jobGates ports expand.k:412-417 job_gates. An enumerated `before` gates
// exactly the named workloads. The broadcast form gates every workload of a
// gateable kind, except itself and another broadcast job: two broadcast jobs
// are peers, each claiming to precede the other, which is not an ordering.
func jobGates(j, target *workload) bool {
	if !j.gatingJob() {
		return false
	}
	if j.broadcast() {
		return target.name != j.name && gatedKinds[target.kind] &&
			!(target.kind == v1alpha1.KindJob && target.broadcast())
	}
	return slices.Contains(j.spec.Before, target.name)
}

// gatingJobs ports expand.k:429-431 _gating_jobs: the jobs that gate target,
// as initContainers. Declaration order is the ordering contract
// (expand.k:423-428): Kubernetes runs initContainers in list order, so a
// migration declared first runs first.
//
// One refinement over expand.k. When one gating job ALSO gates another
// (migrate broadcast-gates a seed job, and both gate `api`), the gate runs
// first whatever the declaration order. In expand.k a seed declared before
// migrate ran before it on every pod they both gated, which is the race
// `before` exists to remove.
func (set *workloadSet) gatingJobs(target *workload) []*workload {
	var gates []*workload
	for _, j := range set.list {
		if jobGates(j, target) {
			gates = append(gates, j)
		}
	}
	// Stable topological order: repeatedly take the first job (in
	// declaration order) none of whose remaining peers gate it. The set is
	// acyclic (validateBefore), so this always makes progress.
	ordered := make([]*workload, 0, len(gates))
	for len(gates) > 0 {
		for i, j := range gates {
			blocked := false
			for _, k := range gates {
				if k != j && jobGates(k, j) {
					blocked = true
					break
				}
			}
			if !blocked {
				ordered = append(ordered, j)
				gates = append(gates[:i], gates[i+1:]...)
				break
			}
		}
	}
	return ordered
}

// render dispatches one workload on its kind (expand.k:795-801).
func (set *workloadSet) render(w *workload, ctx Context) ([]runtime.Object, error) {
	switch w.kind {
	case v1alpha1.KindService, v1alpha1.KindWorker, v1alpha1.KindOperator:
		return set.renderLongRunning(w, ctx)
	case v1alpha1.KindJob:
		if w.gatingJob() {
			// expand.k:731: a job with `before` renders NO standalone Job.
			// Rendering both would run it twice, once unordered. It also
			// renders no identity: it runs under the gated pods' own, and
			// Validate refuses the pod-level fields on it.
			return nil, nil
		}
		return set.renderJob(w, ctx)
	case v1alpha1.KindCron:
		return set.renderCron(w, ctx), nil
	case v1alpha1.KindTool:
		// expand.k:782-784: built into the image and never scheduled.
		return nil, nil
	}
	return nil, fmt.Errorf("kind %q has no renderer", w.kind)
}

// renderLongRunning renders service / worker / operator (expand.k:440-533,
// :748-777): a Deployment, the service's Service, the PDB, the PVC, the
// identity and the ingress NetworkPolicy.
func (set *workloadSet) renderLongRunning(w *workload, ctx Context) ([]runtime.Object, error) {
	s := w.spec
	labels := managedLabels(w.name, ctx.PartOf)
	selector := map[string]string{LabelName: w.name}

	var extraEnv []corev1.EnvVar
	// expand.k:751: an operator's manager is told to elect a leader. An
	// explicitly declared LEADER_ELECTION wins, rather than rendering the
	// name twice (which the API server rejects as a duplicate).
	if s.EffectiveLeaderElection() && !declaresEnv(s.Env, "LEADER_ELECTION") {
		extraEnv = append(extraEnv, corev1.EnvVar{Name: "LEADER_ELECTION", Value: "true"})
	}
	pod := set.podSpec(w, ctx, extraEnv)
	if pod.TerminationGracePeriodSeconds == nil {
		grace, err := gracePeriodSeconds(s.Env)
		if err != nil {
			return nil, fmt.Errorf("workload %q: %w", w.name, err)
		}
		pod.TerminationGracePeriodSeconds = new(grace)
	}
	if s.Replicas > 1 {
		pod.TopologySpreadConstraints = spreadConstraints(w.name)
	}

	replicas := s.Replicas
	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(w.name, ctx.Namespace, labels),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: copyLabels(selector)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: copyLabels(labels), Annotations: s.PodAnnotations},
				Spec:       pod,
			},
		},
	}
	// A ReadWriteOnce volume is mounted by one pod at a time. A rolling
	// update starts the new pod while the old one still holds it, and
	// wedges. Validate guarantees one replica; Recreate is the rollout.
	//
	// An explicit strategy says the same thing for a workload whose replica
	// count is a correctness bound rather than a capacity choice. Validate
	// refuses RollingUpdate beside storage, so the two rules cannot
	// disagree. Recreate carries no rollingUpdate parameters — Kubernetes
	// refuses that combination.
	switch {
	case s.Strategy != "":
		dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.DeploymentStrategyType(s.Strategy)}
	case s.StorageGiB > 0:
		dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	default:
		dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
	}
	// STATED, NOT DEFAULTED. Kubernetes fills these in when they are absent,
	// and fills rollingUpdate under another field manager, which a reconciler
	// then sees as fields it does not own and cannot remove when the strategy
	// later becomes Recreate (internal/cluster/strategy_recovery.go repairs
	// that by hand for direct apply; a reconciler has no such repair). The
	// values are Kubernetes' own defaults, so nothing changes at runtime: the
	// render just says what the cluster would have said. The deadline is also
	// what a control plane derives a rollout's health budget from.
	if dep.Spec.Strategy.Type == appsv1.RollingUpdateDeploymentStrategyType {
		dep.Spec.Strategy.RollingUpdate = &appsv1.RollingUpdateDeployment{
			MaxSurge:       new(intstr.FromString(defaultRolloutPercent)),
			MaxUnavailable: new(intstr.FromString(defaultRolloutPercent)),
		}
	}
	dep.Spec.ProgressDeadlineSeconds = new(DefaultProgressDeadlineSeconds)
	objs := []runtime.Object{dep}

	// expand.k:507-515: a service is dialled by name. So is an operator
	// that declares ports: they are its webhook, metrics or API endpoint
	// (control-plane's workspace-controller serves :9191), and capabilities.k:
	// 232-240's "documentary only" left every such operator to hand-write a
	// Service that forge's renderer should own. A worker still gets none:
	// nothing resolves it by name.
	if w.kind == v1alpha1.KindService || (w.kind == v1alpha1.KindOperator && len(s.Ports) > 0) {
		svc := &corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: objectMeta(w.name, ctx.Namespace, labels),
			Spec:       corev1.ServiceSpec{Selector: copyLabels(selector)},
		}
		for _, p := range s.Ports {
			sp := corev1.ServicePort{Name: p.Name, Port: p.Port, TargetPort: intstr.FromInt32(p.Port), Protocol: k8sProtocol(p.Protocol)}
			if p.AppProtocol != "" {
				sp.AppProtocol = new(p.AppProtocol)
			}
			svc.Spec.Ports = append(svc.Spec.Ports, sp)
		}
		objs = append(objs, svc)
	}
	// expand.k:272-282: a PDB above one replica, so a drain takes one at a
	// time. Omitted at one replica, where it would be vacuous or block the
	// drain forever.
	if s.Replicas > 1 {
		objs = append(objs, &policyv1.PodDisruptionBudget{
			TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
			ObjectMeta: objectMeta(w.name+"-pdb", ctx.Namespace, labels),
			Spec: policyv1.PodDisruptionBudgetSpec{
				MaxUnavailable: new(intstr.FromInt32(1)),
				Selector:       &metav1.LabelSelector{MatchLabels: copyLabels(selector)},
			},
		})
	}
	if s.StorageGiB > 0 {
		objs = append(objs, &corev1.PersistentVolumeClaim{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
			ObjectMeta: objectMeta(PVCName(w.name), ctx.Namespace, labels),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: *resource.NewQuantity(int64(s.StorageGiB)<<30, resource.BinarySI),
				}},
			},
		})
	}
	objs = append(objs, identity(w, ctx)...)
	if np := set.ingressPolicy(w, ctx); np != nil {
		objs = append(objs, np)
	}
	return objs, nil
}

// renderJob renders a STANDALONE one-shot (expand.k:638-744).
func (set *workloadSet) renderJob(w *workload, ctx Context) ([]runtime.Object, error) {
	pod := set.podSpec(w, ctx, nil)
	// expand.k:629-632: OnFailure + backoffLimit retry a transient failure
	// (the database still starting) without retrying a broken command
	// forever.
	pod.RestartPolicy = corev1.RestartPolicyOnFailure

	// expand.k:665-698: a Job's spec.template is IMMUTABLE, so a fixed name
	// makes re-applying an unchanged Job fail at the END of a whole-manifest
	// apply, after every Deployment rolled. The spec hash in the NAME makes
	// an unchanged spec a true no-op and a changed one a new Job. It is
	// taken over the emitted pod spec, so it covers every field without a
	// hand-kept list, and it hashes the image as resolved.
	podMap, err := toMap(&pod)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(podMap) // encoding/json sorts map keys: stable
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])[:v1alpha1.StandaloneJobHashLength]

	labels := managedLabels(w.name, ctx.PartOf)
	jobLabels := copyLabels(labels)
	jobLabels[LabelJobName] = w.name
	jobLabels[LabelSpecHash] = hash
	podLabels := copyLabels(labels)
	podLabels[LabelJobName] = w.name

	meta := objectMeta(w.name+"-"+hash, ctx.Namespace, jobLabels)
	// expand.k:714-718: WHEN this Job runs relative to the deploy's
	// workloads. On metadata, so it never moves the hash.
	meta.Annotations = map[string]string{AnnotationDeployPhase: string(w.spec.EffectiveDeployPhase())}
	job := &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: meta,
		Spec: batchv1.JobSpec{
			BackoffLimit:          new(jobBackoffLimit),
			ActiveDeadlineSeconds: w.spec.ActiveDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: w.spec.PodAnnotations},
				Spec:       pod,
			},
		},
	}
	objs := []runtime.Object{job}
	objs = append(objs, identity(w, ctx)...)
	if np := set.ingressPolicy(w, ctx); np != nil {
		objs = append(objs, np)
	}
	return objs, nil
}

// renderCron renders a CronJob (expand.k:536-619). expand.k sets no
// concurrencyPolicy, startingDeadlineSeconds or history limits, and neither
// does this: Kubernetes' defaults (Allow, none, 3/1) apply. A cron is never
// gated (capabilities.k:188-190).
func (set *workloadSet) renderCron(w *workload, ctx Context) []runtime.Object {
	pod := set.podSpec(w, ctx, nil)
	pod.RestartPolicy = corev1.RestartPolicyOnFailure
	labels := managedLabels(w.name, ctx.PartOf)
	cj := &batchv1.CronJob{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
		ObjectMeta: objectMeta(w.name, ctx.Namespace, labels),
		Spec: batchv1.CronJobSpec{
			Schedule: w.spec.Schedule,
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				ActiveDeadlineSeconds: w.spec.ActiveDeadlineSeconds,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: copyLabels(labels), Annotations: w.spec.PodAnnotations},
					Spec:       pod,
				},
			}},
		},
	}
	objs := []runtime.Object{cj}
	objs = append(objs, identity(w, ctx)...)
	if np := set.ingressPolicy(w, ctx); np != nil {
		objs = append(objs, np)
	}
	return objs
}

// podSpec is the pod every kind shares: identity, hardening, the main
// container and its sidecars, the gating initContainers, volumes and
// placement.
func (set *workloadSet) podSpec(w *workload, ctx Context, extraEnv []corev1.EnvVar) corev1.PodSpec {
	s := w.spec
	main := mainContainer(w, extraEnv)
	containers := []corev1.Container{main}
	// Native sidecars lead the initContainers: they are started in order
	// and each must be "started" before the next begins, so a gating job
	// that dials the proxy finds it up.
	var inits []corev1.Container
	for _, sc := range s.Sidecars {
		c := sidecarContainer(sc, s.SecurityContext)
		if sc.Native {
			c.RestartPolicy = new(corev1.ContainerRestartPolicyAlways)
			inits = append(inits, c)
			continue
		}
		containers = append(containers, c)
	}

	// cron never receives gating (expand.k:791-793): jobGates excludes it
	// through gatedKinds, and validateBefore refuses naming one.
	for _, j := range set.gatingJobs(w) {
		// The init runs in THIS pod, so it takes this pod's identity.
		inits = append(inits, initContainer(j, s.SecurityContext))
	}

	volumes := []corev1.Volume{{Name: v1alpha1.TmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	if s.StorageGiB > 0 {
		volumes = append(volumes, corev1.Volume{Name: v1alpha1.DataVolumeName, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: PVCName(w.name)},
		}})
	}
	for _, v := range s.Volumes {
		volumes = append(volumes, corev1.Volume{Name: v.Name, VolumeSource: volumeSource(v.Source)})
	}

	pod := corev1.PodSpec{
		// Bind the workload's own ServiceAccount. Leaving it unset ran every
		// pod as the namespace `default` (expand.k:504-505, :553-558).
		ServiceAccountName: w.name,
		SecurityContext:    podSecurityContext(needsFSGroup(s), s.SecurityContext),
		InitContainers:     inits,
		Containers:         containers,
		Volumes:            volumes,
		NodeSelector:       s.NodeSelector,
		Tolerations:        tolerations(s.Tolerations),
		// Kubernetes injects <SERVICE>_PORT=tcp://ip:port (and _SERVICE_HOST
		// ...) for every Service in the namespace unless told not to. Those
		// names collide with typed config (TEMPORAL_PORT parsed as an int
		// CrashLooped a prod gateway), so the default is off; a workload
		// that reads them says so (EnableServiceLinks).
		EnableServiceLinks: new(s.EnableServiceLinks),
	}
	// Unset leaves the pod at the cluster's default priority; naming a class
	// the cluster does not have is refused at admission, so it is the
	// author's to apply there.
	if s.PriorityClassName != "" {
		pod.PriorityClassName = s.PriorityClassName
	}
	// An explicit grace period (Full only) wins over the drain-derived one a
	// long-running kind gets, and is the only one a batch pod gets.
	if s.TerminationGracePeriodSeconds != nil {
		pod.TerminationGracePeriodSeconds = new(int64(*s.TerminationGracePeriodSeconds))
	}
	if s.UsesGeneratedServiceAccount() {
		// An API token mounted into a process that never calls the API is
		// pure blast radius (lib/rbac.k:78-79). A workload gets one only
		// when it has RBAC to use it with.
		pod.AutomountServiceAccountToken = new(hasRBAC(w))
	} else {
		// The override names a ServiceAccount forge does not own. Its owner
		// decides whether its token mounts (the ServiceAccount's own
		// automountServiceAccountToken): forge cannot know whether that
		// identity calls the API, and a pod-level false would silently
		// disable an identity that does.
		pod.ServiceAccountName = s.ServiceAccount
		// The env's pull secrets cannot ride that ServiceAccount, so they
		// go on the pod, where Kubernetes unions them with the SA's own
		// (lib/services.k:487). See Context.ImagePullSecrets.
		pod.ImagePullSecrets = pullSecrets(ctx.ImagePullSecrets)
	}
	return pod
}

// mainContainer is the workload's own container (expand.k:454-466).
func mainContainer(w *workload, extraEnv []corev1.EnvVar) corev1.Container {
	s := w.spec
	c := corev1.Container{
		Name:            w.name,
		Image:           s.Image,
		ImagePullPolicy: pullPolicy(s.Image),
		Command:         s.Command,
		Args:            s.Args,
		Env:             append(renderEnv(s.Env), extraEnv...),
		Resources:       resourceRequirements(s.Resources),
		SecurityContext: containerSecurityContext(s.SecurityContext),
		VolumeMounts:    []corev1.VolumeMount{{Name: v1alpha1.TmpVolumeName, MountPath: v1alpha1.TmpMountPath}},
	}
	if len(c.Env) == 0 {
		c.Env = nil
	}
	c.Ports = containerPorts(s.Ports)
	probes := s.EffectiveProbes()
	// expand.k:445-452: a probed workload always declares the port it
	// answers on, even when nothing routes to it (a worker probed on
	// probes.port with no ports declared).
	if len(c.Ports) == 0 && probes != nil {
		c.Ports = []corev1.ContainerPort{{Name: v1alpha1.DefaultHTTPPortName, ContainerPort: probes.Port, Protocol: corev1.ProtocolTCP}}
	}
	c.ReadinessProbe, c.LivenessProbe = probePair(probes)
	c.StartupProbe = startupProbe(probes)
	if s.StorageGiB > 0 {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: v1alpha1.DataVolumeName, MountPath: v1alpha1.DataMountPath})
	}
	for _, v := range s.Volumes {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: v.Name, MountPath: v.MountPath, ReadOnly: v.ReadOnly})
	}
	return c
}

// LowerContainer lowers a Container to its Kubernetes form for a pod forge
// does not render itself (a raw Deployment in Bundle.manifests): image
// pull policy, command/args/env, ports, defaulted resources, the probes with
// forge's timings, and restartPolicy Always for a Native sidecar. It carries
// no securityContext and no /tmp mount, because the raw pod owns both. It is
// the SAME lowering sidecarContainer starts from, so the two cannot drift.
func LowerContainer(c v1alpha1.Container) corev1.Container {
	k := lowerContainer(c)
	if c.Native {
		k.RestartPolicy = new(corev1.ContainerRestartPolicyAlways)
	}
	return k
}

func lowerContainer(sc v1alpha1.Container) corev1.Container {
	c := corev1.Container{
		Name:            sc.Name,
		Image:           sc.Image,
		ImagePullPolicy: pullPolicy(sc.Image),
		Command:         sc.Command,
		Args:            sc.Args,
		Env:             renderEnv(sc.Env),
		Ports:           containerPorts(sc.Ports),
		Resources:       resourceRequirements(sc.Resources),
	}
	probes := sc.EffectiveProbes()
	c.ReadinessProbe, c.LivenessProbe = probePair(probes)
	c.StartupProbe = startupProbe(probes)
	return c
}

// sidecarContainer renders a sidecar with the SAME hardening as the main
// container. The type has no securityContext field, on purpose: a sidecar
// that could loosen it would make the main container's hardening a
// suggestion. It shares /tmp and nothing else (v1alpha1.Container's doc).
func sidecarContainer(sc v1alpha1.Container, podSec *v1alpha1.PodSecurity) corev1.Container {
	c := lowerContainer(sc)
	c.SecurityContext = containerSecurityContext(podSec)
	c.VolumeMounts = []corev1.VolumeMount{{Name: v1alpha1.TmpVolumeName, MountPath: v1alpha1.TmpMountPath}}
	return c
}

// initContainer lowers a gating job onto a dependent (expand.k:318-330).
//
// It runs with ITS OWN command, env and image. Two deliberate differences
// from expand.k:
//
//   - the image is the job's own spec.image. expand.k used the DEPENDENT's,
//     because every workload of a project shared one image name there; the
//     lowering now resolves each workload's image explicitly, and running a
//     job under an image it did not declare would be a guess.
//   - resources are the job's own (defaulted). expand.k used a fixed
//     100m/128Mi block so as not to INHERIT THE DEPENDENT's per-env block;
//     that intent holds. A job's own declaration is what it needs, and
//     Kubernetes charges a pod max(largest init request, sum of containers),
//     so it does not inflate a dependent that already requests as much.
//
// It mounts the pod's /tmp: the root filesystem is read-only here too, and
// a migration tool writing a temp file would otherwise fail.
func initContainer(j *workload, podSec *v1alpha1.PodSecurity) corev1.Container {
	return corev1.Container{
		Name:            j.name,
		Image:           j.spec.Image,
		ImagePullPolicy: pullPolicy(j.spec.Image),
		Command:         j.spec.Command,
		Args:            j.spec.Args,
		Env:             renderEnv(j.spec.Env),
		Resources:       resourceRequirements(j.spec.Resources),
		SecurityContext: containerSecurityContext(podSec),
		VolumeMounts:    []corev1.VolumeMount{{Name: v1alpha1.TmpVolumeName, MountPath: v1alpha1.TmpMountPath}},
	}
}

func containerPorts(ports []v1alpha1.Port) []corev1.ContainerPort {
	var out []corev1.ContainerPort
	for _, p := range ports {
		out = append(out, corev1.ContainerPort{Name: p.Name, ContainerPort: p.Port, Protocol: k8sProtocol(p.Protocol)})
	}
	return out
}

// k8sProtocol upper-cases the spec's lowercase authoring value.
func k8sProtocol(p v1alpha1.PortProtocol) corev1.Protocol {
	if p == "" {
		p = v1alpha1.DefaultPortProtocol
	}
	return corev1.Protocol(strings.ToUpper(string(p)))
}

// probePair renders the readiness and liveness probes from ONE resolved
// Probes (v1alpha1.WorkloadSpec.EffectiveProbes), as two separate objects:
// readiness gates traffic and never restarts, liveness restarts on timings
// DERIVED from the readiness budget (expand.k:198-237). The arithmetic lives
// in v1alpha1 (Readiness / Liveness) and is not restated here.
func probePair(p *v1alpha1.Probes) (readiness, liveness *corev1.Probe) {
	if p == nil {
		return nil, nil
	}
	port := intstr.FromInt32(p.Port)
	handler := func(path string) corev1.ProbeHandler {
		if p.TCP {
			return corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: port}}
		}
		return corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: port}}
	}
	probe := func(h corev1.ProbeHandler, t v1alpha1.ProbeTimings) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler:        h,
			InitialDelaySeconds: t.InitialDelaySeconds,
			PeriodSeconds:       t.PeriodSeconds,
			TimeoutSeconds:      t.TimeoutSeconds,
			FailureThreshold:    t.FailureThreshold,
		}
	}
	return probe(handler(p.ReadinessPath), p.Readiness()), probe(handler(p.LivenessPath), p.Liveness())
}

// startupProbe renders Probes.StartupPath, or nil when none is declared.
func startupProbe(p *v1alpha1.Probes) *corev1.Probe {
	if p == nil || p.StartupPath == "" {
		return nil
	}
	return &corev1.Probe{
		ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: p.StartupPath, Port: intstr.FromInt32(p.Port)}},
		PeriodSeconds:    v1alpha1.DefaultStartupPeriodSeconds,
		TimeoutSeconds:   p.TimeoutSeconds,
		FailureThreshold: v1alpha1.DefaultStartupFailureThreshold,
	}
}

// renderEnv projects every channel (expand.k:104-116). Exactly one source is
// ever emitted per entry: Validate refuses two, and Kubernetes rejects
// value+valueFrom only server-side, after a client dry-run passed.
// WorkloadURL never reaches here (prepareSet refuses it unresolved).
func renderEnv(env []v1alpha1.EnvVar) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}
	out := make([]corev1.EnvVar, 0, len(env))
	for _, e := range env {
		ev := corev1.EnvVar{Name: e.Name}
		switch {
		case e.SecretRef != nil:
			ev.ValueFrom = secretKey(e.SecretRef.Name, e.SecretRef.Key)
			// Only true is stated: `optional: false` is the API default and
			// would add a line to every required reference for nothing.
			if e.SecretRef.Optional {
				ev.ValueFrom.SecretKeyRef.Optional = new(true)
			}
		case e.ManagedSecret != nil:
			ev.ValueFrom = secretKey(v1alpha1.ManagedSecretsSecretName, e.ManagedSecret.Name)
			// The materializer skips an optional name the store lacks, so
			// its key is absent from the Secret: the ref must tolerate that
			// or the pod would hold in CreateContainerConfigError.
			if e.ManagedSecret.Optional {
				ev.ValueFrom.SecretKeyRef.Optional = new(true)
			}
		case e.DatabaseRef != nil:
			ev.ValueFrom = secretKey(DatabaseCredentialSecretName(e.DatabaseRef.Name), string(e.DatabaseRef.EffectiveKey()))
		case e.ConfigMapRef != nil:
			ev.ValueFrom = &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: e.ConfigMapRef.Name}, Key: e.ConfigMapRef.Key,
			}}
		case e.FieldRef != nil:
			ev.ValueFrom = &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: e.FieldRef.FieldPath}}
		default:
			ev.Value = e.Value
		}
		out = append(out, ev)
	}
	return out
}

func secretKey(name, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key,
	}}
}

func declaresEnv(env []v1alpha1.EnvVar, name string) bool {
	return slices.ContainsFunc(env, func(e v1alpha1.EnvVar) bool { return e.Name == name })
}

// envLiteral ports expand.k:149-154 _env_literal: the last literal value of
// key in the workload's env, else fallback. A key bound to a reference has no
// literal to read.
func envLiteral(env []v1alpha1.EnvVar, key, fallback string) string {
	v := ""
	for _, e := range env {
		if e.Name == key && e.Value != "" {
			v = e.Value
		}
	}
	if v == "" {
		return fallback
	}
	return v
}

// drainSeconds is one drain knob's share of the grace period, in whole
// seconds rounded up so the grace period never undercuts the server. It reads
// the value with drainbudget — the reader the server's drain uses too: a Go
// duration ("30s", "1m30s", "500ms"), unset or zero is the default, negative
// is none. Anything else (a bare number, an interpolation, a typo) is an
// error: the server's config loader refuses it at boot, so rendering a grace
// period for it would ship a pod that crash-loops on a budget nobody got.
func drainSeconds(raw string, def time.Duration) (int64, error) {
	d, err := drainbudget.Parse(raw)
	if err != nil {
		return 0, err
	}
	d = drainbudget.Effective(d, def)
	return int64((d + time.Second - 1) / time.Second), nil
}

// gracePeriodSeconds ports expand.k:239-253 _grace_period. On SIGTERM
// serverkit flips /readyz, sleeps PRE_STOP_DELAY, then shuts down bounded by
// SHUTDOWN_TIMEOUT. Its defaults (5s + 30s) exceed Kubernetes' implicit 30s,
// so without this the kubelet SIGKILLs mid-drain on every rollout. Derived
// from the workload's OWN env, so retuning either knob cannot reintroduce
// the truncation. No preStop hook: the app owns the drain.
func gracePeriodSeconds(env []v1alpha1.EnvVar) (int64, error) {
	pre, err := drainSeconds(envLiteral(env, drainbudget.EnvPreStopDelay, ""), drainbudget.DefaultPreStopDelay)
	if err != nil {
		return 0, fmt.Errorf("env %s: %w", drainbudget.EnvPreStopDelay, err)
	}
	shutdown, err := drainSeconds(envLiteral(env, drainbudget.EnvShutdownTimeout, ""), drainbudget.DefaultShutdownTimeout)
	if err != nil {
		return 0, fmt.Errorf("env %s: %w", drainbudget.EnvShutdownTimeout, err)
	}
	return pre + shutdown + graceMarginSeconds, nil
}

// spreadConstraints ports expand.k:255-266: soft (ScheduleAnyway), because a
// hard constraint leaves replicas Pending forever on a one-node dev cluster.
func spreadConstraints(name string) []corev1.TopologySpreadConstraint {
	return []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       "kubernetes.io/hostname",
		WhenUnsatisfiable: corev1.ScheduleAnyway,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{LabelName: name}},
	}}
}

// pullPolicy ports expand.k:73-79: Always for a mutable tag (`latest`, no
// tag, or a `-dirty` dev build), else IfNotPresent.
func pullPolicy(image string) corev1.PullPolicy {
	tail := image[strings.LastIndex(image, "/")+1:]
	tag := "latest"
	if i := strings.LastIndex(tail, ":"); i >= 0 {
		tag = tail[i+1:]
	}
	if strings.HasSuffix(tag, "-dirty") || tag == "latest" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

// resourceRequirements formats neutral millicores/bytes as Kubernetes
// quantities (expand.k:83-88 via lib/quantity.k). Defaults are v1alpha1's
// 250m / 1 GiB entry shape, limits equal to requests.
func resourceRequirements(r v1alpha1.Resources) corev1.ResourceRequirements {
	r = r.WithDefaults()
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(r.CPURequestMillicores, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(r.MemoryRequestBytes, resource.BinarySI),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(r.CPULimitMillicores, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(r.MemoryLimitBytes, resource.BinarySI),
		},
	}
}

// podSecurityContext ports expand.k:45-50 plus fsGroup. Restricted Pod
// Security: non-root, seccomp RuntimeDefault. fsGroup makes a mounted
// PersistentVolumeClaim group-writable by 65532; without it most
// provisioners hand the non-root container a root-owned volume.
//
// o is the workload's PodSecurity override (Full only): it replaces uid, gid
// and fsGroup, each independently. An explicit FSGroup is always set. It
// never touches runAsNonRoot or seccomp, so the pod stays Pod Security
// `restricted` (Validate refuses a zero id).
func podSecurityContext(fsGroup bool, o *v1alpha1.PodSecurity) *corev1.PodSecurityContext {
	uid, gid := RunAsUser, RunAsUser
	var fs *int64
	if fsGroup {
		fs = new(RunAsUser)
	}
	if o != nil {
		if o.RunAsUser != nil {
			uid = *o.RunAsUser
		}
		if o.RunAsGroup != nil {
			gid = *o.RunAsGroup
		}
		if o.FSGroup != nil {
			fs = new(*o.FSGroup)
		}
	}
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   new(true),
		RunAsUser:      &uid,
		RunAsGroup:     &gid,
		FSGroup:        fs,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// needsFSGroup: the pod mounts a PVC, forge's own (storageGiB) or a declared
// one.
func needsFSGroup(s v1alpha1.WorkloadSpec) bool {
	return s.StorageGiB > 0 || slices.ContainsFunc(s.Volumes, func(v v1alpha1.Volume) bool { return v.Source.PVC != nil })
}

// containerSecurityContext ports expand.k:52-59: every container forge
// renders (main, sidecar, init) gets it, unconditionally. The pod's
// PodSecurity override (uid, gid, a writable root filesystem) applies to
// every container alike, so the pod has one identity. No escalation, drop
// ALL and runAsNonRoot are never overridable.
func containerSecurityContext(o *v1alpha1.PodSecurity) *corev1.SecurityContext {
	uid, gid, ro := RunAsUser, RunAsUser, true
	if o != nil {
		if o.RunAsUser != nil {
			uid = *o.RunAsUser
		}
		if o.RunAsGroup != nil {
			gid = *o.RunAsGroup
		}
		if o.ReadOnlyRootFilesystem != nil {
			ro = *o.ReadOnlyRootFilesystem
		}
	}
	return &corev1.SecurityContext{
		RunAsNonRoot:             new(true),
		RunAsUser:                &uid,
		RunAsGroup:               &gid,
		ReadOnlyRootFilesystem:   &ro,
		AllowPrivilegeEscalation: new(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// optionalPtr renders an unset optional as absent, so a Secret volume that
// never asked for it renders byte-identically to before the field existed.
func optionalPtr(optional bool) *bool {
	if !optional {
		return nil
	}
	return &optional
}

func volumeSource(src v1alpha1.VolumeSource) corev1.VolumeSource {
	items := func(in []v1alpha1.KeyToPath) []corev1.KeyToPath {
		var out []corev1.KeyToPath
		for _, it := range in {
			out = append(out, corev1.KeyToPath{Key: it.Key, Path: it.Path})
		}
		return out
	}
	switch {
	case src.Secret != nil:
		return corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: src.Secret.Name, Items: items(src.Secret.Items), Optional: optionalPtr(src.Secret.Optional)}}
	case src.ConfigMap != nil:
		return corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: src.ConfigMap.Name}, Items: items(src.ConfigMap.Items),
		}}
	case src.PVC != nil:
		return corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: src.PVC.ClaimName}}
	default:
		ed := &corev1.EmptyDirVolumeSource{}
		if src.EmptyDir != nil && src.EmptyDir.SizeLimitBytes > 0 {
			ed.SizeLimit = resource.NewQuantity(src.EmptyDir.SizeLimitBytes, resource.BinarySI)
		}
		return corev1.VolumeSource{EmptyDir: ed}
	}
}

func tolerations(in []v1alpha1.Toleration) []corev1.Toleration {
	var out []corev1.Toleration
	for _, t := range in {
		out = append(out, corev1.Toleration{
			Key: t.Key, Operator: corev1.TolerationOperator(t.Operator), Value: t.Value,
			Effect: corev1.TaintEffect(t.Effect), TolerationSeconds: t.TolerationSeconds,
		})
	}
	return out
}

// defaultRBACRules are lib/rbac.k:35-41: read the namespace's ConfigMaps and
// Secrets. Granted in every namespaced Role forge renders, never to a
// workload that asked for no RBAC, and NEVER in a ClusterRole: there it is
// read access to every Secret in the cluster. (lib/rbac.k put them in an
// operator's ClusterRole; that was the same hole.)
var defaultRBACRules = []rbacv1.PolicyRule{{
	APIGroups: []string{""}, Resources: []string{"configmaps", "secrets"}, Verbs: []string{"get", "list", "watch"},
}}

// hasRBAC ports expand.k:370-380: an operator always has RBAC (a manager
// cannot start without the config-read defaults and its lease); any other
// kind only when it declares rules in either tier. A workload with RBAC gets
// its namespaced Role and its token mounted.
func hasRBAC(w *workload) bool {
	return w.kind == v1alpha1.KindOperator || len(w.spec.ClusterRBAC) > 0 || len(w.spec.NamespacedRBAC) > 0
}

// identity ports expand.k:381-394 _workload_rbac with lib/rbac.k: exactly ONE
// ServiceAccount per workload, and up to two bindings on it, split by SCOPE:
//
//   - a Role + RoleBinding in the workload's own namespace, whenever it has
//     any RBAC: the config-read defaults, an operator's leader-election
//     lease, and namespacedRBAC;
//   - a ClusterRole + ClusterRoleBinding only for cluster-scoped intent: an
//     operator's CRD rules and clusterRBAC. Nothing namespace-local is
//     widened into it.
//
// The tiers used to be exclusive, with the ClusterRole REPLACING the Role
// and so carrying the defaults cluster-wide: every operator and every
// workload with clusterRBAC could read every Secret in the cluster. Two
// bindings on one ServiceAccount are the union of their rules, and each
// object states exactly the part of that union at its scope, so neither
// misdescribes what is in force.
//
// With a ServiceAccount override forge renders nothing: it does not own an
// identity it was told already exists, and a copy without the real one's
// annotations would strip them on apply (lib/rbac.k:125-131).
func identity(w *workload, ctx Context) []runtime.Object {
	s := w.spec
	if !s.UsesGeneratedServiceAccount() {
		return nil
	}
	labels := managedLabels(w.name, ctx.PartOf)
	sa := &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: objectMeta(w.name, ctx.Namespace, labels),
	}
	// Workload identity (iam.gke.io/gcp-service-account) is read off the
	// SA the pod runs as, so the annotations go HERE and on nothing else
	// (lib/rbac.k:43-67).
	if len(s.ServiceAccountAnnotations) > 0 {
		sa.Annotations = s.ServiceAccountAnnotations
	}
	// The target's image_pull_secrets ride the SA, so every pod, Job and
	// CronJob bound to it pulls with them (lib/rbac.k:90-98, :159-160).
	// These are CREDENTIALS, which are per-cluster; the registry itself is
	// named by each image, so one SA's secrets can cover several registries.
	sa.ImagePullSecrets = pullSecrets(ctx.ImagePullSecrets)
	if !hasRBAC(w) {
		// lib/rbac.k:69-99: identity without permission, and no token.
		sa.AutomountServiceAccountToken = new(false)
		return []runtime.Object{sa}
	}
	subject := []rbacv1.Subject{{Kind: "ServiceAccount", Name: w.name, Namespace: ctx.Namespace}}
	// lib/rbac.k:149-191.
	roleName := w.name + "-role"
	objs := []runtime.Object{
		sa,
		&rbacv1.Role{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
			ObjectMeta: objectMeta(roleName, ctx.Namespace, labels),
			Rules:      namespacedRules(s),
		},
		&rbacv1.RoleBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
			ObjectMeta: objectMeta(w.name+"-rolebinding", ctx.Namespace, labels),
			Subjects:   subject,
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: roleName},
		},
	}
	clusterRules := clusterRules(s)
	if len(clusterRules) == 0 {
		// An operator with no Group derives no CRD rules; with no declared
		// clusterRBAC either, a ClusterRole would bind nothing.
		return objs
	}
	// lib/rbac.k:230-267. Cluster-scoped names carry the NAMESPACE: with an
	// env-invariant name every env in a shared cluster writes the same
	// binding, and the last deploy silently repoints it at its own
	// namespace (lib/rbac.k:198-222).
	clusterRoleName := fmt.Sprintf("%s-%s-clusterrole", w.name, ctx.Namespace)
	return append(objs,
		&rbacv1.ClusterRole{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
			ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName, Labels: copyLabels(labels)},
			Rules:      clusterRules,
		},
		&rbacv1.ClusterRoleBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-clusterrolebinding", w.name, ctx.Namespace), Labels: copyLabels(labels)},
			Subjects:   subject,
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: clusterRoleName},
		},
	)
}

// crdVerbs / crdStatusVerbs / crdFinalizerVerbs are what the scaffolded
// controller's kubebuilder markers grant (internal/templates/crd/
// controller.go.tmpl:56-58), so a forge operator's ClusterRole and its code
// agree without either restating the other.
var (
	crdVerbs          = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	crdStatusVerbs    = []string{"get", "update", "patch"}
	crdFinalizerVerbs = []string{"update"}
	leaseVerbs        = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
)

// namespacedRules is the workload's Role: the config-read defaults, then an
// operator's coordination.k8s.io leases when it elects a leader, then its
// declared NamespacedRBAC.
//
// The lease is namespace-local. The scaffolded manager (operatorkit.Run)
// leaves LeaderElectionNamespace empty in-cluster, so controller-runtime
// takes the lease in the namespace its ServiceAccount token names — this
// workload's own. A non-empty namespace is only the opt-in for a manager run
// as a HOST process, which has no rendered ServiceAccount to grant anything
// to. Granting leases cluster-wide would add nothing but the power to take
// over every other controller's lease.
func namespacedRules(s v1alpha1.WorkloadSpec) []rbacv1.PolicyRule {
	rules := slices.Clone(defaultRBACRules)
	if s.EffectiveLeaderElection() {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: leaseVerbs})
	}
	return append(rules, policyRules(s.NamespacedRBAC)...)
}

// clusterRules is the workload's ClusterRole: cluster-scoped intent only —
// the rules an operator's manager needs on the CRDs it declares (the
// resource, /status and /finalizers in Group), then the declared
// ClusterRBAC. Empty means no ClusterRole is rendered.
//
// expand.k derived no CRD rules (it passed only cluster_rbac), so an
// operator declaring crds, but not restating them as rules, rendered a
// ClusterRole that could not watch its own resources, and the manager failed
// its first list with "forbidden". Without a Group the CRDs are not
// addressable, so nothing is derived; declared rules still apply.
func clusterRules(s v1alpha1.WorkloadSpec) []rbacv1.PolicyRule {
	var rules []rbacv1.PolicyRule
	if s.EffectiveKind() == v1alpha1.KindOperator && s.Group != "" && len(s.CRDs) > 0 {
		var res, status, finalizers []string
		for _, kind := range s.CRDs {
			plural := crdPlural(kind)
			res = append(res, plural)
			status = append(status, plural+"/status")
			finalizers = append(finalizers, plural+"/finalizers")
		}
		group := []string{s.Group}
		rules = append(rules,
			rbacv1.PolicyRule{APIGroups: group, Resources: res, Verbs: crdVerbs},
			rbacv1.PolicyRule{APIGroups: group, Resources: status, Verbs: crdStatusVerbs},
			rbacv1.PolicyRule{APIGroups: group, Resources: finalizers, Verbs: crdFinalizerVerbs},
		)
	}
	return append(rules, policyRules(s.ClusterRBAC)...)
}

// crdPlural is the lowercase plural resource name of a CRD kind, derived
// exactly as forge's CRD scaffold names it (internal/generator/crd_gen.go:
// strings.ToLower(naming.Pluralize(kind)), i.e. jinzhu/inflection). A
// divergent pluralizer would grant access to a resource the CRD does not
// define.
func crdPlural(kind string) string { return strings.ToLower(inflection.Plural(kind)) }

func pullSecrets(names []string) []corev1.LocalObjectReference {
	var out []corev1.LocalObjectReference
	for _, n := range names {
		out = append(out, corev1.LocalObjectReference{Name: n})
	}
	return out
}

func policyRules(in []v1alpha1.PolicyRule) []rbacv1.PolicyRule {
	var out []rbacv1.PolicyRule
	for _, r := range in {
		out = append(out, rbacv1.PolicyRule{APIGroups: r.APIGroups, Resources: r.Resources, Verbs: r.Verbs, ResourceNames: r.ResourceNames})
	}
	return out
}

// ingressPolicy is the per-workload ingress NetworkPolicy (ported from the
// retired RenderSimpleBackend; expand.k had none). It selects the workload's
// own pods and allows:
//
//   - its declared ports from every pod in the SAME namespace (an empty
//     podSelector peer with no namespaceSelector);
//   - its exposed port (Port.Expose) from anywhere, since a gateway in
//     another namespace, or the platform's router, delivers the public
//     traffic.
//
// Everything else is denied ingress. A batch pod or a worker with no ports
// gets the policy with no rules at all: ingress default-deny. Egress is
// deliberately unrestricted here. It is the namespace's control (Context.
// Network, or the control plane's per-customer rules).
//
// An operator gets NONE. Its inbound traffic is the API server calling its
// webhook and a monitoring stack in another namespace scraping metrics,
// neither of which a namespace peer can express, and a policy that selects
// its pods would deny both. Its isolation comes from the env-wide bundle.
//
// WHEN IT IS EMITTED. Under ProfileRestricted, always: it is the hosted
// boundary, and the platform relies on it. Under ProfileFull only when the
// environment opted into network policy (Context.Network != nil). A cluster
// the author operates had no per-workload policy under the KCL renderer, and
// one appearing unasked would cut off every caller the author has not
// modelled (a gateway in another namespace dialling a non-exposed port, a
// cross-namespace scraper), which is a silent outage on upgrade rather than
// a hardening.
func (set *workloadSet) ingressPolicy(w *workload, ctx Context) runtime.Object {
	if w.kind == v1alpha1.KindOperator {
		return nil
	}
	if set.profile != v1alpha1.ProfileRestricted && ctx.Network == nil {
		return nil
	}
	np := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: objectMeta(w.name+"-ingress", ctx.Namespace, managedLabels(w.name, ctx.PartOf)),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelName: w.name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	// Batch kinds declare no ports (Validate), so they get no rules.
	var private, public []networkingv1.NetworkPolicyPort
	for _, p := range w.spec.Ports {
		proto := k8sProtocol(p.Protocol)
		port := intstr.FromInt32(p.Port)
		npp := networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &port}
		if p.Expose {
			public = append(public, npp)
		} else {
			private = append(private, npp)
		}
	}
	if len(private) > 0 {
		np.Spec.Ingress = append(np.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{
			Ports: private,
			From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
		})
	}
	if len(public) > 0 {
		np.Spec.Ingress = append(np.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{Ports: public})
	}
	return np
}

// envNetworkPolicies ports kcl/lib/netpol.k:53-122 render_network_policies:
// namespace default-deny (ingress AND egress) plus the allow-list that keeps
// an env's own DNS, pods, external dependencies, collector and gateway
// reachable. See Context.Network for why this is an env-scope input.
func envNetworkPolicies(namespace string, n EnvNetworkPolicy) []runtime.Object {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	port := func(p int32, proto *corev1.Protocol) networkingv1.NetworkPolicyPort {
		v := intstr.FromInt32(p)
		return networkingv1.NetworkPolicyPort{Protocol: proto, Port: &v}
	}
	nsPeer := func(ns string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}}
	}
	policy := func(name string, spec networkingv1.NetworkPolicySpec) *networkingv1.NetworkPolicy {
		return &networkingv1.NetworkPolicy{
			TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       spec,
		}
	}
	ingress, egress := networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress
	out := []runtime.Object{
		policy("default-deny", networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{ingress, egress}}),
		policy("allow-dns", networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{egress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{nsPeer("kube-system")},
				Ports: []networkingv1.NetworkPolicyPort{port(53, &udp), port(53, &tcp)},
			}},
		}),
		policy("allow-internal", networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{ingress, egress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}},
		}),
	}
	if len(n.EgressPorts) > 0 {
		var ports []networkingv1.NetworkPolicyPort
		for _, p := range n.EgressPorts {
			ports = append(ports, port(p, &tcp))
		}
		out = append(out, policy("allow-egress-external", networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{egress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}}},
				Ports: ports,
			}},
		}))
	}
	if n.TelemetryNamespace != "" {
		out = append(out, policy("allow-egress-telemetry", networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{egress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{nsPeer(n.TelemetryNamespace)},
				Ports: []networkingv1.NetworkPolicyPort{port(4317, &tcp), port(4318, &tcp)},
			}},
		}))
	}
	if n.IngressNamespace != "" {
		out = append(out, policy("allow-ingress-controller", networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{ingress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{nsPeer(n.IngressNamespace)}}},
		}))
	}
	return out
}
