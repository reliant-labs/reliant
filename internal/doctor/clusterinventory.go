// Copyright (c) 2025 Reliant Labs
package doctor

// clusterinventory.go — the STRUCTURED half of the "Cluster Workloads" check.
//
// The check (clusterhealth.go) already holds every fact a consumer could
// want: the rendered workload set, the cluster and namespace each one was
// read from, the pods that matched it, their phases and restart counts. It
// then threw all of it away, formatting the facts into a prose Evidence
// blob and a one-line Message meant for a human at a terminal.
//
// That made `forge env status <env> --json` actively misleading. Its only
// structured inventory was `services` — the HOST processes, a fact about
// the developer's own machine — so a consumer asking forge "what is running
// in prod?" received two local dev servers as data, while the fourteen real
// deployments existed only inside a string. The one documented path to them
// was to parse that string, which is the class of workaround this project
// forbids outright: the format is a display concern and every change to it
// silently breaks the parser.
//
// So the check now ALSO emits what it already knew. Nothing here re-queries
// a cluster; [buildInventory] is assembled from the same targetResults the
// Evidence is rendered from, which is what makes the two incapable of
// disagreeing.
//
// THE CERTAINTY DISCIPLINE CARRIES OVER, and it is the whole reason this is
// a document rather than a bare array. "forge looked and found no
// workloads" and "forge could not reach the cluster" are different facts,
// and an empty JSON array states the first while meaning the second. So:
//
//   - [ClusterInventory.Status] is the check's own verdict, in the check's
//     own vocabulary (pass / fail / warn / unknown / skip) — never a
//     parallel one invented for JSON.
//   - A cluster that refused to answer appears in [ClusterInventory.Clusters]
//     with Status [StatusUnknown] and the error text, and its rendered
//     workloads are STILL listed — each with Status [StatusUnknown] and no
//     pods. A UI can then say "16 workloads, state unknown" instead of
//     showing nothing, which is the same silence the check was written to
//     end.
//   - A workload the render could not route to any cluster is listed with
//     an empty Cluster and Status [StatusUnknown].

import (
	"sort"
	"strings"
)

// ClusterInventory is the structured answer to "what is deployed, and is it
// running?" for one environment. It is emitted at the top level of
// `forge env status <env> --json` as `workloads`, beside `services` — which
// remains, and remains the HOST-process list.
type ClusterInventory struct {
	// Status is the Cluster Workloads check's verdict, unchanged. A
	// consumer that reads nothing else still learns whether the rest of
	// this document is a complete picture: StatusUnknown means at least
	// one fact could not be obtained, so Workloads is NOT an inventory.
	Status Status `json:"status"`
	// Env is the environment whose render produced this set.
	Env string `json:"env"`
	// Clusters are the (context, namespace) pairs forge read, each with
	// its own status. This is how a consumer says WHICH cluster it is
	// reporting on — and, when a probe failed, which one it is not.
	Clusters []ClusterScope `json:"clusters"`
	// Workloads is every pod-owning object this env's render declares,
	// whether or not it was found. Never nil in JSON: a null and an empty
	// array would read alike, and the distinction that matters (unknown
	// vs. genuinely none) is carried by Status, not by absence.
	Workloads []WorkloadState `json:"workloads"`
}

// ClusterScope is one (kubectl context, namespace) pair the check probed.
type ClusterScope struct {
	// Cluster is the kubectl context name — always explicit, never
	// kubectl's ambient current-context, which forge refuses to fall back
	// to (see kubectlPods).
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	// Status is StatusPass when the pod list was obtained and
	// StatusUnknown when it was not. It says nothing about the health of
	// what was found — that is per-workload.
	Status Status `json:"status"`
	// Error is why the probe failed, verbatim, when Status is unknown.
	Error string `json:"error,omitempty"`
	// RenderedWorkloads is how many of this env's workloads land here.
	// Non-zero alongside an unknown Status is precisely the "we could not
	// see N things" fact an empty array cannot express.
	RenderedWorkloads int `json:"rendered_workloads"`
}

// WorkloadState is one rendered pod-owning object and what the cluster said
// about it.
type WorkloadState struct {
	Name string `json:"name"`
	// Kind is lowercased ("deployment", "job", "statefulset", …), matching
	// how the check names kinds in its human output.
	Kind string `json:"kind"`
	// Cluster is the kubectl context this record was read from. EMPTY
	// means the render did not say where this workload deploys — a hole,
	// reported with Status unknown, never silently attributed to whatever
	// context happened to be current.
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	// Status is this workload's verdict in the check's vocabulary:
	// pass, warn, fail, or unknown (the cluster could not be read).
	Status Status `json:"status"`
	// DesiredReplicas is spec.replicas from the RENDER — what this env
	// asked for, which is why a live Deployment hand-scaled to 0 reads as
	// a finding. Omitted for kinds that have no replica count at all
	// (DaemonSet, Job, CronJob); an omitted field and a 0 are different
	// claims and must not collapse.
	DesiredReplicas *int `json:"desired_replicas,omitempty"`
	// ReadyReplicas is how many matched pods report Ready. Always 0 when
	// Status is unknown — read it together with Status, never alone.
	ReadyReplicas int `json:"ready_replicas"`
	// Restarts is the highest restart count across this workload's pods.
	// The max rather than the sum: one container dying 130 times is the
	// signal, and summing it across replicas only obscures which.
	Restarts int `json:"restarts"`
	// Ephemeral marks the kinds whose pods are expected to come and go
	// (Job, CronJob), so a consumer does not render "0 pods" as an alarm
	// for a Job that completed and was reaped.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// Pods are the pods matched to this workload. Empty for a completed
	// Job; empty ALSO when Status is unknown, which is why Status is the
	// field to branch on.
	Pods []PodState `json:"pods"`
	// Findings are the check's own diagnoses for this workload —
	// "0/1 Ready CrashLoopBackOff last=OOMKilled(exit 137) restarts=37",
	// or the reason the cluster could not be read. Already assembled, so a
	// UI can show the actionable sentence without re-deriving it.
	Findings []string `json:"findings,omitempty"`
}

// PodState is one pod, reduced to what a status view shows.
type PodState struct {
	Name string `json:"name"`
	// Ready is the pod's Ready CONDITION, not a container count — it is
	// the field that decides whether traffic reaches it.
	Ready bool `json:"ready"`
	// ContainersReady / Containers are the "1/1" pair.
	ContainersReady int `json:"containers_ready"`
	Containers      int `json:"containers"`
	// Phase is the pod phase ("Running", "Pending", "Succeeded", …).
	Phase string `json:"phase"`
	// Restarts is the highest restartCount across this pod's containers.
	Restarts int `json:"restarts"`
}

// InventoryOf returns the structured cluster inventory carried by report's
// Cluster Workloads check, or nil when that check did not run (a `--signal`
// arm that excludes it, or a project with no runtime checks at all).
//
// nil is a THIRD answer, distinct from both an unknown inventory and an
// empty one: it means forge was never asked.
func InventoryOf(r Report) *ClusterInventory {
	for _, c := range r.Checks {
		if c.Name == clusterWorkloadsCheckName {
			return c.Cluster
		}
	}
	return nil
}

// inventoryStub is the inventory for an outcome that never reached a
// cluster — the render failed, timed out, or the env deploys nothing to
// Kubernetes. Workloads is a non-nil empty slice so the JSON is an array
// either way and the reason lives in Status, where a consumer must look.
func inventoryStub(env string, status Status, why string) *ClusterInventory {
	inv := &ClusterInventory{
		Status:    status,
		Env:       env,
		Clusters:  []ClusterScope{},
		Workloads: []WorkloadState{},
	}
	if why != "" && status == StatusUnknown {
		// Carried on a placeholder scope with no cluster name: there is no
		// context to attribute it to, and inventing one would be the
		// current-context footgun in a different costume.
		inv.Clusters = append(inv.Clusters, ClusterScope{Status: StatusUnknown, Error: why})
	}
	return inv
}

// buildInventory assembles the structured record from the SAME probe
// results the Evidence blob is rendered from. It re-queries nothing and
// re-judges nothing: workload verdicts are read back off the findings the
// check already produced, so the JSON and the human text cannot drift.
func buildInventory(env string, overall Status, results []targetResult, findings []workloadFinding, unrouted []*clusterWorkload) *ClusterInventory {
	inv := &ClusterInventory{
		Status:    overall,
		Env:       env,
		Clusters:  []ClusterScope{},
		Workloads: []WorkloadState{},
	}

	// Findings keyed by target+workload, so a workload deployed to two
	// clusters gets each cluster's verdict rather than the union.
	type fkey struct{ target, kind, workload string }
	byWorkload := map[fkey][]workloadFinding{}
	for _, f := range findings {
		k := fkey{f.target, f.kind, f.workload}
		byWorkload[k] = append(byWorkload[k], f)
	}

	for _, tr := range results {
		label := tr.target.label()
		scope := ClusterScope{
			Cluster:           tr.target.kctx,
			Namespace:         tr.target.namespace,
			Status:            StatusPass,
			RenderedWorkloads: len(tr.target.workloads),
		}
		if tr.err != nil {
			// The hole. The workloads are still listed — each unknown —
			// because "16 workloads, state unknown" is a usable report and
			// showing nothing is the silence this check exists to end.
			scope.Status = StatusUnknown
			scope.Error = tr.err.Error()
			inv.Clusters = append(inv.Clusters, scope)
			for _, w := range tr.target.workloads {
				st := newWorkloadState(w, tr.target.kctx, tr.target.namespace, StatusUnknown)
				st.Findings = []string{"could not list pods on " + label + ": " + tr.err.Error()}
				inv.Workloads = append(inv.Workloads, st)
			}
			continue
		}
		inv.Clusters = append(inv.Clusters, scope)
		for _, w := range tr.target.workloads {
			st := newWorkloadState(w, tr.target.kctx, tr.target.namespace, StatusPass)
			st.Pods = tr.states[w]
			for _, p := range st.Pods {
				if p.Ready {
					st.ReadyReplicas++
				}
				if p.Restarts > st.Restarts {
					st.Restarts = p.Restarts
				}
			}
			for _, f := range byWorkload[fkey{label, w.kind, w.name}] {
				st.Findings = append(st.Findings, findingLine(f))
				if f.severity == StatusFail {
					st.Status = StatusFail
				} else if st.Status != StatusFail {
					st.Status = StatusWarn
				}
			}
			inv.Workloads = append(inv.Workloads, st)
		}
	}

	for _, w := range unrouted {
		st := newWorkloadState(w, "", w.namespace, StatusUnknown)
		st.Findings = []string{"the render does not say which cluster this deploys to" + namespaceSuffix(w.namespace)}
		inv.Workloads = append(inv.Workloads, st)
	}

	sortInventory(inv)
	return inv
}

// newWorkloadState fills the render-derived half — the facts that are true
// whether or not the cluster answered.
func newWorkloadState(w *clusterWorkload, kctx, namespace string, status Status) WorkloadState {
	st := WorkloadState{
		Name:      w.name,
		Kind:      strings.ToLower(w.kind),
		Cluster:   kctx,
		Namespace: namespace,
		Status:    status,
		Ephemeral: w.ephemeral,
		Pods:      []PodState{},
	}
	if w.desired >= 0 {
		d := w.desired
		st.DesiredReplicas = &d
	}
	return st
}

// findingLine renders one finding as the sentence a UI can show, naming the
// pod when the finding is about a pod rather than the workload as a whole.
func findingLine(f workloadFinding) string {
	if f.pod == "" {
		return f.detail
	}
	return "pod " + f.pod + ": " + f.detail
}

// sortInventory makes the document stable across runs — pods within a
// workload, workloads within the set, and the scopes. An unstable order
// turns every poll into a spurious diff for anything that stores or
// compares these snapshots.
func sortInventory(inv *ClusterInventory) {
	sort.SliceStable(inv.Clusters, func(i, j int) bool {
		if inv.Clusters[i].Cluster != inv.Clusters[j].Cluster {
			return inv.Clusters[i].Cluster < inv.Clusters[j].Cluster
		}
		return inv.Clusters[i].Namespace < inv.Clusters[j].Namespace
	})
	sort.SliceStable(inv.Workloads, func(i, j int) bool {
		a, b := inv.Workloads[i], inv.Workloads[j]
		if a.Cluster != b.Cluster {
			return a.Cluster < b.Cluster
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	for i := range inv.Workloads {
		pods := inv.Workloads[i].Pods
		sort.SliceStable(pods, func(a, b int) bool { return pods[a].Name < pods[b].Name })
	}
}
