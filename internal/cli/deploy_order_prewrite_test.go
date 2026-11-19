package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// control-plane v1.7.85's prod graph, exactly as its KCL renders it: three
// cluster workloads that each name the others' contracts, and the build-only
// daemon image (workspace-base) that two of them, and it in turn, name. The
// apply refused it with `release component reliant-temporal-worker has missing
// predecessor "workspace-base"` after prod's binding had moved to it. It is one
// stage with no ordering edge: every pair is same-stage or names a build-only
// artifact.
func v1785ProdGraph() *KCLEntities {
	return &KCLEntities{Workloads: []WorkloadEntity{
		orderClusterWorkload("reliant-api-server", map[string]string{
			"daemon-gateway":          "reliant/api-gateway-nats/v1",
			"reliant-temporal-worker": "reliant/api-worker-db-temporal/v1",
		}),
		orderClusterWorkload("reliant-temporal-worker", map[string]string{
			"daemon-gateway":     "reliant/worker-gateway-nats/v1",
			"reliant-api-server": "reliant/api-worker-db-temporal/v1",
			"workspace-base":     "reliant/worker-daemon-tools/v1",
		}),
		orderClusterWorkload("daemon-gateway", map[string]string{
			"reliant-api-server":      "reliant/api-gateway-nats/v1",
			"reliant-temporal-worker": "reliant/worker-gateway-nats/v1",
			"workspace-base":          "reliant/gateway-daemon-stream/v1",
		}),
		{Name: "workspace-base", Runtime: RuntimeEntity{Type: RuntimeBuildOnly}, ReleaseDependencies: map[string]string{
			"daemon-gateway":          "reliant/gateway-daemon-stream/v1",
			"reliant-temporal-worker": "reliant/worker-daemon-tools/v1",
		}},
	}}
}

func TestDeployStageOrder_ControlPlaneV1785ProdGraph(t *testing.T) {
	e := v1785ProdGraph()
	edges, err := deployStageEdges(e)
	if err != nil {
		t.Fatalf("v1.7.85's prod graph refused: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("v1.7.85's prod graph ordered something: %+v (all same-stage or build-only)", edges)
	}
	order, err := deployStageOrder(e)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if strings.Join(stageNames(order), ",") != "local,hosted,frontends" {
		t.Errorf("order = %v, want the default local,hosted,frontends", order)
	}
	decl, err := bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatalf("bundle placement declarations refused v1.7.85's prod graph: %v", err)
	}
	for _, d := range decl {
		if len(d.Dependencies) != 0 {
			t.Errorf("%s carries placement predecessors %v; a single-stage graph has none", d.Workload, d.Dependencies)
		}
	}
}

func stageNames(order []deployStage) []string {
	out := make([]string, 0, len(order))
	for _, s := range order {
		out = append(out, string(s))
	}
	return out
}

// THE ORDER IS DECIDED BEFORE THE PROMOTION IS RECORDED. The pre-write
// preflight (preflightOnly) is the run whose refusal leaves the binding
// unmoved (TestDeployRelease_PreflightRefusalRecordsNothing). An order the
// deploy cannot place must fail THAT run, not the apply after it — v1.7.85 was
// bound on prod and then refused by the apply's order check, so the ledger
// named a release nothing had shipped. It also fails before any kubectl call.
func TestRunDeploy_PreflightRefusesAnUnplaceableOrderBeforeTheWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL and runs the deploy; runs in task test")
	}
	requirePOSIXFake(t, "kubectl")
	kclplugin.Register()
	dir := workloadURLProject(t, "prod", `    project = "acme"
    env = "prod"
    cluster_target = forge.ClusterTarget { cluster = "gke_hub", namespace = "acme-prod" }
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "gke_hub", namespace = "acme-prod"}}
        release_dependencies = {absent = "acme/absent/v1"}
    }]`)
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\ncat >/dev/null 2>&1\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(dir)
	useLedgerHome(t, t.TempDir())
	writeProjectName(t, dir, "acme")

	var err error
	captureStdout(t, func() {
		err = runDeploy(context.Background(), "prod", deployOptions{dryRun: true, preflightOnly: true, imageTag: "v1"})
	})
	if err == nil || !strings.Contains(err.Error(), `missing predecessor "absent"`) {
		t.Fatalf("the pre-write preflight accepted an order the apply would refuse: %v", err)
	}
	if data, _ := os.ReadFile(log); len(data) > 0 {
		t.Errorf("an unplaceable order reached kubectl before it was refused:\n%s", data)
	}
}
