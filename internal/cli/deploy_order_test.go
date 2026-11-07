package cli

import (
	"reflect"
	"strings"
	"testing"
)

func orderClusterWorkload(name string, deps map[string]string) WorkloadEntity {
	return WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeCluster}, ReleaseDependencies: deps}
}

func orderHostedWorkload(name string, deps map[string]string) WorkloadEntity {
	return WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeHosted}, ReleaseDependencies: deps}
}

func orderFrontend(name, runtime string, deps map[string]string) FrontendEntity {
	return FrontendEntity{Name: name, Runtime: FrontendRuntime{Type: runtime}, ReleaseDependencies: deps}
}

// The order is DERIVED from declarations, never a fixed tier. Each case names
// the placement mix and the order its dependencies imply.
func TestDeployStageOrder(t *testing.T) {
	cases := []struct {
		name string
		e    *KCLEntities
		want []deployStage
	}{
		{
			name: "no edges: the order forge always had",
			e:    &KCLEntities{Workloads: []WorkloadEntity{orderClusterWorkload("api", map[string]string{})}},
			want: []deployStage{stageLocal, stageHosted, stageFrontends},
		},
		{
			// control-plane prod after the merge: the API on our cluster, the
			// SPA hosted. The SPA declares nothing, so it waits for every
			// server workload.
			name: "hosted SPA, cluster API, SPA declares nothing",
			e: &KCLEntities{
				Workloads: []WorkloadEntity{orderClusterWorkload("api", nil)},
				Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
			},
			want: []deployStage{stageLocal, stageHosted, stageFrontends},
		},
		{
			// The case a fixed "cluster before hosted" tier gets wrong: a
			// hosted API and a cluster frontend container that calls it.
			name: "cluster workload consumes a hosted API",
			e: &KCLEntities{Workloads: []WorkloadEntity{
				orderHostedWorkload("api", map[string]string{}),
				orderClusterWorkload("console", map[string]string{"api": "v1"}),
			}},
			want: []deployStage{stageHosted, stageLocal, stageFrontends},
		},
		{
			// A frontend forge ships to a bucket, consuming a hosted API: the
			// hosted stage first, then the bucket.
			name: "bucket frontend consumes a hosted API",
			e: &KCLEntities{
				Workloads: []WorkloadEntity{orderHostedWorkload("api", nil)},
				Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeBucket, map[string]string{"api": "v2"})},
			},
			want: []deployStage{stageLocal, stageHosted, stageFrontends},
		},
		{
			// Explicit {} on the frontend: independent, so no edge and the
			// default order stands even though a cluster workload depends on
			// a hosted one.
			name: "explicit {} frontend adds no edge",
			e: &KCLEntities{
				Workloads: []WorkloadEntity{orderHostedWorkload("api", nil), orderClusterWorkload("worker", map[string]string{"api": "v1"})},
				Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, map[string]string{})},
			},
			want: []deployStage{stageHosted, stageLocal, stageFrontends},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deployStageOrder(tc.e)
			if err != nil {
				t.Fatalf("deployStageOrder: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

// A cluster part and a hosted part that each wait for the other cannot be
// ordered; the refusal names one edge each way and happens before anything
// ships.
func TestDeployStageOrder_RefusesACycle(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		orderHostedWorkload("api", map[string]string{"db-proxy": "v1"}),
		orderClusterWorkload("db-proxy", map[string]string{"api": "v1"}),
	}}
	_, err := deployStageOrder(e)
	if err == nil {
		t.Fatal("a cross-placement cycle was ordered")
	}
	for _, want := range []string{"api (hosted) needs db-proxy (local)", "db-proxy (local) needs api (hosted)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// A frontend whose release_dependencies is ABSENT waits for every server
// workload; one naming a peer waits only for it; peers in its own stage are
// that stage's business.
func TestDeployStageEdges_FrontendDefaults(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{orderClusterWorkload("api", nil), orderHostedWorkload("edge", nil), {Name: "cli", Runtime: RuntimeEntity{Type: RuntimeBuildOnly}}},
		Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
	}
	got, err := deployStageEdges(e)
	if err != nil {
		t.Fatal(err)
	}
	want := []stageEdge{{Dependency: "api", Dependent: "web", From: stageLocal, To: stageHosted}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v (build-only and same-stage peers add none)", got, want)
	}
}

// A deployed workload may declare a release dependency on a build-only
// artifact (control-plane prod: reliant-temporal-worker and daemon-gateway on
// workspace-base, the daemon image the release carries). The artifact has no
// stage, so the edge orders nothing; it must not refuse the deploy as a
// missing predecessor (v1.7.85 was recorded on prod and then not applied).
// A peer that names nothing at all still refuses.
func TestDeployStageEdges_BuildOnlyPeerOrdersNothing(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		orderHostedWorkload("api", nil),
		orderClusterWorkload("worker", map[string]string{"api": "v1", "workspace-base": "v1"}),
		{Name: "workspace-base", Runtime: RuntimeEntity{Type: RuntimeBuildOnly}, ReleaseDependencies: map[string]string{"worker": "v1"}},
	}}
	got, err := deployStageEdges(e)
	if err != nil {
		t.Fatalf("a dependency on a build-only artifact refused the deploy: %v", err)
	}
	want := []stageEdge{{Dependency: "api", Dependent: "worker", From: stageHosted, To: stageLocal}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
	}
	if _, err := deployStageOrder(e); err != nil {
		t.Errorf("order: %v", err)
	}
	e.Workloads[1].ReleaseDependencies["absent"] = "v1"
	if _, err := deployStageEdges(e); err == nil || !strings.Contains(err.Error(), "missing predecessor") {
		t.Errorf("a peer that names nothing was accepted: %v", err)
	}
}

// Every part reports its own placement and actuator; the env has no kind.
func TestPlacementsOf_MixedEnv(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod-k8s"}}}},
		Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
	}
	got := placementsOf(e)
	want := []partPlacement{
		{Name: "web", Kind: "frontend", Placement: placementHosted, Actuator: actuatorPlatform},
		{Name: "api", Kind: "workload", Placement: placementCluster, Cluster: "prod-k8s", Actuator: actuatorForge},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("placements = %+v, want %+v", got, want)
	}
	if s := summarizePlacements(e); !s.Mixed() {
		t.Errorf("summary = %+v, want mixed", s)
	}
}
