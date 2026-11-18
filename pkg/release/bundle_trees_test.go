package release

import (
	"reflect"
	"strings"
	"testing"
)

// treeObj is a TreeObject on the prod hub.
func treeObj(cluster, unit string, phase Phase, kind, name string) TreeObject {
	return TreeObject{Cluster: cluster, Unit: unit, Phase: phase, Kind: kind, Namespace: "control-plane-prod", Name: name}
}

func TestClassifyPhase(t *testing.T) {
	post := map[string]string{AnnotationDeployPhase: DeployPhasePostRollout}
	pre := map[string]string{AnnotationDeployPhase: DeployPhasePreRollout}
	for _, tc := range []struct {
		name, kind string
		ann        map[string]string
		want       Phase
	}{
		// control-plane prod's Jobs
		{"temporal-schema-1023848b73", "Job", nil, PhasePre},
		{"control-plane-migrate-b2e57763c4", "Job", pre, PhasePre},
		{"temporal-namespace-040a6cfe06", "Job", post, PhasePost},
		{"openbao-bootstrap-df3aa129ed", "Job", post, PhasePost},
		// the workloads
		{"admin-server", "Deployment", nil, PhaseRollout},
		{"nats-cluster", "StatefulSet", nil, PhaseRollout},
		{"kata-prepull", "DaemonSet", nil, PhaseRollout},
		{"backup-prune", "CronJob", nil, PhaseRollout},
		// support: what a pre-rollout Job rests on
		{"control-plane-prod", "Namespace", nil, PhasePre},
		{"workspaces.example", "CustomResourceDefinition", nil, PhasePre},
		{"workspace-ssd", "StorageClass", nil, PhasePre},
		{"admin-server", "Service", nil, PhasePre},
		{"admin-server", "ServiceAccount", nil, PhasePre},
		{"allow-temporal-ingress", "NetworkPolicy", nil, PhasePre},
		// a phase annotation on anything but a Job means nothing
		{"admin-server", "Deployment", post, PhaseRollout},
	} {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			got, err := ClassifyPhase(tc.kind, tc.name, tc.ann)
			if err != nil || got != tc.want {
				t.Errorf("ClassifyPhase = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestClassifyPhaseRefusesATypo(t *testing.T) {
	_, err := ClassifyPhase("Job", "app-migrate", map[string]string{AnnotationDeployPhase: "pre-rolout"})
	if err == nil {
		t.Fatal("a typo'd phase must not silently pick a side")
	}
	for _, want := range []string{"app-migrate", "pre-rolout", DeployPhasePreRollout, DeployPhasePostRollout} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// prodUnitsT is the apply graph of design §4.2: the daemon cluster first, then
// the hub's base (Namespace, CRDs), then every other unit.
var prodUnitsT = []DeliveryUnit{
	{Name: "daemon-substrate", After: []string{}},
	{Name: "hub-base", After: []string{"daemon-substrate"}},
	{Name: "reliant", After: []string{"hub-base"}},
	{Name: "infra", After: []string{"hub-base"}},
	{Name: "control-plane", After: []string{"hub-base"}},
}

// controlPlaneProd is the shape of prod from rollout-log session C: the daemon
// cluster, then the hub's base, infra, reliant and control-plane units.
func controlPlaneProd() ([]TreeObject, []DeliveryUnit) {
	const hub, daemon = "gke-prod", "gke-prod-daemon"
	objs := []TreeObject{
		// daemon-substrate: CNPG, kata, storage classes
		treeObj(daemon, "daemon-substrate", PhasePre, "StorageClass", "workspace-ssd"),
		treeObj(daemon, "daemon-substrate", PhasePre, "CustomResourceDefinition", "clusters.postgresql.cnpg.io"),
		treeObj(daemon, "daemon-substrate", PhaseRollout, "Deployment", "cnpg-controller-manager"),
		treeObj(daemon, "daemon-substrate", PhaseRollout, "DaemonSet", "kata-prepull"),
		// hub base: namespace, CRDs, network policy, the prune-disabled PVC
		treeObj(hub, "hub-base", PhasePre, "Namespace", "control-plane-prod"),
		treeObj(hub, "hub-base", PhasePre, "PersistentVolumeClaim", "nats-jetstream"),
		// infra: temporal-schema before, temporal-namespace after
		treeObj(hub, "infra", PhasePre, "Job", "temporal-schema-1023848b73"),
		treeObj(hub, "infra", PhaseRollout, "Deployment", "temporal"),
		treeObj(hub, "infra", PhaseRollout, "StatefulSet", "nats-cluster"),
		treeObj(hub, "infra", PhasePost, "Job", "temporal-namespace-040a6cfe06"),
		// reliant: runs migrations at boot, no Jobs
		treeObj(hub, "reliant", PhaseRollout, "Deployment", "reliant-api-server"),
		// control-plane: migrate before, idp-provision after
		treeObj(hub, "control-plane", PhasePre, "Job", "control-plane-migrate-b2e57763c4"),
		treeObj(hub, "control-plane", PhaseRollout, "Deployment", "admin-server"),
		treeObj(hub, "control-plane", PhasePost, "Job", "control-plane-idp-provision-aa11bb22cc"),
	}
	units := prodUnitsT
	return objs, units
}

func treeByPath(t *testing.T, trees []BundleTree, path string) BundleTree {
	t.Helper()
	for _, tr := range trees {
		if tr.Path == path {
			return tr
		}
	}
	t.Fatalf("no tree %s in %v", path, paths(trees))
	return BundleTree{}
}

func paths(trees []BundleTree) []string {
	out := make([]string, len(trees))
	for i, t := range trees {
		out[i] = t.Path
	}
	return out
}

// TestPlanTreesControlPlaneProd is the golden for the real prod shape: the
// order session C had to enforce by hand — temporal-schema before rollout,
// migrate before the apps, temporal-namespace after, the daemon cluster before
// the hub.
func TestPlanTreesControlPlaneProd(t *testing.T) {
	objs, units := controlPlaneProd()
	trees, err := PlanTrees(objs, units)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"manifests/gke-prod-daemon/daemon-substrate/pre",
		"manifests/gke-prod-daemon/daemon-substrate/rollout",
		"manifests/gke-prod/control-plane/post",
		"manifests/gke-prod/control-plane/pre",
		"manifests/gke-prod/control-plane/rollout",
		"manifests/gke-prod/hub-base/pre",
		"manifests/gke-prod/infra/post",
		"manifests/gke-prod/infra/pre",
		"manifests/gke-prod/infra/rollout",
		"manifests/gke-prod/reliant/rollout",
	}
	if got := paths(trees); !reflect.DeepEqual(got, want) {
		t.Fatalf("trees\n got %v\nwant %v", got, want)
	}

	deps := map[string][]string{
		// the daemon substrate waits for nothing; its phases chain
		"manifests/gke-prod-daemon/daemon-substrate/pre":     nil,
		"manifests/gke-prod-daemon/daemon-substrate/rollout": {"manifests/gke-prod-daemon/daemon-substrate/pre"},
		// the hub's base waits for the daemon cluster's LAST tree
		"manifests/gke-prod/hub-base/pre": {"manifests/gke-prod-daemon/daemon-substrate/rollout"},
		// every other unit waits for base; infra: schema Job before temporal,
		// temporal-namespace after temporal serves
		"manifests/gke-prod/infra/pre":     {"manifests/gke-prod/hub-base/pre"},
		"manifests/gke-prod/infra/rollout": {"manifests/gke-prod/infra/pre"},
		"manifests/gke-prod/infra/post":    {"manifests/gke-prod/infra/rollout"},
		// reliant: stateless, no Jobs
		"manifests/gke-prod/reliant/rollout": {"manifests/gke-prod/hub-base/pre"},
		// control-plane: migrate before the apps, idp-provision after
		"manifests/gke-prod/control-plane/pre":     {"manifests/gke-prod/hub-base/pre"},
		"manifests/gke-prod/control-plane/rollout": {"manifests/gke-prod/control-plane/pre"},
		"manifests/gke-prod/control-plane/post":    {"manifests/gke-prod/control-plane/rollout"},
	}
	for path, want := range deps {
		if got := treeByPath(t, trees, path).DependsOn; !reflect.DeepEqual(got, want) {
			t.Errorf("%s dependsOn\n got %v\nwant %v", path, got, want)
		}
	}

	infra := treeByPath(t, trees, "manifests/gke-prod/infra/rollout")
	if infra.TimeoutSeconds != TreeTimeoutSeconds || infra.RetryIntervalSeconds != TreeRetryIntervalSeconds {
		t.Errorf("per-attempt settings = %d/%d", infra.TimeoutSeconds, infra.RetryIntervalSeconds)
	}
	wantHealth := []BundleHealthCheck{
		{Kind: "Deployment", Namespace: "control-plane-prod", Name: "temporal"},
		{Kind: "StatefulSet", Namespace: "control-plane-prod", Name: "nats-cluster"},
	}
	if !reflect.DeepEqual(infra.Health, wantHealth) {
		t.Errorf("infra/rollout health = %v, want %v", infra.Health, wantHealth)
	}
	// A prepull DaemonSet is not a gate: direct apply does not wait on it.
	for _, h := range treeByPath(t, trees, "manifests/gke-prod-daemon/daemon-substrate/rollout").Health {
		if h.Kind == "DaemonSet" {
			t.Errorf("a DaemonSet gates its tree: %+v", h)
		}
	}
	// A pre tree is judged on its Jobs, never its support objects.
	pre := treeByPath(t, trees, "manifests/gke-prod/infra/pre")
	if len(pre.Health) != 1 || pre.Health[0].Kind != "Job" {
		t.Errorf("infra/pre health = %v, want the temporal-schema Job only", pre.Health)
	}
}

func TestPlanTreesIsDeterministic(t *testing.T) {
	objs, units := controlPlaneProd()
	first, err := PlanTrees(objs, units)
	if err != nil {
		t.Fatal(err)
	}
	// Reverse the input and the declarations: the output is a function of the
	// SET, not of the order a render happened to emit it in.
	rev := make([]TreeObject, len(objs))
	for i, o := range objs {
		rev[len(objs)-1-i] = o
	}
	ru := make([]DeliveryUnit, len(units))
	for i, u := range units {
		ru[len(units)-1-i] = u
	}
	for i := 0; i < 20; i++ {
		again, err := PlanTrees(rev, ru)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d produced different trees", i)
		}
	}
}

func TestPlanTreesDefaultUnitOrdering(t *testing.T) {
	// Undeclared units wait for base; base waits for nothing.
	trees, err := PlanTrees([]TreeObject{
		treeObj("hub", "", PhasePre, "Namespace", "ns"),
		treeObj("hub", "web", PhaseRollout, "Deployment", "web"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeByPath(t, trees, "manifests/hub/web/rollout").DependsOn; !reflect.DeepEqual(got, []string{"manifests/hub/base/pre"}) {
		t.Errorf("web waits for %v, want base", got)
	}
	if got := treeByPath(t, trees, "manifests/hub/base/pre").DependsOn; got != nil {
		t.Errorf("base waits for %v", got)
	}
}

func TestPlanTreesSkipsAbsentPhases(t *testing.T) {
	// Only rollout objects: no empty pre or post tree, and no dependency on one.
	trees, err := PlanTrees([]TreeObject{treeObj("hub", "reliant", PhaseRollout, "Deployment", "api")}, []DeliveryUnit{{Name: "reliant", After: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].DependsOn != nil {
		t.Errorf("trees = %+v", trees)
	}
}

func TestPlanTreesRefusals(t *testing.T) {
	two := []TreeObject{treeObj("hub", "a", PhaseRollout, "Deployment", "a"), treeObj("hub", "b", PhaseRollout, "Deployment", "b")}
	for _, tc := range []struct {
		name  string
		objs  []TreeObject
		units []DeliveryUnit
		want  string
	}{
		{"cycle", two, []DeliveryUnit{{Name: "a", After: []string{"b"}}, {Name: "b", After: []string{"a"}}}, "cycle"},
		{"self", two, []DeliveryUnit{{Name: "a", After: []string{"a"}}}, "after itself"},
		{"unknown dependency", two, []DeliveryUnit{{Name: "a", After: []string{"nope"}}}, "nothing declares or renders"},
		{"duplicate unit", two, []DeliveryUnit{{Name: "a"}, {Name: "a"}}, "declared twice"},
		{"bad segment", two, []DeliveryUnit{{Name: "a/b"}}, "path segment"},
		{"colliding segments", []TreeObject{treeObj("hub", "a b", PhasePre, "Namespace", "x"), treeObj("hub", "a-b", PhasePre, "Namespace", "y")}, nil, "same path segment"},
		{"flat cluster", []TreeObject{treeObj("", "a", PhasePre, "Namespace", "x")}, nil, "flat cluster"},
		{"bad phase", []TreeObject{treeObj("hub", "a", Phase("sideways"), "Namespace", "x")}, nil, "phase"},
		// base is implicit for everyone else: a unit that does not want it
		// must say so, and base after a unit that waits on base is a cycle.
		{"implicit base cycle", []TreeObject{treeObj("hub", "", PhasePre, "Namespace", "x"), treeObj("hub", "web", PhaseRollout, "Deployment", "w")},
			[]DeliveryUnit{{Name: "base", After: []string{"web"}}}, "cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PlanTrees(tc.objs, tc.units)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestBundleTreePathMatchesTheLayerGrammar(t *testing.T) {
	// The tree path is exactly the directory ValidateManifestsLayer's v2
	// grammar admits: manifests/<cluster>/<unit>/<phase>.
	if got := BundleTreePath("gke_p/us-central1", "infra", PhasePost); got != "manifests/gke_p-us-central1/infra/post" {
		t.Errorf("path = %q", got)
	}
	for _, p := range Phases {
		if !layerPhases[string(p)] {
			t.Errorf("phase %q is not in the layer grammar", p)
		}
	}
	if len(layerPhases) != len(Phases) {
		t.Errorf("layer grammar has %d phases, the classifier has %d", len(layerPhases), len(Phases))
	}
}

func TestValidateTreesRefusesALyingDocument(t *testing.T) {
	good := BundleTree{Cluster: "hub", Unit: "a", Phase: PhaseRollout, Path: BundleTreePath("hub", "a", PhaseRollout), Documents: 2, TimeoutSeconds: 90, RetryIntervalSeconds: 15, BudgetSeconds: 720}
	clusters := []BundleClusterTree{{Cluster: "hub", Path: BundleClusterPath("hub"), Documents: 2}}
	if err := validateTrees(clusters, []BundleTree{good}); err != nil {
		t.Fatalf("a consistent document is refused: %v", err)
	}
	for name, mutate := range map[string]func(*BundleTree){
		"wrong path":     func(t *BundleTree) { t.Path = "manifests/hub/a/pre" },
		"empty tree":     func(t *BundleTree) { t.Documents = 0 },
		"no timeout":     func(t *BundleTree) { t.TimeoutSeconds = 0 },
		"tiny budget":    func(t *BundleTree) { t.BudgetSeconds = 30 },
		"unknown phase":  func(t *BundleTree) { t.Phase = "late" },
		"dangling dep":   func(t *BundleTree) { t.DependsOn = []string{"manifests/hub/zzz/pre"} },
		"self dependent": func(t *BundleTree) { t.DependsOn = []string{t.Path} },
		"flat cluster":   func(t *BundleTree) { t.Cluster = "hosted"; t.Path = BundleTreePath("hosted", "a", PhaseRollout) },
		"count mismatch": func(t *BundleTree) { t.Documents = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			if err := validateTrees(clusters, []BundleTree{bad}); err == nil {
				t.Error("a lying tree set was accepted")
			}
		})
	}
}
