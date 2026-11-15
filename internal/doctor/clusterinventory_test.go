package doctor

import (
	"encoding/json"
	"strings"
	"testing"
)

// The defect these tests pin: `forge env status <env> --json` emitted the
// cluster's real state ONLY as prose inside a check's `evidence` string,
// while its one structured inventory (`services`) listed host processes —
// two local dev servers, for a prod env running fourteen deployments. A
// consumer's only path to the truth was to parse the prose. So every case
// below asserts a fact is reachable as DATA, and in particular that the
// three answers a naive array conflates stay apart: workloads present,
// cluster unreachable (unknown — NOT an empty list), and a Job that
// legitimately has no pods.

// cwInventory runs the check and returns the structured inventory, failing
// if there isn't one — a missing inventory is the original bug.
func cwInventory(t *testing.T, r envRender, list podLister) *ClusterInventory {
	t.Helper()
	got := cwRun(t, r, list)
	if got.Cluster == nil {
		t.Fatalf("check produced no structured inventory — the only machine-readable "+
			"answer would again be the evidence blob\nmessage: %s", got.Message)
	}
	return got.Cluster
}

func cwFindWorkload(t *testing.T, inv *ClusterInventory, name string) WorkloadState {
	t.Helper()
	for _, w := range inv.Workloads {
		if w.Name == name {
			return w
		}
	}
	t.Fatalf("workload %q is not in the inventory (%d present)", name, len(inv.Workloads))
	return WorkloadState{}
}

// --- workloads present ----------------------------------------------------

// The headline case: what a UI asking "what is running in prod?" must get
// back. Every field it needs — name, kind, replica counts, restarts, pods,
// and the cluster/namespace coordinates — as data, not text.
func TestClusterInventoryReportsWorkloadsAsStructuredData(t *testing.T) {
	r := cwRender(t, "prod", "gke_prod", nil,
		cwDeploy("admin-server", "control-plane-prod"),
		cwDeploy("zitadel", "control-plane-prod"))

	inv := cwInventory(t, r, cwPods(t, map[string][]string{
		"gke_prod/control-plane-prod": {
			cwPod{name: "admin-server-7c9d-aaa", app: "admin-server", ownerRS: "admin-server-7c9d", ready: true}.json(),
			cwPod{name: "zitadel-64d5c8484c-ccm8k", app: "zitadel", ownerRS: "zitadel-64d5c8484c", ready: true, restarts: 2}.json(),
		},
	}))

	if inv.Status != StatusPass {
		t.Fatalf("inventory status = %q, want %q", inv.Status, StatusPass)
	}
	if inv.Env != "prod" {
		t.Errorf("inventory env = %q, want %q", inv.Env, "prod")
	}
	if len(inv.Workloads) != 2 {
		t.Fatalf("got %d workloads, want 2", len(inv.Workloads))
	}

	// The cluster coordinates. A consumer has to be able to say WHICH
	// cluster it read, and a rendered-workload count is what makes an
	// unreachable scope distinguishable from an empty one.
	if len(inv.Clusters) != 1 {
		t.Fatalf("got %d cluster scopes, want 1: %+v", len(inv.Clusters), inv.Clusters)
	}
	scope := inv.Clusters[0]
	if scope.Cluster != "gke_prod" || scope.Namespace != "control-plane-prod" {
		t.Errorf("scope = %q/%q, want gke_prod/control-plane-prod", scope.Cluster, scope.Namespace)
	}
	if scope.Status != StatusPass || scope.RenderedWorkloads != 2 {
		t.Errorf("scope status/count = %q/%d, want pass/2", scope.Status, scope.RenderedWorkloads)
	}

	z := cwFindWorkload(t, inv, "zitadel")
	if z.Kind != "deployment" {
		t.Errorf("kind = %q, want %q", z.Kind, "deployment")
	}
	if z.Cluster != "gke_prod" || z.Namespace != "control-plane-prod" {
		t.Errorf("workload coordinates = %q/%q, want gke_prod/control-plane-prod", z.Cluster, z.Namespace)
	}
	if z.Status != StatusPass {
		t.Errorf("status = %q, want %q", z.Status, StatusPass)
	}
	if z.DesiredReplicas == nil || *z.DesiredReplicas != 1 {
		t.Errorf("desired replicas = %v, want 1", z.DesiredReplicas)
	}
	if z.ReadyReplicas != 1 {
		t.Errorf("ready replicas = %d, want 1", z.ReadyReplicas)
	}
	if z.Restarts != 2 {
		t.Errorf("restarts = %d, want 2 — the count is what says a container keeps dying", z.Restarts)
	}
	if len(z.Pods) != 1 {
		t.Fatalf("got %d pods, want 1", len(z.Pods))
	}
	p := z.Pods[0]
	if p.Name != "zitadel-64d5c8484c-ccm8k" || !p.Ready || p.Phase != "Running" ||
		p.ContainersReady != 1 || p.Containers != 1 || p.Restarts != 2 {
		t.Errorf("pod = %+v, want the name/ready/phase/1-of-1/restarts=2 a status view shows", p)
	}
}

// A failing workload's diagnosis must reach the structured record too —
// otherwise a UI can show that something is red but not what to do, and
// reaching for the prose to find out is the exact workaround being removed.
func TestClusterInventoryCarriesTheFindingOnAnUnhealthyWorkload(t *testing.T) {
	r := cwRender(t, "dev", "k3d-control-plane", nil, cwDeploy("daemon-gateway", "control-plane-dev"))

	inv := cwInventory(t, r, cwPods(t, map[string][]string{
		"k3d-control-plane/control-plane-dev": {
			cwPod{
				name: "daemon-gateway-8598f99486-frs5m", app: "daemon-gateway",
				ownerRS: "daemon-gateway-8598f99486", ready: false,
				waiting: "CrashLoopBackOff", lastTerm: "OOMKilled", exitCode: 137, restarts: 37,
			}.json(),
		},
	}))

	if inv.Status != StatusFail {
		t.Fatalf("inventory status = %q, want %q", inv.Status, StatusFail)
	}
	w := cwFindWorkload(t, inv, "daemon-gateway")
	if w.Status != StatusFail {
		t.Errorf("workload status = %q, want %q", w.Status, StatusFail)
	}
	if w.ReadyReplicas != 0 || w.Restarts != 37 {
		t.Errorf("ready/restarts = %d/%d, want 0/37", w.ReadyReplicas, w.Restarts)
	}
	if len(w.Findings) == 0 {
		t.Fatal("no findings on a crashlooping workload — a consumer would have to parse the evidence blob")
	}
	joined := ""
	for _, f := range w.Findings {
		joined += f + "\n"
	}
	for _, want := range []string{"OOMKilled", "CrashLoopBackOff", "daemon-gateway-8598f99486-frs5m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings never mention %q:\n%s", want, joined)
		}
	}
}

// --- cluster unreachable: UNKNOWN, never an empty list --------------------

// The certainty rule, which is why `workloads` is a document and not an
// array. An unreachable cluster must NOT serialise as "zero workloads":
// those are different facts and a consumer acting on the first when the
// second is true reports a healthy empty env over an outage.
func TestClusterInventoryIsUnknownNotEmptyWhenTheClusterCannotBeRead(t *testing.T) {
	r := cwRender(t, "prod", "gke_prod", nil,
		cwDeploy("admin-server", "control-plane-prod"),
		cwDeploy("zitadel", "control-plane-prod"))

	inv := cwInventory(t, r, cwUnreachable("Unable to connect to the server: dial tcp: i/o timeout"))

	if inv.Status != StatusUnknown {
		t.Fatalf("inventory status = %q, want %q — an unreachable cluster is a hole, not a pass",
			inv.Status, StatusUnknown)
	}
	// The workloads are STILL listed, each unknown. "2 workloads, state
	// unknown" is usable; showing nothing is the silence the check exists
	// to end.
	if len(inv.Workloads) != 2 {
		t.Fatalf("got %d workloads, want 2 — the render knows they exist even when the cluster will not say",
			len(inv.Workloads))
	}
	for _, w := range inv.Workloads {
		if w.Status != StatusUnknown {
			t.Errorf("workload %q status = %q, want %q", w.Name, w.Status, StatusUnknown)
		}
		if len(w.Pods) != 0 {
			t.Errorf("workload %q reports pods it could not have seen: %+v", w.Name, w.Pods)
		}
		if w.ReadyReplicas != 0 {
			t.Errorf("workload %q claims %d ready replicas from a cluster that never answered",
				w.Name, w.ReadyReplicas)
		}
	}
	if len(inv.Clusters) != 1 {
		t.Fatalf("got %d cluster scopes, want 1", len(inv.Clusters))
	}
	scope := inv.Clusters[0]
	if scope.Status != StatusUnknown {
		t.Errorf("scope status = %q, want %q", scope.Status, StatusUnknown)
	}
	if scope.Error == "" {
		t.Error("scope carries no error — a UI cannot say WHY it could not read the cluster")
	}
	if scope.Cluster != "gke_prod" || scope.Namespace != "control-plane-prod" {
		t.Errorf("an unreachable scope must still name itself; got %q/%q", scope.Cluster, scope.Namespace)
	}
	if scope.RenderedWorkloads != 2 {
		t.Errorf("rendered_workloads = %d, want 2 — this is the count that makes "+
			"'could not see 2 things' expressible", scope.RenderedWorkloads)
	}
}

// The same rule one level down: a workload the render cannot route to any
// cluster is unknown with an EMPTY cluster field, never attributed to
// whatever kubectl context happened to be current.
func TestClusterInventoryMarksAnUnroutableWorkloadUnknownWithNoCluster(t *testing.T) {
	r := cwRender(t, "dev", "", nil, cwDeploy("orphan", "some-ns"))

	inv := cwInventory(t, r, cwPods(t, nil))

	if inv.Status != StatusUnknown {
		t.Fatalf("inventory status = %q, want %q", inv.Status, StatusUnknown)
	}
	w := cwFindWorkload(t, inv, "orphan")
	if w.Status != StatusUnknown {
		t.Errorf("status = %q, want %q", w.Status, StatusUnknown)
	}
	if w.Cluster != "" {
		t.Errorf("cluster = %q, want empty — forge never guesses a context", w.Cluster)
	}
}

// A render that fails entirely still yields an inventory, so a consumer can
// tell "forge could not learn what should be running" from "nothing runs".
func TestClusterInventoryExistsWhenThereIsNoEnvToRender(t *testing.T) {
	got := CheckClusterWorkloads(t.Context(), &Environment{ProjectName: "p", ProjectDir: t.TempDir()})
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q", got.Status, StatusUnknown)
	}
	if got.Cluster == nil {
		t.Fatal("no inventory on an undetermined check — a consumer sees a missing key and cannot tell why")
	}
	if got.Cluster.Status != StatusUnknown {
		t.Errorf("inventory status = %q, want %q", got.Cluster.Status, StatusUnknown)
	}
	if got.Cluster.Workloads == nil {
		t.Error("workloads is null rather than an empty array — the reason belongs in status, " +
			"but the shape must not shift under a consumer")
	}
}

// --- a job with no pods ---------------------------------------------------

// A completed Job whose pods were reaped by ttlSecondsAfterFinished has no
// pods, and that is CORRECT — not a hole and not a failure. It must be in
// the inventory as a pass with an empty pod list and the ephemeral marker,
// so a UI does not render it as an alarm.
func TestClusterInventoryReportsAJobWithNoPodsAsPassAndEphemeral(t *testing.T) {
	r := cwRender(t, "prod", "gke_prod", nil,
		cwDeploy("admin-server", "control-plane-prod"),
		cwJob("control-plane-migrate-aaa90288d6", "control-plane-prod"))

	inv := cwInventory(t, r, cwPods(t, map[string][]string{
		"gke_prod/control-plane-prod": {
			cwPod{name: "admin-server-7c9d-aaa", app: "admin-server", ownerRS: "admin-server-7c9d", ready: true}.json(),
		},
	}))

	if inv.Status != StatusPass {
		t.Fatalf("inventory status = %q, want %q — a reaped Job is not a finding\n%+v", inv.Status, StatusPass, inv)
	}
	job := cwFindWorkload(t, inv, "control-plane-migrate-aaa90288d6")
	if job.Kind != "job" {
		t.Errorf("kind = %q, want %q", job.Kind, "job")
	}
	if job.Status != StatusPass {
		t.Errorf("status = %q, want %q", job.Status, StatusPass)
	}
	if !job.Ephemeral {
		t.Error("ephemeral = false — without it a consumer cannot tell 'no pods, expected' " +
			"from 'no pods, missing'")
	}
	if len(job.Pods) != 0 {
		t.Errorf("pods = %+v, want none", job.Pods)
	}
	if len(job.Findings) != 0 {
		t.Errorf("findings = %v, want none on a healthy completed Job", job.Findings)
	}
	// A Job has no replica count at all, so the field must be ABSENT
	// rather than 0 — those are different claims.
	if job.DesiredReplicas != nil {
		t.Errorf("desired_replicas = %d on a Job, want omitted", *job.DesiredReplicas)
	}
}

// --- the wire shape -------------------------------------------------------

// The contract is only real if it survives marshalling: the consumer reads
// JSON, not Go structs.
func TestClusterInventorySerialisesTheKeysAConsumerReads(t *testing.T) {
	r := cwRender(t, "prod", "gke_prod", nil, cwJob("migrate", "control-plane-prod"))
	got := cwRun(t, r, cwPods(t, nil))

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal CheckResult: %v", err)
	}
	var back struct {
		Cluster *struct {
			Status    string `json:"status"`
			Env       string `json:"env"`
			Workloads []struct {
				Name      string `json:"name"`
				Kind      string `json:"kind"`
				Cluster   string `json:"cluster"`
				Namespace string `json:"namespace"`
				Status    string `json:"status"`
				Pods      []struct {
					Name string `json:"name"`
				} `json:"pods"`
			} `json:"workloads"`
			Clusters []struct {
				Cluster   string `json:"cluster"`
				Namespace string `json:"namespace"`
				Status    string `json:"status"`
			} `json:"clusters"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if back.Cluster == nil {
		t.Fatalf("no `cluster` key in the marshalled check:\n%s", raw)
	}
	if back.Cluster.Env != "prod" || back.Cluster.Status == "" {
		t.Errorf("env/status = %q/%q\n%s", back.Cluster.Env, back.Cluster.Status, raw)
	}
	if len(back.Cluster.Workloads) != 1 {
		t.Fatalf("got %d workloads over the wire, want 1\n%s", len(back.Cluster.Workloads), raw)
	}
	w := back.Cluster.Workloads[0]
	if w.Name != "migrate" || w.Kind != "job" || w.Cluster != "gke_prod" || w.Namespace != "control-plane-prod" {
		t.Errorf("workload over the wire = %+v", w)
	}
	// Never null: a consumer iterating pods must not have to nil-check a
	// field whose emptiness already means something.
	if w.Pods == nil {
		t.Errorf("pods serialised as null rather than []\n%s", raw)
	}
	if len(back.Cluster.Clusters) != 1 {
		t.Errorf("clusters over the wire = %+v", back.Cluster.Clusters)
	}
}

// InventoryOf is how the CLI lifts the inventory to the top level of
// `forge env status --json`. nil is a real third answer — forge was not
// asked — and must not be confused with an unknown or empty inventory.
func TestInventoryOfFindsTheClusterCheckAndNilsOtherwise(t *testing.T) {
	want := &ClusterInventory{Status: StatusPass, Env: "prod"}
	rep := Report{Checks: []CheckResult{
		{Name: "App Health", Status: StatusPass},
		{Name: clusterWorkloadsCheckName, Status: StatusPass, Cluster: want},
	}}
	if got := InventoryOf(rep); got != want {
		t.Errorf("InventoryOf did not return the cluster check's inventory: %+v", got)
	}
	if got := InventoryOf(Report{Checks: []CheckResult{{Name: "App Health"}}}); got != nil {
		t.Errorf("InventoryOf = %+v on a report with no cluster check, want nil", got)
	}
}
