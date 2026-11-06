package cli

import (
	"context"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/flux"
)

func platformTestEnv() (*KCLEntities, []ClusterEntity) {
	clusters := []ClusterEntity{{Name: "a", Context: "k3d-a"}}
	e := &KCLEntities{Clusters: clusters, ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-a", Namespace: "app"}}
	e.HelmCharts = []HelmChartEntity{
		{Name: "cnpg", OCI: "oci://ghcr.io/cloudnative-pg/charts/cloudnative-pg", Version: "1", Namespace: "cnpg"},
		{Name: "envoy-gateway", OCI: "oci://docker.io/envoyproxy/gateway-helm", Version: "1", Namespace: "eg", CRDs: "gateway-api"},
		// Not named "flux": detection is by chart reference.
		{Name: "gitops", OCI: flux.ChartOCI, Version: flux.ChartVersion, Namespace: flux.Namespace},
	}
	return e, clusters
}

func captureChartInstalls(t *testing.T) *[]string {
	t.Helper()
	var got []string
	restore := applyPlatformChartFn
	applyPlatformChartFn = func(_ context.Context, kctx string, spec cluster.HelmChartSpec) error {
		got = append(got, spec.Name+"@"+kctx)
		return nil
	}
	t.Cleanup(func() { applyPlatformChartFn = restore })
	return &got
}

// `cluster up` installs the DECLARED Flux (previously skipped by forge's own
// install and never replaced), Flux first and CRD providers before consumers.
func TestEnsureEnvPlatformChartsInstalled_InstallsDeclaredFluxFirst(t *testing.T) {
	got := captureChartInstalls(t)
	e, clusters := platformTestEnv()
	if err := ensureEnvPlatformChartsInstalled(context.Background(), "e2e", "proj", e, clusters); err != nil {
		t.Fatal(err)
	}
	want := []string{"gitops@k3d-a", "envoy-gateway@k3d-a", "cnpg@k3d-a"}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("installs = %v, want %v", *got, want)
	}
}

func TestEnsureEnvPlatformChartsInstalled_SkipsNonK3dClusters(t *testing.T) {
	got := captureChartInstalls(t)
	e, _ := platformTestEnv()
	clusters := []ClusterEntity{{Name: "prod", Context: "k3d-a", Provider: "gke"}}
	if err := ensureEnvPlatformChartsInstalled(context.Background(), "e2e", "proj", e, clusters); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("installed %v into a cluster forge did not create", *got)
	}
}

// `env deploy --target <chart>` installs that chart and writes no pointer.
func TestFollowPromote_TargetPlatformChartInstallsWithoutPointer(t *testing.T) {
	got := captureChartInstalls(t)
	e, _ := platformTestEnv()
	charts, rest := splitPlatformChartTargets(e, []string{"cnpg"})
	if !reflect.DeepEqual(charts, []string{"cnpg"}) || len(rest) != 0 {
		t.Fatalf("split = %v / %v", charts, rest)
	}
	if err := installPlatformCharts(context.Background(), e, platformChartInstalls(e, charts, nil), ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, []string{"cnpg@k3d-a"}) {
		t.Errorf("installs = %v", *got)
	}
	if c, r := splitPlatformChartTargets(e, []string{"api"}); len(c) != 0 || len(r) != 1 {
		t.Errorf("an app target must not be taken for a chart: %v / %v", c, r)
	}
}
