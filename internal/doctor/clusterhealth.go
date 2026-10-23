// Copyright (c) 2025 Reliant Labs
package doctor

// clusterhealth.go — the "Cluster Workloads" runtime check.
//
// forge applies manifests to a cluster and then held NO opinion about
// whether what it applied is alive. `forge env status <env>` reported Host
// services, Frontends, Compose Infra and App Health — every one of them a
// fact about the developer's own machine — and printed nothing at all about
// the cluster it had just deployed to. Two measurements from one week on
// control-plane, both invisible to forge at the time:
//
//   - `daemon-gateway` was OOMKilled and then CrashLoopBackOff for over an
//     hour while `forge env status dev` stayed all-green. The product
//     reported "no machine is connected" and no forge command would say
//     why. The decisive fact — `lastState.terminated.reason: OOMKilled` —
//     was reachable only by hand, with kubectl, by someone who already
//     suspected the answer.
//   - Later, `admin-api` sat in CreateContainerConfigError while two
//     `litellm` pods and two `reliant-api-server` pods crashlooped to ~120
//     and ~130 restarts each. That ran for TEN HOURS in the deployed
//     namespace with nothing in forge reporting it.
//
// The same hole exists at the gate: `forge env up` blocks only on HOST
// service readiness ("host service(s) never came up under this run") and
// exits 0 with cluster workloads crashlooping behind it.
//
// FOUR PROPERTIES MAKE THIS CHECK WORTH READING
//
//  1. It reports the facts that end the investigation, not a rollup. A pod
//     NAME, the container, the waiting reason, the restart count, and —
//     called out explicitly, because it was the decisive invisible fact —
//     `OOMKilled` from `lastState.terminated`. "1 workload unhealthy" would
//     have saved nobody an hour; "daemon-gateway-8598f99486-frs5m 0/1
//     CrashLoopBackOff last=OOMKilled(exit 137) restarts=37" ends it. The
//     detail therefore goes in the MESSAGE, not only in Evidence: Evidence
//     prints only under `-v`, and the person who needs this is not passing
//     `-v` yet — they do not know there is anything to look at.
//
//  2. It is scoped to the workloads THIS env renders, matched against the
//     rendered set. A namespace is not an environment: `dev` and `dev-k8s`
//     both render into `control-plane-dev`, and reporting on everything in
//     the namespace would make one env's status depend on another env's
//     leftovers. Cross-env writes are a different defect with its own check
//     (CheckObjectCollision); this one answers "is MY env alive".
//
//  3. It honours the three-state Status model, and that is the whole point.
//     A check that answers Pass when it could not look is precisely what
//     created this gap. Cluster unreachable, kubectl missing, context not in
//     the kubeconfig, RBAC refusing the list, render failed, a workload whose
//     cluster could not be determined → [StatusUnknown], which `forge env
//     status` prints as "N UNDETERMINED (not a pass — forge could not obtain
//     the facts)". Only "this env deploys nothing to Kubernetes" is
//     [StatusSkip]. An Unknown anywhere also SUPPRESSES a Pass verdict for
//     the rest: a partial look is not a clean bill of health.
//
//  4. It cannot hang. Every cluster call is bounded, the KCL render is
//     bounded, and a timeout reports Unknown. This runs inside `forge env
//     status`, a snapshot command a human waits on, whose whole runtime
//     phase has a 15s leash (internal/cli.envStatusCheckTimeout).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// clusterWorkloadsCheckName is the display name. Named for what it inspects
// — the k8s workloads the env deploys — rather than "Kubernetes", because a
// project's cluster may hold plenty this env neither owns nor reports on.
const clusterWorkloadsCheckName = "Cluster Workloads"

const (
	// clusterProbeTimeout bounds ONE `kubectl get pods` call. A dead API
	// server, a k3d cluster that is up but not serving, a VPN-less GKE
	// context: all of them hang for kubectl's own default, which is
	// effectively forever for a status command. Six seconds is generous for
	// a list against a reachable cluster and short enough that even two
	// unreachable clusters fit inside the 15s runtime-phase budget, since
	// the probes run concurrently.
	clusterProbeTimeout = 6 * time.Second

	// clusterRenderTimeout bounds the KCL render. kclrender.Run takes no
	// context, so the bound is applied around it (see renderEnvForCluster).
	clusterRenderTimeout = 10 * time.Second

	// podStartupGrace separates "mid-rollout" from "broken". A pod that is
	// not Ready 20 seconds after creation is a deploy in progress; the same
	// pod not Ready two minutes later is a finding. Without the grace this
	// check would go red every time someone deploys, which is how a check
	// teaches people to ignore it — and an ignored check is the state we
	// are already in.
	podStartupGrace = 90 * time.Second

	// restartWarnThreshold is where a Ready pod's restart count stops being
	// noise. The litellm / reliant-api-server pods reached ~120 and ~130;
	// anything past a handful is a container dying repeatedly, whatever it
	// happens to be doing at the instant of the snapshot.
	restartWarnThreshold = 5
)

// hardWaitingReasons are kubelet waiting reasons that are NEVER transient:
// each one means the container will not start until a human changes
// something. They are reported as failures on sight, with no startup grace,
// because waiting longer cannot change the answer — it only lengthens
// the outage.
// Every reason here is drawn from an incident above: CrashLoopBackOff
// (daemon-gateway, litellm, reliant-api-server), CreateContainerConfigError
// (admin-api — a Secret key that was never provisioned), and the image
// family that forge's own deploy preflight names.
var hardWaitingReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"RunContainerError":          true,
	"ImageInspectError":          true,
	"ErrImageNeverPull":          true,
}

// oomKilled is the container termination reason that was invisible and
// decisive. It is singled out everywhere in this file — sorted first,
// named in the one-line message, never folded into a generic "restarted"
// — because it names the FIX (raise the memory limit) rather than the
// symptom, and because an hour was spent not knowing it.
const oomKilled = "OOMKilled"

// clusterWorkloadKinds are the rendered kinds that own pods, mapped to
// whether their pods are EPHEMERAL — expected to come and go. A Deployment
// with no pods is a finding; a Job with no pods has almost certainly
// completed and been garbage-collected by ttlSecondsAfterFinished, and a
// CronJob between schedules never has any. Reporting those as missing would
// be a permanent false alarm on every project that migrates.
var clusterWorkloadKinds = map[string]bool{
	"Deployment":  false,
	"StatefulSet": false,
	"DaemonSet":   false,
	"Job":         true,
	"CronJob":     true,
}

// clusterWorkload is one rendered pod-owning object, reduced to what
// deciding "is it alive" needs.
type clusterWorkload struct {
	kind      string
	name      string
	namespace string
	// app is the `app.kubernetes.io/name` label forge stamps on every
	// workload it renders. It is the SELECTOR half of pod matching — the
	// same label internal/cluster.podSelectorForDeploy uses — and the
	// fallback when a pod's ownerReference chain says nothing useful.
	app string
	// desired is spec.replicas from the RENDER, not from the live object.
	// The render is the authority on what this env asked for: a live
	// Deployment scaled to 0 by hand is a finding precisely because the
	// render still says 1. -1 means the kind has no replica count
	// (DaemonSet / Job / CronJob).
	desired int
	// ephemeral mirrors clusterWorkloadKinds: absence of pods is normal.
	ephemeral bool
	// envTag is the env name forge's ownership stamp (`forge.dev/env`,
	// kcl/lib/labels.k) would carry for this render. Kept on the workload so
	// a pod occupying this address on behalf of a DIFFERENT env can be named
	// as such instead of being reported as this env's broken workload.
	envTag string
	// clusters is every kubectl context this object actually lands on, via
	// envRender.clustersOf — which mirrors internal/cluster.
	// ScopeManifestsToGroup, the code that performs the routing at deploy
	// time. An env can span several: control-plane's `dev` puts most
	// workloads on `k3d-control-plane` and `workspace-proxy` on
	// `k3d-cp-daemon`, so "the cluster" is not a property of the env.
	clusters []string
}

// probeTarget is one (cluster, namespace) pair to list pods in. Workloads
// are grouped into these so an env spanning N clusters costs N kubectl
// calls, not one per workload — and so a cluster that cannot be reached
// takes down the report for ITS workloads only.
type probeTarget struct {
	kctx      string
	namespace string
	workloads []*clusterWorkload
}

func (t probeTarget) label() string { return t.kctx + "/" + t.namespace }

// podView is the slice of `kubectl get pods -o json` this check reads.
// Deliberately narrow: every field here is one more thing a k8s upgrade can
// change under us, and the rest of the PodStatus answers no question being
// asked.
type podView struct {
	Metadata struct {
		Name              string            `json:"name"`
		Labels            map[string]string `json:"labels"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		OwnerReferences   []struct {
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			Controller *bool  `json:"controller"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type               string    `json:"type"`
			Status             string    `json:"status"`
			Reason             string    `json:"reason"`
			Message            string    `json:"message"`
			LastTransitionTime time.Time `json:"lastTransitionTime"`
		} `json:"conditions"`
		ContainerStatuses     []containerView `json:"containerStatuses"`
		InitContainerStatuses []containerView `json:"initContainerStatuses"`
	} `json:"status"`
}

// containerView carries BOTH the current state and lastState. lastState is
// not optional detail: a container that has been restarted is, at the
// instant of the snapshot, either Running (so its current state says
// nothing) or Waiting with reason CrashLoopBackOff (which says it is
// backing off, but not from WHAT). `lastState.terminated.reason` is the
// only place `OOMKilled` ever appears.
type containerView struct {
	Name         string         `json:"name"`
	Ready        bool           `json:"ready"`
	RestartCount int            `json:"restartCount"`
	State        containerState `json:"state"`
	LastState    containerState `json:"lastState"`
}

type containerState struct {
	Waiting *struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"waiting"`
	Terminated *struct {
		Reason   string `json:"reason"`
		ExitCode int    `json:"exitCode"`
	} `json:"terminated"`
	Running *struct {
		StartedAt time.Time `json:"startedAt"`
	} `json:"running"`
}

// podLister is the seam. Production supplies kubectlPods; tests supply
// fabricated pods and fabricated failures, which is the only way to pin
// "unreachable cluster ⇒ Unknown" without an unreachable cluster.
type podLister func(ctx context.Context, kctx, namespace string) ([]podView, error)

// CheckClusterWorkloads reports whether the Kubernetes workloads this
// environment deploys are actually running.
func CheckClusterWorkloads(ctx context.Context, env *Environment) CheckResult {
	render, early := renderEnvForCluster(ctx, env)
	if early != nil {
		// Even an early exit carries an inventory: a consumer must be able
		// to tell "the render failed, so the workload set is unknown" from
		// "this env deploys none", and both from a real empty cluster.
		early.Cluster = inventoryStub(strings.TrimSpace(env.Env), early.Status, early.Message)
		return *early
	}
	return clusterWorkloadReport(ctx, render, clusterPodLister)
}

// clusterPodLister is the seam at the OUTER entry point. clusterWorkloadReport
// already takes a podLister, but that seam is downstream of the render — so a
// test using it cannot observe the one thing that was wrong here: which
// namespace the render resolved, and therefore which namespace the probe asks
// kubectl about. Substituting here puts the render inside the test's reach, so
// the assertion can be on the namespace that reaches the k8s call rather than
// on a helper's return value.
var clusterPodLister podLister = kubectlPods

// renderEnvForCluster renders THIS environment — and only this one.
//
// The deployability checks share deployRenders(), which renders EVERY
// declared environment behind a sync.Once, because all of them ask
// cross-env questions. This check asks about one env, in a command a human
// is waiting on: control-plane's five environments cost ~2.5s to render
// against ~0.9s for one, and rendering `prod` to answer a question about
// `dev` is work whose only possible outcomes are "wasted" and "a new
// failure mode" (KCL renders can have side effects — control-plane's dev
// render materialises deploy/nats/nats.conf via file.write).
//
// The `-D env=<name>` binding is NOT optional and is the one thing that
// must not drift from renderDeployEnvs / internal/cli's renderKCLRaw: it is
// what `option("env")` reads, and forge's own kcl/schema.k gates rules on
// it. A render without it is a different render.
func renderEnvForCluster(ctx context.Context, env *Environment) (envRender, *CheckResult) {
	name := strings.TrimSpace(env.Env)
	if name == "" {
		return envRender{}, &CheckResult{
			Status:  StatusUnknown,
			Message: "no environment was named, so there is no render to say which workloads should be running",
		}
	}
	rel := filepath.Join("deploy", "kcl", name, "main.k")
	if _, err := os.Stat(filepath.Join(env.ProjectDir, rel)); err != nil {
		// Not a failure and not a hole: a --kind cli project, or an env
		// that genuinely deploys nothing to a cluster, has answered the
		// question by its own shape.
		return envRender{}, &CheckResult{
			Status:  StatusSkip,
			Message: fmt.Sprintf("env %q declares no %s — nothing is deployed to a cluster", name, rel),
		}
	}

	// kclrender.Run takes no context, so the bound goes around it. On
	// timeout the goroutine is left to finish and its result is dropped:
	// one goroutine that terminates on its own, in a CLI process, is the
	// cheap half of the trade against a status command that never returns.
	type outcome struct {
		raw []byte
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, err := kclrender.Run(env.ProjectDir, filepath.Join(env.ProjectDir, rel), []string{"env=" + name})
		done <- outcome{raw: raw, err: err}
	}()

	timer := time.NewTimer(clusterRenderTimeout)
	defer timer.Stop()
	var got outcome
	select {
	case got = <-done:
	case <-timer.C:
		return envRender{}, &CheckResult{
			Status:  StatusUnknown,
			Message: fmt.Sprintf("rendering env %q did not finish within %s — could not determine which workloads should be running", name, clusterRenderTimeout),
		}
	case <-ctx.Done():
		return envRender{}, &CheckResult{
			Status:  StatusUnknown,
			Message: fmt.Sprintf("rendering env %q was cancelled — could not determine which workloads should be running", name),
		}
	}
	if got.err != nil {
		// UNDETERMINED, not Fail. That the KCL does not render is a real
		// defect, but it is CheckDeployManifests' finding — reported once,
		// by the check that exists for it. Here it means only that this
		// check could not learn what should be running.
		return envRender{}, &CheckResult{
			Status:   StatusUnknown,
			Message:  fmt.Sprintf("could not render env %q, so the workloads it deploys are unknown (see `forge doctor`)", name),
			Evidence: got.err.Error(),
		}
	}
	r := parseRender(got.raw)
	r.env = name
	if r.err != nil {
		return envRender{}, &CheckResult{
			Status:   StatusUnknown,
			Message:  fmt.Sprintf("could not parse the render of env %q, so the workloads it deploys are unknown", name),
			Evidence: r.err.Error(),
		}
	}
	return r, nil
}

// clusterWorkloadReport is the whole check, minus the two things that talk
// to the outside world. Given a render and a way to list pods it is pure,
// which is what lets the tests pin CrashLoopBackOff, OOMKilled, an
// unreachable cluster and a multi-cluster env without a cluster.
func clusterWorkloadReport(ctx context.Context, r envRender, list podLister) CheckResult {
	workloads, unrouted := clusterWorkloadsOf(r)
	if len(workloads) == 0 && len(unrouted) == 0 {
		return CheckResult{
			Status:  StatusSkip,
			Message: fmt.Sprintf("env %q deploys no Kubernetes workloads", r.env),
			Cluster: inventoryStub(r.env, StatusSkip, ""),
		}
	}

	targets := groupByTarget(workloads)

	var findings []workloadFinding
	var undetermined []string
	// A workload whose cluster the render could not name is a hole, never a
	// pass. Falling back to kubectl's CURRENT context would be the exact
	// footgun internal/cluster.KubectlApply refuses for writes — asking the
	// wrong cluster and believing the answer.
	for _, w := range unrouted {
		undetermined = append(undetermined, fmt.Sprintf(
			"%s/%s: the render does not say which cluster it deploys to%s",
			w.kind, w.name, namespaceSuffix(w.namespace)))
	}

	results := probeTargets(ctx, targets, list)
	now := time.Now()
	for i := range results {
		tr := &results[i]
		if tr.err != nil {
			undetermined = append(undetermined, fmt.Sprintf(
				"%s: could not list pods (%d workload(s) unreported) — %s",
				tr.target.label(), len(tr.target.workloads), tr.err))
			continue
		}
		var found []workloadFinding
		found, tr.matched, tr.healthy, tr.states = judgeTarget(tr.target, tr.pods, now)
		findings = append(findings, found...)
	}

	res := summarise(targets, results, findings, undetermined)
	// The structured twin of Evidence, built from the SAME results rather
	// than re-queried or re-judged — see clusterinventory.go for why the
	// prose blob alone was not an answer.
	res.Cluster = buildInventory(r.env, res.Status, results, findings, unrouted)
	return res
}

// clusterWorkloadsOf turns a render into the workload set to report on, and
// separately the ones whose cluster could not be determined.
//
// The RENDERED objects are the authority — not the namespace's contents.
// That is the scoping rule: `dev` and `dev-k8s` both render into
// `control-plane-dev`, so "everything in the namespace" would make one
// env's status a function of another env's leftovers.
func clusterWorkloadsOf(r envRender) (workloads, unrouted []*clusterWorkload) {
	for _, o := range r.objects {
		ephemeral, owns := clusterWorkloadKinds[o.Kind]
		if !owns {
			continue // Service, ConfigMap, RBAC, CRD: no pods, nothing to be unhealthy
		}
		name := strings.TrimSpace(o.Metadata.Name)
		if name == "" {
			continue // not addressable; CheckDeployManifests' finding, not ours
		}
		w := &clusterWorkload{
			kind:      o.Kind,
			name:      name,
			namespace: strings.TrimSpace(o.Metadata.Namespace),
			app:       o.Metadata.Labels[cluster.AppNameLabel],
			desired:   renderedReplicas(o),
			ephemeral: ephemeral,
			envTag:    r.env,
			clusters:  r.clustersOf(o),
		}
		switch {
		case len(w.clusters) == 0 || w.namespace == "":
			unrouted = append(unrouted, w)
		default:
			workloads = append(workloads, w)
		}
	}
	return workloads, unrouted
}

// renderedReplicas reads spec.replicas, defaulting the way the API server
// does (1 when unset for the replicated kinds). -1 for kinds that have no
// replica count at all, so "no pods" can be judged against the right
// expectation.
func renderedReplicas(o k8sObject) int {
	switch o.Kind {
	case "Deployment", "StatefulSet":
	default:
		return -1
	}
	v, ok := o.Spec["replicas"]
	if !ok {
		return 1
	}
	if n, isNum := v.(float64); isNum {
		return int(n)
	}
	return 1
}

// groupByTarget collapses workloads into one probe per (cluster,
// namespace). A workload that lands on several clusters — the routing rule
// replicates an unattributed manifest to EVERY group — appears in each,
// because each is a real place its pods are supposed to be.
func groupByTarget(workloads []*clusterWorkload) []probeTarget {
	idx := map[string]*probeTarget{}
	var order []string
	for _, w := range workloads {
		for _, c := range w.clusters {
			key := c + "\x00" + w.namespace
			t, seen := idx[key]
			if !seen {
				t = &probeTarget{kctx: c, namespace: w.namespace}
				idx[key] = t
				order = append(order, key)
			}
			t.workloads = append(t.workloads, w)
		}
	}
	sort.Strings(order)
	out := make([]probeTarget, 0, len(order))
	for _, key := range order {
		out = append(out, *idx[key])
	}
	return out
}

// targetResult is one probe's outcome; err is a HOLE, never a failure.
type targetResult struct {
	target probeTarget
	pods   []podView
	err    error
	// matched is how many of the listed pods belong to a workload THIS env
	// renders. It is what the report counts, never len(pods): the namespace
	// legitimately holds other envs' pods (control-plane-dev holds `dev`'s
	// and `dev-k8s`'s at once), and counting those would claim forge
	// inspected work it deliberately ignored.
	matched int
	// healthy is one evidence line per workload that had nothing wrong with
	// it, naming its pods. `-v` means "show me what you looked at": a
	// verbose report that lists only complaints leaves the reader with the
	// same "did it even check?" doubt this check exists to remove.
	healthy []string
	// states is the STRUCTURED twin of healthy + findings: the matched pods
	// per workload, reduced to what a consumer reads. Produced by the same
	// pass that produces the prose, which is what stops the two from
	// disagreeing. Nil when the probe failed — see buildInventory.
	states map[*clusterWorkload][]PodState
}

// probeTargets lists pods for every target CONCURRENTLY. Serial probes
// would multiply the per-call timeout by the number of clusters, and an env
// spanning two unreachable clusters would then blow the whole runtime
// phase's budget on its own.
func probeTargets(ctx context.Context, targets []probeTarget, list podLister) []targetResult {
	results := make([]targetResult, len(targets))
	done := make(chan int, len(targets))
	for i, t := range targets {
		go func(i int, t probeTarget) {
			pods, err := list(ctx, t.kctx, t.namespace)
			results[i] = targetResult{target: t, pods: pods, err: err}
			done <- i
		}(i, t)
	}
	for range targets {
		<-done
	}
	return results
}

// workloadFinding is one thing wrong with one workload, carrying enough to
// act on without a follow-up kubectl.
type workloadFinding struct {
	severity Status // StatusFail or StatusWarn
	target   string // "<context>/<namespace>"
	workload string
	kind     string
	pod      string // empty when the finding is about the workload as a whole
	// detail is the diagnosis, already assembled: "0/1 Ready
	// CrashLoopBackOff last=OOMKilled(exit 137) restarts=37".
	detail string
	// oom marks a finding that carries an OOMKill, so it sorts to the front
	// and reaches the one-line message even when other findings compete.
	oom bool
}

// judgeTarget matches the target's pods to its workloads and judges each.
// The second return is how many pods matched — the count the report uses,
// because the unmatched ones are not this env's to speak for — the third
// is an evidence line per clean workload, and the fourth is the same
// matched pods as structured [PodState]s for the JSON inventory.
func judgeTarget(t probeTarget, pods []podView, now time.Time) ([]workloadFinding, int, []string, map[*clusterWorkload][]PodState) {
	byName := map[workloadKey]*clusterWorkload{}
	byApp := map[string][]*clusterWorkload{}
	for _, w := range t.workloads {
		byName[workloadKey{w.kind, w.name}] = w
		if w.app != "" {
			byApp[w.app] = append(byApp[w.app], w)
		}
	}

	owned := map[*clusterWorkload][]podView{}
	matched := 0
	for _, p := range pods {
		if w := matchWorkload(p, byName, byApp); w != nil {
			owned[w] = append(owned[w], p)
			matched++
		}
	}

	var findings []workloadFinding
	var healthy []string
	for _, w := range t.workloads {
		mine := owned[w]
		if len(mine) == 0 {
			if f, bad := judgeMissing(t, w); bad {
				f.kind = w.kind
				findings = append(findings, f)
				continue
			}
			healthy = append(healthy, fmt.Sprintf("%s (%s)  no pods — expected for this kind",
				w.name, strings.ToLower(w.kind)))
			continue
		}
		clean := true
		for _, p := range mine {
			if f, bad := judgePod(t, w, p, now); bad {
				f.kind = w.kind
				findings = append(findings, f)
				clean = false
			}
		}
		if clean {
			healthy = append(healthy, fmt.Sprintf("%s (%s)  %s",
				w.name, strings.ToLower(w.kind), podRoster(mine)))
		}
	}
	states := make(map[*clusterWorkload][]PodState, len(t.workloads))
	for _, w := range t.workloads {
		states[w] = podStates(owned[w])
	}
	return findings, matched, healthy, states
}

// podStates is podRoster's structured twin: the same readiness, phase and
// restart facts, as data rather than a formatted line. Both read the same
// fields in the same way, deliberately — a consumer and a human looking at
// one status must never be told different things.
func podStates(pods []podView) []PodState {
	out := make([]PodState, 0, len(pods))
	for _, p := range pods {
		st := PodState{Name: p.Metadata.Name, Phase: p.Status.Phase, Ready: conditionTrue(p, "Ready")}
		if st.Phase == "" {
			st.Phase = "?"
		}
		for _, c := range p.Status.ContainerStatuses {
			st.Containers++
			if c.Ready {
				st.ContainersReady++
			}
			if c.RestartCount > st.Restarts {
				st.Restarts = c.RestartCount
			}
		}
		// Init containers restart too, and an init container stuck in a
		// crash loop is exactly the shape that leaves a pod at 0/1 with a
		// container restart count of zero.
		for _, c := range p.Status.InitContainerStatuses {
			if c.RestartCount > st.Restarts {
				st.Restarts = c.RestartCount
			}
		}
		out = append(out, st)
	}
	return out
}

// podRoster names the pods behind a healthy workload, with their readiness
// and restart counts. A workload that is Ready on 130 restarts is reported
// as a warning elsewhere; here it is the plain record of what was seen.
func podRoster(pods []podView) string {
	parts := make([]string, 0, len(pods))
	for _, p := range pods {
		ready, total, restarts := 0, 0, 0
		for _, c := range p.Status.ContainerStatuses {
			total++
			if c.Ready {
				ready++
			}
			if c.RestartCount > restarts {
				restarts = c.RestartCount
			}
		}
		phase := p.Status.Phase
		if phase == "" {
			phase = "?"
		}
		parts = append(parts, fmt.Sprintf("pod %s %d/%d %s restarts=%d",
			p.Metadata.Name, ready, total, phase, restarts))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// judgeMissing decides what "the render says this exists, the cluster has
// no pods for it" means. This is the OTHER half of the daemon-gateway
// incident's shape: a workload that is absent is exactly as invisible as
// one that is crashlooping, and reports the same "no machine is connected".
func judgeMissing(t probeTarget, w *clusterWorkload) (workloadFinding, bool) {
	switch {
	case w.ephemeral:
		// A completed Job whose pods were reaped by ttlSecondsAfterFinished,
		// or a CronJob between schedules. Silence is the correct report.
		return workloadFinding{}, false
	case w.desired == 0:
		// The render itself asks for zero. Deliberate, so not a finding.
		return workloadFinding{}, false
	case w.kind == "DaemonSet":
		// A DaemonSet's pod count is the node count filtered by its
		// nodeSelector/tolerations, which can legitimately be zero. Warn —
		// worth knowing, not worth asserting is broken.
		return workloadFinding{
			severity: StatusWarn, target: t.label(), workload: w.name,
			detail: "no pods scheduled (DaemonSet — no node matches its selector?)",
		}, true
	default:
		want := "1 replica"
		if w.desired > 1 {
			want = strconv.Itoa(w.desired) + " replicas"
		}
		return workloadFinding{
			severity: StatusFail, target: t.label(), workload: w.name,
			detail: fmt.Sprintf("NO PODS — the render declares %s, the cluster has none (never deployed, pruned, or scaled to 0 by hand)", want),
		}, true
	}
}

// judgePod is the diagnosis. It reads the fields a human would read, in the
// order that ends the investigation soonest.
func judgePod(t probeTarget, w *clusterWorkload, p podView, now time.Time) (workloadFinding, bool) {
	ready, total := 0, 0
	restarts := 0
	var reasons []string
	oom, hard := false, false

	inspect := func(c containerView, kind string) {
		if c.RestartCount > restarts {
			restarts = c.RestartCount
		}
		// An OOM kill lives in lastState for a container that restarted and
		// in state for one that is dead right now. BOTH are read and both
		// are named — this is the fact the check exists for — but only the
		// current one is a hard failure on its own: a container that was
		// OOM-killed and came back is a wrong memory limit, not an outage,
		// and calling it an outage is how a check gets ignored.
		if t := c.State.Terminated; t != nil && t.Reason == oomKilled {
			oom, hard = true, true
			reasons = append(reasons, oomPhrase(kind, c.Name, t.ExitCode))
		} else if t := c.LastState.Terminated; t != nil && t.Reason == oomKilled {
			oom = true
			reasons = append(reasons, oomPhrase(kind, c.Name, t.ExitCode))
		}
		if c.State.Waiting != nil && hardWaitingReasons[c.State.Waiting.Reason] {
			hard = true
			reasons = append(reasons, kind+c.Name+" "+c.State.Waiting.Reason+lastTerminationSuffix(c))
		}
		if c.State.Terminated != nil && c.State.Terminated.Reason != oomKilled &&
			c.State.Terminated.ExitCode != 0 && p.Status.Phase != "Succeeded" {
			hard = true
			reasons = append(reasons, fmt.Sprintf("%s%s terminated %s (exit %d)",
				kind, c.Name, c.State.Terminated.Reason, c.State.Terminated.ExitCode))
		}
	}
	for _, c := range p.Status.InitContainerStatuses {
		inspect(c, "init container ")
	}
	for _, c := range p.Status.ContainerStatuses {
		total++
		if c.Ready {
			ready++
		}
		inspect(c, "")
	}

	podReady := conditionTrue(p, "Ready")
	phase := p.Status.Phase
	age := now.Sub(p.Metadata.CreationTimestamp)

	// A Pending pod with no container statuses at all has not been
	// scheduled: the reason is on the PodScheduled condition, and
	// "Unschedulable — insufficient memory" is otherwise reported as a
	// blank "0/0 not Ready", which says nothing.
	if total == 0 && phase == "Pending" {
		if why := unschedulableReason(p); why != "" {
			reasons = append(reasons, why)
			hard = true
		}
	}
	if phase == "Failed" {
		reasons = append(reasons, "pod phase Failed")
		hard = true
	}

	detail := func() string {
		parts := []string{fmt.Sprintf("%d/%d Ready", ready, total)}
		if phase != "" && phase != "Running" {
			parts = append(parts, phase)
		}
		if len(reasons) > 0 {
			parts = append(parts, strings.Join(reasons, "; "))
		}
		if restarts > 0 {
			parts = append(parts, fmt.Sprintf("restarts=%d", restarts))
		}
		if tag := p.Metadata.Labels[forgeEnvLabel]; tag != "" && tag != w.envTag {
			// The pod at this address was put here by a DIFFERENT env. Not
			// this check's finding (that is CheckObjectCollision), but
			// naming it turns a baffling "our workload is unhealthy" into
			// "that is not our workload".
			parts = append(parts, "forge.dev/env="+tag)
		}
		return strings.Join(parts, "  ")
	}

	switch {
	case phase == "Succeeded" && !oom:
		// A Job pod that ran to completion. Not-Ready is its resting state.
		return workloadFinding{}, false
	case hard, oom && !podReady:
		// An OOM kill on a pod that is DOWN is a failure outright — no
		// startup grace. The grace exists to tell a rollout from an outage,
		// and an OOMKill is neither ambiguous nor transient: waiting longer
		// only produces the next one. Measured: daemon-gateway's first
		// snapshot after the limit was wrong showed exactly this shape,
		// seconds before the kubelet relabelled it CrashLoopBackOff.
		return workloadFinding{severity: StatusFail, target: t.label(), workload: w.name,
			pod: p.Metadata.Name, detail: detail(), oom: oom}, true
	case oom:
		// Back up and serving, but it HAS been OOM-killed. Warn rather than
		// Fail — nothing is down at this instant — and never silence: the
		// limit is still wrong and it will happen again, which is how
		// daemon-gateway got from "restarted once" to an hour of
		// CrashLoopBackOff.
		return workloadFinding{severity: StatusWarn, target: t.label(), workload: w.name,
			pod: p.Metadata.Name, detail: detail(), oom: true}, true
	case !podReady:
		// Pod age determines startup grace, but does not measure an outage:
		// a years-old pod can have lost readiness only seconds ago.
		var unreadyFor time.Duration
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "False" && !c.LastTransitionTime.IsZero() {
				unreadyFor = now.Sub(c.LastTransitionTime)
			}
		}
		sev := StatusFail
		if age >= 0 && age < podStartupGrace {
			sev = StatusWarn // mid-rollout, not broken
		}
		return workloadFinding{severity: sev, target: t.label(), workload: w.name,
			pod: p.Metadata.Name, detail: detail() + startingSuffix(sev, age, unreadyFor)}, true
	case restarts >= restartWarnThreshold:
		return workloadFinding{severity: StatusWarn, target: t.label(), workload: w.name,
			pod: p.Metadata.Name, detail: detail() + " — Ready now, but it keeps dying", oom: false}, true
	}
	return workloadFinding{}, false
}

// oomPhrase names an OOM kill AND the fix. "OOMKilled" alone is the symptom;
// the memory limit is the thing a reader can act on, and the hour spent on
// daemon-gateway was spent not knowing either.
func oomPhrase(kind, container string, exit int) string {
	return fmt.Sprintf("%s%s OOMKilled (exit %d) — raise its memory limit", kind, container, exit)
}

// forgeEnvLabel is the env-ownership stamp forge renders onto every object
// (kcl/lib/labels.k). Read opportunistically: objects deployed before the
// stamp existed do not carry it, so its ABSENCE means nothing.
const forgeEnvLabel = "forge.dev/env"

func startingSuffix(sev Status, age, unreadyFor time.Duration) string {
	if sev == StatusWarn {
		return fmt.Sprintf(" — starting (%s old)", age.Round(time.Second))
	}
	if unreadyFor <= 0 {
		return " — not Ready"
	}
	return fmt.Sprintf(" — not Ready for %s", unreadyFor.Round(time.Second))
}

// lastTerminationSuffix appends WHY the container last died to a waiting
// reason. "CrashLoopBackOff" alone says a container is backing off;
// "CrashLoopBackOff last=OOMKilled(exit 137)" says what to fix.
func lastTerminationSuffix(c containerView) string {
	t := c.LastState.Terminated
	if t == nil || t.Reason == "" || t.Reason == oomKilled {
		return "" // OOMKilled is reported on its own, in full
	}
	return fmt.Sprintf(" last=%s(exit %d)", t.Reason, t.ExitCode)
}

func conditionTrue(p podView, typ string) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == typ {
			return c.Status == "True"
		}
	}
	return false
}

func unschedulableReason(p podView) string {
	for _, c := range p.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status != "True" {
			msg := strings.TrimSpace(c.Message)
			if msg == "" {
				msg = c.Reason
			}
			if msg == "" {
				return "not scheduled"
			}
			return "not scheduled: " + msg
		}
	}
	return ""
}

// matchWorkload answers which rendered workload a pod belongs to, or nil —
// and nil is a normal, frequent answer, because the namespace holds pods
// this env does not own.
//
// The ownerReference chain is preferred over the app label because it is
// EXACT. A pod's owner is a ReplicaSet named "<deployment>-<hash>", or the
// StatefulSet / DaemonSet / Job itself; stripping exactly one trailing
// segment recovers the workload name without the prefix-matching that would
// attribute `reliant-api-server`'s pods to a workload called `reliant-api`.
// A known controller outside the rendered set rules out label fallback:
// content-hashed Jobs deliberately share an app label across deployments.
// Falling back would attribute an old failed Job's pods to its replacement.
type workloadKey struct{ kind, name string }

func matchWorkload(p podView, byName map[workloadKey]*clusterWorkload, byApp map[string][]*clusterWorkload) *clusterWorkload {
	matchOwner := func(kind, name string) *clusterWorkload {
		if w, ok := byName[workloadKey{kind, name}]; ok {
			return w
		}
		if base, ok := stripGeneratedSuffix(kind, name); ok {
			parentKind := "Deployment"
			if kind == "Job" {
				parentKind = "CronJob"
			}
			return byName[workloadKey{parentKind, base}]
		}
		return nil
	}
	for _, ref := range p.Metadata.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return matchOwner(ref.Kind, ref.Name)
		}
	}
	// Older/synthetic pod snapshots may omit controller. Preserve owner
	// matching there, but never let a shared label override an explicit owner.
	controlled := false
	for _, ref := range p.Metadata.OwnerReferences {
		if ref.Controller == nil {
			controlled = true
			if w := matchOwner(ref.Kind, ref.Name); w != nil {
				return w
			}
		}
	}
	if controlled {
		return nil
	}
	// Ambiguous label (two rendered workloads share it — a Deployment and
	// its migrate Job commonly do) attributes to neither: a wrong
	// attribution reports the wrong workload as broken, which is worse than
	// reporting nothing.
	if ws := byApp[p.Metadata.Labels[cluster.AppNameLabel]]; len(ws) == 1 {
		return ws[0]
	}
	return nil
}

// stripGeneratedSuffix removes the one segment the controller generates:
// a ReplicaSet's pod-template hash, or the timestamp a CronJob appends to
// each Job it creates.
func stripGeneratedSuffix(kind, name string) (string, bool) {
	switch kind {
	case "ReplicaSet", "Job":
	default:
		return "", false
	}
	i := strings.LastIndex(name, "-")
	if i <= 0 {
		return "", false
	}
	if kind == "Job" {
		// CronJobs append a numeric scheduled timestamp. A content hash on
		// an unrelated standalone Job is not evidence of CronJob ownership.
		if _, err := strconv.ParseUint(name[i+1:], 10, 64); err != nil {
			return "", false
		}
	}
	return name[:i], true
}

func namespaceSuffix(ns string) string {
	if ns == "" {
		return " and renders no namespace"
	}
	return " (namespace " + ns + ")"
}

// summarise rolls the findings up. The precedence is Fail > Unknown > Warn >
// Pass: an undetermined probe outranks a warning because a hole in the
// report is the specific defect this check was written to close, and it must
// never be reported as a clean pass for the parts that WERE reachable.
func summarise(targets []probeTarget, results []targetResult, findings []workloadFinding, undetermined []string) CheckResult {
	sortFindings(findings)

	pods := 0
	for _, tr := range results {
		pods += tr.matched
	}
	scope := targetScope(targets)

	var fails, warns int
	for _, f := range findings {
		if f.severity == StatusFail {
			fails++
		} else {
			warns++
		}
	}

	res := CheckResult{Evidence: buildEvidence(results, findings, undetermined)}
	switch {
	case fails > 0:
		res.Status = StatusFail
		// A failure does NOT absolve the holes. Reporting "1 failing" while
		// silently dropping "and one cluster never answered" is a smaller
		// version of the same defect this check closes, so the hole rides
		// along on the one line the reader actually sees.
		res.Message = headline(fails, warns, findings, scope) + undeterminedSuffix(undetermined)
	case len(undetermined) > 0:
		res.Status = StatusUnknown
		res.Message = fmt.Sprintf("%s — %s", strings.Join(shorten(undetermined, 2), "; "),
			partialSuffix(warns))
	case warns > 0:
		res.Status = StatusWarn
		res.Message = headline(fails, warns, findings, scope)

	default:
		res.Status = StatusPass
		workloads := 0
		for _, t := range targets {
			workloads += len(t.workloads)
		}
		res.Message = fmt.Sprintf("%d workload(s), %d pod(s) Ready — %s", workloads, pods, scope)
	}
	return res
}

// undeterminedSuffix appends the count of things forge could not look at to
// a report that already has something to say.
func undeterminedSuffix(undetermined []string) string {
	if len(undetermined) == 0 {
		return ""
	}
	return fmt.Sprintf("  + %d NOT LOOKED AT (-v)", len(undetermined))
}

// headline is the one line the reader gets without `-v`. It names pods and
// reasons, not counts: "1 workload unhealthy" is the report that already
// existed, in spirit, and it is the one that cost an hour.
func headline(fails, warns int, findings []workloadFinding, scope string) string {
	n := 2
	if len(findings) == 3 {
		n = 3 // never say "+1 more" when spelling it out costs one clause
	}
	shown := findings
	if len(shown) > n {
		shown = shown[:n]
	}
	parts := make([]string, 0, len(shown))
	for _, f := range shown {
		if f.pod == "" {
			parts = append(parts, fmt.Sprintf("%s: %s", f.workload, f.detail))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (pod %s): %s", f.workload, f.pod, f.detail))
	}
	msg := strings.Join(parts, " | ")
	if extra := len(findings) - len(shown); extra > 0 {
		msg += fmt.Sprintf(" | +%d more (-v)", extra)
	}
	return fmt.Sprintf("%s  [%d failing, %d warning in %s]", msg, fails, warns, scope)
}

func partialSuffix(warns int) string {
	if warns == 0 {
		return "the rest could not be judged, so this is NOT a pass"
	}
	return fmt.Sprintf("%d warning(s) elsewhere; the rest could not be judged, so this is NOT a pass", warns)
}

// sortFindings puts an OOMKill first, then failures, then everything else
// alphabetically. The ordering is load-bearing: only the first two or three
// findings reach the one-line message, and OOMKilled is the fact that was
// invisible for an hour.
func sortFindings(f []workloadFinding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].oom != f[j].oom {
			return f[i].oom
		}
		if (f[i].severity == StatusFail) != (f[j].severity == StatusFail) {
			return f[i].severity == StatusFail
		}
		if f[i].workload != f[j].workload {
			return f[i].workload < f[j].workload
		}
		return f[i].pod < f[j].pod
	})
}

func targetScope(targets []probeTarget) string {
	seen := map[string]bool{}
	var labels []string
	for _, t := range targets {
		if l := t.label(); !seen[l] {
			seen[l] = true
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	if len(labels) == 0 {
		return "no cluster"
	}
	return strings.Join(labels, ", ")
}

func shorten(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	out := append([]string(nil), lines[:n]...)
	return append(out, fmt.Sprintf("+%d more (-v)", len(lines)-n))
}

// buildEvidence prints the full per-target picture, including the healthy
// workloads. Under `-v` the reader wants to see what forge LOOKED at, not
// only what it disliked — an empty evidence block on a Pass is the same
// "did it even check?" doubt the check exists to remove.
func buildEvidence(results []targetResult, findings []workloadFinding, undetermined []string) string {
	var b strings.Builder
	byTarget := map[string][]workloadFinding{}
	for _, f := range findings {
		byTarget[f.target] = append(byTarget[f.target], f)
	}
	for _, tr := range results {
		label := tr.target.label()
		fmt.Fprintf(&b, "%s  (%d workload(s) rendered)\n", label, len(tr.target.workloads))
		if tr.err != nil {
			fmt.Fprintf(&b, "  ? could not list pods: %v\n", tr.err)
			continue
		}
		for _, f := range byTarget[label] {
			icon := "!"
			if f.severity == StatusFail {
				icon = "x"
			}
			if f.pod == "" {
				fmt.Fprintf(&b, "  %s %s  %s\n", icon, f.workload, f.detail)
			} else {
				fmt.Fprintf(&b, "  %s %s  pod %s  %s\n", icon, f.workload, f.pod, f.detail)
			}
		}
		for _, line := range tr.healthy {
			fmt.Fprintf(&b, "  o %s\n", line)
		}
	}
	if len(undetermined) > 0 {
		b.WriteString("\nUndetermined — forge could not obtain these facts:\n")
		for _, u := range undetermined {
			fmt.Fprintf(&b, "  ? %s\n", u)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// kubectlPods lists a namespace's pods on ONE declared context.
//
// The context is always passed explicitly (internal/cluster.KubectlArgs) and
// an empty one is refused rather than silently becoming kubectl's current
// context — the same rule KubectlApply enforces for writes, for the same
// reason: an unrelated tool flipping current-context (k3d does) would make
// this check report on a cluster the env has nothing to do with, and believe
// it.
func kubectlPods(ctx context.Context, kctx, namespace string) ([]podView, error) {
	if strings.TrimSpace(kctx) == "" {
		return nil, fmt.Errorf("no kubectl context declared for this workload (forge never falls back to the current context)")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, fmt.Errorf("kubectl is not on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, clusterProbeTimeout)
	defer cancel()

	// --request-timeout is kubectl's OWN bound, so an unreachable API
	// server fails with a message instead of being killed by the context
	// with no explanation of what it was waiting for.
	args := cluster.KubectlArgs(kctx,
		"get", "pods", "-n", namespace, "-o", "json",
		"--request-timeout="+clusterProbeTimeout.String())
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context %q did not answer within %s", kctx, clusterProbeTimeout)
		}
		return nil, fmt.Errorf("kubectl --context %s: %s", kctx, firstStderrLine(stderr.String(), err))
	}
	var list struct {
		Items []podView `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		return nil, fmt.Errorf("could not parse kubectl output: %w", err)
	}
	return list.Items, nil
}

// firstStderrLine keeps a kubectl failure to one line — the report is a
// table, and a wrapped RBAC stack trace destroys it. The full text is not
// lost: it is what the user sees when they run the command themselves.
func firstStderrLine(stderr string, err error) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return err.Error()
}
