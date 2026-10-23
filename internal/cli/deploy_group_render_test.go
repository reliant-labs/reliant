package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

// collidingGroupNamespaceMain is control-plane's prod reduced to the part that
// broke its rollout. The env namespace is read from KCL's `namespace` option
// (the idiom every scaffolded env uses), a second cluster's workload lives in
// namespace "observability", and the env ALSO declares a Namespace named
// "observability" on the first cluster, next to an override that targets the
// env namespace on that cluster (prod's prune guard).
//
// Rendered with the env namespace ("app") there is exactly one
// Namespace/app@ctx-hub and one Namespace/observability@ctx-hub. Re-rendered
// with the SECOND cluster's group namespace bound to `namespace`, the env's own
// namespace becomes "observability" and the override key
// "Namespace/observability@ctx-hub" addresses two objects.
const collidingGroupNamespaceMain = `
import forge
import forge.workloads as fw

_ns = option("namespace") or "app"
_hub = forge.ClusterTarget {cluster = "ctx-hub", namespace = _ns}
_daemon = forge.ClusterTarget {cluster = "ctx-daemon", namespace = "observability"}

output = forge.render(forge.Bundle {
    project = "repro"
    cluster_target = _hub
    workloads = [
        fw.Workload {name = "api", image = "docker.io/library/busybox:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _hub}}
        fw.Workload {name = "agent", image = "docker.io/library/busybox:1", ports = [fw.Port {name = "http", port = 8081}], runtime = forge.OnCluster {target = _daemon}}
    ]
    manifests = [
        forge.Manifests {
            name = "obs-ns"
            cluster = "ctx-hub"
            objects = [{apiVersion = "v1", kind = "Namespace", metadata.name = "observability"}]
        }
    ]
    overrides = {
        "Namespace/${_ns}@ctx-hub" = {metadata.annotations = {"prune" = "off"}}
    }
})
`

// TestDeployGroups_RenderOnceWithTheEnvNamespace is the prod blocker.
//
// `forge env render` rendered the env once; `forge env deploy` rendered it
// once PER cluster group, binding the group's namespace to the `namespace`
// option. A group is where an apply goes and what it waits on — not an input
// to what the env declares — so the second group's render described a
// different env, and the deploy failed (override "Namespace/observability@…"
// matches 2 objects) after `forge env render` had passed.
//
// The test drives the deploy's own dispatch (the same builder, provider and
// scoping the real path uses) in dry-run and asserts both halves of the
// contract: nothing fails, and every cluster receives EXACTLY the partition of
// the one env render.
func TestDeployGroups_RenderOnceWithTheEnvNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	ctx := context.Background()
	dir := writeKCLProject(t, collidingGroupNamespaceMain)
	mainK := dir + "/main.k"

	entities, err := parseKCLEntities(renderKCLProject(t, dir, `namespace="app"`, `env="prod"`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	groups, err := buildDeployGroups("prod", entities, "app")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	var k8s []string
	for _, g := range groups {
		if g.ProviderID == "k8s-cluster" {
			k8s = append(k8s, g.Cluster+"/"+g.Namespace)
		}
	}
	if got, want := strings.Join(k8s, " "), "ctx-daemon/observability ctx-hub/app"; got != want {
		t.Fatalf("precondition: the daemon group must carry the namespace the hub also declares, got %q want %q", got, want)
	}

	// What `forge env render` prints: the env, rendered once with the env namespace.
	envRender, err := cluster.RenderManifests(ctx, mainK, "t", "app", "prod", nil, nil)
	if err != nil {
		t.Fatalf("the env must render with its own namespace (forge env render passes): %v", err)
	}

	var mu sync.Mutex
	streams := map[string]string{}
	var current string
	builder := applyOptsBuilderFromContext(applyOptsContext{
		MainK: mainK, ImageTag: "t", FallbackNamespace: "app", Env: "prod", DryRun: true,
		Groups: groups, Entities: entities,
		OnStream: func(s string) {
			mu.Lock()
			defer mu.Unlock()
			streams[current] = s
		},
	})
	registry := deploytarget.NewRegistry()
	registry.Register(deploytarget.K8sClusterProvider{ApplyOptsBuilder: func(g deploytarget.ServiceGroup) cluster.ApplyOpts {
		current = g.Cluster
		return builder(g)
	}})
	captureStdout(t, func() {
		err = dispatchDeployGroups(ctx, registry, groups)
	})
	if err != nil {
		t.Fatalf("a cluster group whose namespace the env also declares elsewhere must not change what the env renders, got: %v", err)
	}

	scopeFor := clusterScopeForGroups(groups, entities)
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" {
			continue
		}
		want := cluster.ScopeManifestsToGroup(envRender, *scopeFor(g))
		if got := streams[g.Cluster]; got != want {
			t.Errorf("cluster %s applies a stream that is not its partition of the env render:\n--- applied ---\n%s\n--- render, scoped ---\n%s",
				g.Cluster, got, want)
		}
	}
	if !strings.Contains(streams["ctx-hub"], "name: observability") || strings.Contains(streams["ctx-daemon"], "name: app\n") {
		t.Errorf("hub must receive its declared Namespace/observability and the daemon must not receive the hub's env namespace:\nhub:\n%s\ndaemon:\n%s", streams["ctx-hub"], streams["ctx-daemon"])
	}
}

// laterGroupRefusedMain is a two-cluster env whose SECOND group (ctx-hub sorts
// after ctx-daemon) carries a Job with a deploy phase forge does not know. The
// apply refuses that group — but only after it has applied the first.
const laterGroupRefusedMain = `
import forge
import forge.workloads as fw

_ns = option("namespace") or "app"
_hub = forge.ClusterTarget {cluster = "ctx-hub", namespace = _ns}
_daemon = forge.ClusterTarget {cluster = "ctx-daemon", namespace = "agents"}

output = forge.render(forge.Bundle {
    project = "repro"
    cluster_target = _hub
    workloads = [
        fw.Workload {name = "api", image = "docker.io/library/busybox:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _hub}}
        fw.Workload {name = "agent", image = "docker.io/library/busybox:1", ports = [fw.Port {name = "http", port = 8081}], runtime = forge.OnCluster {target = _daemon}}
    ]
    manifests = [
        forge.Manifests {
            name = "bad-phase"
            cluster = "ctx-hub"
            objects = [{
                apiVersion = "batch/v1", kind = "Job"
                metadata = {name = "migrate", annotations = {"forge.dev/deploy-phase" = "pre-rolout"}}
                spec.template.spec = {restartPolicy = "Never", containers = [{name = "m", image = "docker.io/library/busybox:1"}]}
            }]
        }
    ]
})
`

// TestRenderGate_RefusesBeforeAnythingIsWritten is the ledger half of the
// prod incident: a deploy recorded its promotion (prod → release X), THEN found
// a group it could not apply, and the ledger claimed a release that was never
// live.
//
// The pre-record gate runs every cluster group through the apply's own path as
// a dry run. The test asserts the two facts that make it worth having: the
// refusal happens in the gate (so runPromote, which runs the gate first, writes
// no promotion), and the gate touched no cluster; while the real apply of the
// same groups DID write the first cluster before refusing the second — which is
// the state the old ordering left behind.
func TestRenderGate_RefusesBeforeAnythingIsWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	ctx := context.Background()
	dir := writeKCLProject(t, laterGroupRefusedMain)
	mainK := dir + "/main.k"
	entities, err := parseKCLEntities(renderKCLProject(t, dir, `namespace="app"`, `env="prod"`))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	groups, err := buildDeployGroups("prod", entities, "app")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	if len(groups) != 2 || groups[0].Cluster != "ctx-daemon" || groups[1].Cluster != "ctx-hub" {
		t.Fatalf("precondition: the refused group must be dispatched second, got %+v", groups)
	}
	calls := fakeCrossClusterKubectl(t)
	in := deployApplyInput{
		groups: groups, entities: entities, hasK8sServices: true,
		mainK: mainK, imageTag: "t", namespace: "app", envName: "prod",
		rollout: cluster.RolloutPolicy{Timeout: time.Second},
		report:  newDeployReport("prod", false),
	}

	var gateErr error
	captureStdout(t, func() { gateErr = renderGateDeployGroups(ctx, in) })
	if gateErr == nil || !strings.Contains(gateErr.Error(), "pre-rolout") {
		t.Fatalf("the gate must refuse the group the apply would refuse, naming why, got: %v", gateErr)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("the render gate must not touch a cluster, kubectl was called:\n%s", strings.Join(got, "\n"))
	}

	// The counterfactual — what recording first used to leave behind.
	in.groups, err = buildDeployGroups("prod", entities, "app")
	if err != nil {
		t.Fatal(err)
	}
	var applyErr error
	captureStdout(t, func() { applyErr = applyDeployGroups(ctx, in) })
	if applyErr == nil {
		t.Fatal("the real apply must refuse the same group")
	}
	var wroteDaemon bool
	for _, c := range calls() {
		wroteDaemon = wroteDaemon || strings.HasPrefix(c, "ctx-daemon apply ")
	}
	if !wroteDaemon {
		t.Fatal("expected the real apply to have written ctx-daemon before refusing ctx-hub: the premise of the ordering fix")
	}
}
