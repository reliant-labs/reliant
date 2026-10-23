package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// clusterTargetJSON is the render shape that exposed the defect: the env's
// WORKLOADS are agnostic forge.Services (folded into `services` AFTER
// `bundle.services`), and `bundle.services` holds an image-less infra service
// that pins its manifests to a SECOND cluster. forge.render emits
// `services = bundle.services + projected(bundle.workloads)`, so the infra
// service comes first in the list even though it is the exception, not the
// env.
//
// The env's declared target — the Bundle's `cluster_target` — names the
// control-plane cluster and namespace. That declaration is what every
// env-wide question must be answered from.
const clusterTargetJSON = `{
  "output": {
    "cluster_target": {
      "cluster": "gke_proj_us-central1_prod",
      "namespace": "control-plane-prod",
      "platform": "amd64",
      "image_tag": "stable"
    },
    "workloads": [
      {
        "name": "workspace-base",
        "kind": "tool",
        "runtime": {
          "type": "build-only",
          "build_variants": []
        },
        "spec": {
          "kind": "tool"
        }
      },
      {
        "name": "kata-prepull",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "gke_proj_us-central1-a_prod-daemon-v2",
          "namespace": "kata-prepull",
          "platform": "arm64"
        },
        "spec": {
          "kind": "service"
        }
      },
      {
        "name": "admin-server",
        "kind": "service",
        "image": "control-plane",
        "runtime": {
          "type": "cluster",
          "cluster": "gke_proj_us-central1_prod",
          "namespace": "control-plane-prod",
          "platform": "amd64"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`

func parseClusterTargetFixture(t *testing.T) *KCLEntities {
	t.Helper()
	entities, err := parseKCLEntities([]byte(clusterTargetJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	return entities
}

// TestParseKCLEntities_ClusterTarget pins that the declared env target
// survives the parse. Without it every consumer below is left to guess.
func TestParseKCLEntities_ClusterTarget(t *testing.T) {
	e := parseClusterTargetFixture(t)
	if e.ClusterTarget == nil {
		t.Fatal("cluster_target was dropped during parse")
	}
	if e.ClusterTarget.Namespace != "control-plane-prod" || e.ClusterTarget.Cluster != "gke_proj_us-central1_prod" {
		t.Errorf("cluster_target parsed as %+v", *e.ClusterTarget)
	}
}

// TestK8sClusterField_PrefersDeclaredClusterTarget is the regression. Before
// the fix, the env namespace resolved to the FIRST cluster-shaped service's —
// the infra service's "kata-prepull" — and `forge env render/deploy prod`
// silently re-rendered every prod object into that namespace. The cluster,
// registry and domain fields go through the same resolver and had the same
// defect.
func TestK8sClusterField_PrefersDeclaredClusterTarget(t *testing.T) {
	e := parseClusterTargetFixture(t)
	for field, want := range map[string]string{
		"namespace": "control-plane-prod",
		"cluster":   "gke_proj_us-central1_prod",
	} {
		if got := k8sClusterFieldFromEntities(e, field); got != want {
			t.Errorf("%s: got %q, want the declared cluster_target's %q", field, got, want)
		}
	}
}

// TestMainClusterForEntities_PrefersDeclaredClusterTarget: operators and
// cronjobs carry no deploy block and are attributed to the env's main
// cluster. That must be the declared target, not whichever cross-cluster
// service happens to render first — otherwise the workspace operator is
// applied to the daemon cluster and dropped from its own.
func TestMainClusterForEntities_PrefersDeclaredClusterTarget(t *testing.T) {
	e := parseClusterTargetFixture(t)
	if got := mainClusterForEntities(e, nil); got != "gke_proj_us-central1_prod" {
		t.Errorf("main cluster: got %q, want the declared cluster_target's", got)
	}
}

// TestKCLFirstClusterPlatform_PrefersDeclaredClusterTarget: the build arch for
// the env's project image is the env's declared platform, not a secondary
// cluster's.
func TestKCLFirstClusterPlatform_PrefersDeclaredClusterTarget(t *testing.T) {
	e := parseClusterTargetFixture(t)
	if got := kclFirstClusterPlatform(e); got != "amd64" {
		t.Errorf("platform: got %q, want the declared cluster_target's amd64", got)
	}
}

// TestDeclaredEnvContext_PrefersDeclaredClusterTarget: the env-wide kubectl
// context (the deploy preflight's target, the secrets pre-apply) is
// the declared cluster_target's, not the first deploy group's. Groups are
// ordered by their SORTED key, `k8s-cluster|<context>|…`, so the first group
// is whichever context sorts lowest. That is unrelated to which cluster is the
// env's: GKE names a zonal cluster `gke_<p>_us-central1-a_<c>` and a regional
// one `gke_<p>_us-central1_<c>`, and '-' sorts before '_', so a zonal
// secondary cluster always wins. Before the fix, adding a second cluster to
// prod pointed the deploy preflight at it, and the preflight failed on prod's
// CRDs and Secrets missing from the daemon cluster.
func TestDeclaredEnvContext_PrefersDeclaredClusterTarget(t *testing.T) {
	e := parseClusterTargetFixture(t)
	groups := []deploytarget.ServiceGroup{
		k8sGroup("gke_proj_us-central1-a_prod-daemon-v2", "control-plane-prod"),
		k8sGroup("gke_proj_us-central1_prod", "control-plane-prod"),
	}
	if got := declaredEnvContext(e, groups); got != "gke_proj_us-central1_prod" {
		t.Errorf("env context: got %q, want the declared cluster_target's gke_proj_us-central1_prod", got)
	}
	// No cluster_target: the first declared group, as before.
	if got := declaredEnvContext(nil, groups); got != "gke_proj_us-central1-a_prod-daemon-v2" {
		t.Errorf("no cluster_target: got %q, want the first group's", got)
	}
}

// TestHelmChartsRideTheDeclaredClusterTargetGroup: an env's platform deps
// (forge.HelmChart: cert-manager, the gateway controller) apply ONCE, with
// the group whose context is the env's. That must be the declared
// cluster_target's group, not the first group in sorted order. The daemon
// cluster's zonal context sorts first, so before the fix a bare
// `forge env deploy prod` would have installed prod's cert-manager and
// gateway charts onto the daemon cluster and skipped prod.
func TestHelmChartsRideTheDeclaredClusterTargetGroup(t *testing.T) {
	daemon := k8sGroup("gke_proj_us-central1-a_prod-daemon-v2", "control-plane-prod")
	prod := k8sGroup("gke_proj_us-central1_prod", "control-plane-prod")
	builder := applyOptsBuilderFromContext(applyOptsContext{
		Groups:     []deploytarget.ServiceGroup{daemon, prod},
		Entities:   parseClusterTargetFixture(t),
		HelmCharts: []cluster.HelmChartSpec{{Name: "cert-manager"}},
	})
	if got := builder(daemon).HelmCharts; len(got) != 0 {
		t.Errorf("the daemon cluster's group carries the env's platform deps %v; they belong to the declared target", got)
	}
	if got := builder(prod).HelmCharts; len(got) != 1 {
		t.Errorf("the declared target's group carries %d platform deps, want 1", len(got))
	}
}

// TestK8sClusterField_NoClusterTargetKeepsServiceFallback: an env that
// declares no cluster_target (a hand-rolled contract, the in-tree test
// fixtures) still resolves from its services exactly as before.
func TestK8sClusterField_NoClusterTargetKeepsServiceFallback(t *testing.T) {
	e, err := parseKCLEntities([]byte(`{
  "output": {
    "workloads": [
      {
        "name": "a",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-x",
          "namespace": "ns-x",
          "platform": "arm64"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if e.ClusterTarget != nil {
		t.Fatalf("no cluster_target declared, parsed %+v", *e.ClusterTarget)
	}
	if got := k8sClusterFieldFromEntities(e, "namespace"); got != "ns-x" {
		t.Errorf("namespace fallback: got %q", got)
	}
	if got := mainClusterForEntities(e, nil); got != "k3d-x" {
		t.Errorf("main cluster fallback: got %q", got)
	}
	if got := kclFirstClusterPlatform(e); got != "arm64" {
		t.Errorf("platform fallback: got %q", got)
	}
}
