package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// THE README's FIRST SIXTY SECONDS, from a real scaffold.
//
// `forge project new` → `forge scaffold service` → `forge env up dev` is the
// headline promise: "no cluster". The scaffolded dev env runs every workload
// as a host process, but it also declares a cluster_target (k3d-<project>) for
// its Namespace and the base ingress Gateway. `forge env up` read that
// declaration as "deploys to a cluster": with no `k3d-<project>` context it
// refused ("declares cluster … but no such kubectl context exists"), and once
// a user ran `forge cluster up` by hand it refused again trying to push a
// project image no pod would ever pull. The only way through was
// `--no-deploy`.
//
// This renders the scaffolded dev env through the production decoder and
// pins the decision `forge env up` makes from it: deploy the host infra, need
// NO cluster, and name the declared-but-unneeded one. The counterfactual —
// binding a workload to the cluster — must flip it back, or the predicate
// would pass by never asking for a cluster at all.
func TestScaffoldedDevEnvNeedsNoCluster(t *testing.T) {
	kclplugin.Register()
	dir := scaffoldWorkloadModelProject(t)

	renderDev := func() *KCLEntities {
		t.Helper()
		kclplugin.UsePortStoreReadOnly(filepath.Join(dir, ".forge", "ports-dev.json"))
		raw, err := kclrender.Run(dir, filepath.Join(dir, "deploy", "kcl", "dev"), []string{"env=dev"})
		if err != nil {
			t.Fatalf("render dev: %v", err)
		}
		e, err := parseKCLEntities(raw)
		if err != nil {
			t.Fatalf("decode dev: %v", err)
		}
		return e
	}

	dev := renderDev()
	// Fixture sanity: this IS the shape that failed — a declared k3d
	// cluster_target beside workloads that all run on the host.
	if got := dev.ClusterTarget.field("cluster"); got != "k3d-acme" {
		t.Fatalf("fixture is wrong: scaffolded dev must declare cluster_target k3d-acme, got %q", got)
	}
	if !kclEntitiesHaveK8sCluster(dev) {
		t.Fatal("fixture is wrong: the scaffolded dev env must still READ as cluster-shaped to the deploy guard")
	}

	if demand := envClusterDemand(dev); len(demand) != 0 {
		t.Errorf("nothing in the scaffolded dev env runs in a cluster, but demand = %v", demand)
	}
	got := targetPhaseRequirements(dev, nil)
	want := upPhaseRequirements{deploy: true, cluster: false}
	if got != want {
		t.Errorf("forge env up dev on a fresh scaffold: requirements = %+v, want %+v (host infra deploys, no cluster)", got, want)
	}
	if got := declaredButUnneededClusters(dev); !reflect.DeepEqual(got, []string{"k3d-acme"}) {
		t.Errorf("the skipped cluster must be named, got %v", got)
	}

	// Counterfactual: bind the API (the binary's `server`, running item) to the local cluster.
	mainK := filepath.Join(dir, "deploy", "kcl", "dev", "main.k")
	src, err := os.ReadFile(mainK)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "_on_host(_api)") {
		t.Fatalf("fixture is wrong: dev/main.k does not bind _api with _on_host:\n%s", src)
	}
	if err := os.WriteFile(mainK, []byte(strings.Replace(string(src), "_on_host(_api)", "_on_k3d(_api)", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	onCluster := renderDev()
	if got := targetPhaseRequirements(onCluster, nil); !got.cluster || !got.deploy {
		t.Errorf("a workload bound to k3d must require the cluster, got %+v (demand %v)", got, envClusterDemand(onCluster))
	}
	if got := declaredButUnneededClusters(onCluster); len(got) != 0 {
		t.Errorf("a needed cluster must not be reported as unneeded, got %v", got)
	}
}

// The demand rules, one per kind of work. A Namespace and the env's own
// Gateway are support; everything else that lands on a cluster is work.
func TestEnvPhaseRequirements(t *testing.T) {
	scaffoldedDev := func() *KCLEntities {
		return &KCLEntities{
			ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-acme", Namespace: "acme-dev"},
			Workloads:     []WorkloadEntity{hostWL("billing"), hostWL("migrate")},
			Infra:         []HostInfraEntity{{Name: "postgres", Engine: "postgres"}},
			Gateways:      []GatewayEntity{{Name: "public"}},
		}
	}

	tests := []struct {
		name   string
		mutate func(*KCLEntities)
		want   upPhaseRequirements
	}{
		{name: "scaffolded dev: host workloads + host infra", want: upPhaseRequirements{deploy: true}},
		{name: "nothing to bring up at all", mutate: func(e *KCLEntities) { e.Infra = nil }, want: upPhaseRequirements{}},
		{name: "compose service, no infra", mutate: func(e *KCLEntities) {
			e.Infra = nil
			e.Workloads = append(e.Workloads, composeWL("redis", "docker-compose.yml"))
		}, want: upPhaseRequirements{deploy: true}},
		{name: "workload on the cluster", mutate: func(e *KCLEntities) {
			e.Workloads = append(e.Workloads, clusterWL("reaper", "k3d-acme", "acme-dev"))
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "route to a host process", mutate: func(e *KCLEntities) {
			e.HTTPRoutes = []HTTPRouteEntity{{Name: "api", Gateway: "public"}}
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "self-managed database", mutate: func(e *KCLEntities) {
			e.Databases = []DatabaseEntity{{Name: "db", Runtime: RuntimeCluster}}
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "hosted database is not cluster work", mutate: func(e *KCLEntities) {
			e.Databases = []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted}}
		}, want: upPhaseRequirements{deploy: true}},
		{name: "platform chart", mutate: func(e *KCLEntities) {
			e.HelmCharts = []HelmChartEntity{{Name: "envoy-gateway"}}
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "placed Secret", mutate: func(e *KCLEntities) {
			e.RenderedSecrets = []RenderedSecretEntity{{Name: "creds"}}
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "a k3d cluster the Bundle asks forge to create", mutate: func(e *KCLEntities) {
			e.Clusters = []ClusterEntity{{Name: "acme"}}
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
		{name: "a raw manifest in the rendered stream", mutate: func(e *KCLEntities) {
			e.ClusterWork = []string{"ConfigMap/settings"}
		}, want: upPhaseRequirements{deploy: true, cluster: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := scaffoldedDev()
			if tc.mutate != nil {
				tc.mutate(e)
			}
			if got := envPhaseRequirements(e); got != tc.want {
				t.Errorf("envPhaseRequirements = %+v, want %+v (demand %v)", got, tc.want, envClusterDemand(e))
			}
		})
	}

	// No render, no declaration to read: keep the full reconcile.
	if got := envPhaseRequirements(nil); got != (upPhaseRequirements{deploy: true, cluster: true}) {
		t.Errorf("a failed render must keep the full reconcile, got %+v", got)
	}
}

// The stream half of the demand: what the render would WRITE to a cluster,
// minus the env's own support objects.
func TestManifestClusterWork(t *testing.T) {
	m := func(kind, name string) rawManifest {
		var r rawManifest
		r.Kind = kind
		r.Metadata.Name = name
		return r
	}
	got := manifestClusterWork([]rawManifest{
		m("Namespace", "acme-dev"),
		m("Gateway", "public"),   // declared in Bundle.gateways: support
		m("Gateway", "internal"), // carried by a raw forge.Manifests group: work
		m("Workload", "reaper"),
		m("HTTPRoute", "api"),
	}, []GatewayEntity{{Name: "public"}})
	want := []string{"Gateway/internal", "Workload/reaper", "HTTPRoute/api"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifestClusterWork = %v, want %v", got, want)
	}
}

// Leaving a declared cluster alone is a stated decision, never a silence.
func TestClusterNotNeededNote(t *testing.T) {
	if note := clusterNotNeededNote("dev", nil); note != "" {
		t.Errorf("an env that declares no cluster needs no note, got %q", note)
	}
	note := clusterNotNeededNote("dev", []string{"k3d-acme"})
	for _, want := range []string{"k3d-acme", "nothing in env dev runs in a cluster", "forge env deploy dev"} {
		if !strings.Contains(note, want) {
			t.Errorf("note must mention %q:\n%s", want, note)
		}
	}
}

// With skipClusterApply the deploy keeps exactly what runs off a cluster.
func TestWithoutClusterGroups(t *testing.T) {
	got := withoutClusterGroups([]deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Cluster: "k3d-acme"},
		{ProviderID: "host-infra"},
		{ProviderID: "compose"},
	})
	if len(got) != 2 || got[0].ProviderID != "host-infra" || got[1].ProviderID != "compose" {
		t.Errorf("withoutClusterGroups kept %+v, want host-infra + compose", got)
	}
}

// The local image is built and pushed only when a pod will pull it. A
// cluster holding only support objects or a vendored image does not — and
// pushing anyway failed the dev deploy with a message that the env
// "declares no workloads".
func TestEnvRunsProjectImageOnCluster(t *testing.T) {
	if !envRunsProjectImageOnCluster(nil) {
		t.Error("a failed render must keep the build+push")
	}
	if envRunsProjectImageOnCluster(&KCLEntities{Workloads: []WorkloadEntity{hostWL("billing")}}) {
		t.Error("host-only workloads pull no image")
	}
	vendored := clusterWL("redis", "k3d-acme", "acme-dev")
	vendored.Build = BuildConfigEntity{}
	if envRunsProjectImageOnCluster(&KCLEntities{Workloads: []WorkloadEntity{vendored}}) {
		t.Error("a cluster workload with no forge build runs a vendored image, not the project's")
	}
	built := clusterWL("reaper", "k3d-acme", "acme-dev")
	if built.GoBuild() == nil {
		t.Fatal("fixture is wrong: clusterWL must carry a Go build")
	}
	if !envRunsProjectImageOnCluster(&KCLEntities{Workloads: []WorkloadEntity{built}}) {
		t.Error("a forge-built cluster workload runs the project image")
	}
}

// A missing `k3d-` context is a local cluster forge creates: the refusal's
// first fix must say so, not send the user to fetch cloud credentials.
func TestDeclaredContextExistsVerdict_K3dNamesClusterUp(t *testing.T) {
	err := declaredContextExistsVerdict("dev", "k3d-acme", nil)
	if err == nil {
		t.Fatal("a missing declared context must refuse")
	}
	if !strings.Contains(err.Error(), "forge cluster up") {
		t.Errorf("a missing k3d context must name `forge cluster up`:\n%v", err)
	}
	cloud := declaredContextExistsVerdict("prod", "gke_acme_us-central1_prod", nil)
	if cloud == nil || strings.Contains(cloud.Error(), "forge cluster up") {
		t.Errorf("a cloud context is not created by forge cluster up:\n%v", cloud)
	}
}
