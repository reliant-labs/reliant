package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// manifestGroupStream adds to the two-cluster env the shape control-plane dev
// carries in `additional_manifests`: groups of raw manifests that NO entity
// owns. `openbao` is pinned to the control-plane cluster by the first-class
// routing label; `fake-gcs` is pinned to cp-daemon. `forge env render --list`
// prints both names in its APP column.
const manifestGroupStream = twoClusterManifests + `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: openbao
  namespace: cp-dev
  labels:
    app.kubernetes.io/name: openbao
    forge.dev/cluster: k3d-control-plane
---
apiVersion: v1
kind: Service
metadata:
  name: openbao
  namespace: cp-dev
  labels:
    app.kubernetes.io/name: openbao
    forge.dev/cluster: k3d-control-plane
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fake-gcs
  namespace: cp-dev
  labels:
    app.kubernetes.io/name: fake-gcs
    forge.dev/cluster: k3d-cp-daemon`

// TestTargetAcceptsEveryGroupRenderListShows is D2: `--target` must accept
// exactly the names `forge env render --list` attributes objects to. Before
// the fix the vocabulary was entity names only, so `--target openbao` failed
// "unknown --target openbao" while `--list` showed an openbao APP.
func TestTargetAcceptsEveryGroupRenderListShows(t *testing.T) {
	entities := renderFixture(t, twoClusterContract)
	objects, _ := attributeFixture(t, twoClusterContract, manifestGroupStream)

	for _, o := range objects {
		if o.App == "" {
			continue // env-shared: no name to target, by design
		}
		if err := validateTargetsAgainstRender(entities, []string{o.App}, manifestGroupStream); err != nil {
			t.Errorf("render --list shows APP %q for %s/%s, but --target refuses it: %v", o.App, o.Kind, o.Name, err)
		}
	}
}

// TestTargetTypoListsTheManifestGroups: the refusal names what IS available,
// manifest-only groups included and marked, so the user can see the spelling.
func TestTargetTypoListsTheManifestGroups(t *testing.T) {
	entities := renderFixture(t, twoClusterContract)
	err := validateTargetsAgainstRender(entities, []string{"openboa"}, manifestGroupStream)
	if err == nil {
		t.Fatal("a typo'd target was accepted")
	}
	for _, want := range []string{"openboa", "openbao (manifests)", "fake-gcs (manifests)", "daemon-gateway", "workspace-controller"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestTargetedDeployRoutesAManifestOnlyGroupToItsCluster is D2's routing half.
// A manifest-only target owns no entity, so the target-filtered env has no
// deploy group for it; the old path then fell through to the unscoped direct
// apply against the env's PRIMARY context — `fake-gcs`, which `--list` routes
// to cp-daemon, would have been applied to the control-plane cluster.
//
// targetedK8sGroups must dispatch exactly the cluster(s) the selection lands
// on, using the full env's groups so each apply is scoped by the full
// topology.
func TestTargetedDeployRoutesAManifestOnlyGroupToItsCluster(t *testing.T) {
	full := renderFixture(t, twoClusterContract)
	fullGroups, err := buildDeployGroups("dev", full, "cp-dev")
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{
		"fake-gcs":        "k3d-cp-daemon",
		"openbao":         "k3d-control-plane",
		"workspace-proxy": "k3d-cp-daemon",
	} {
		targets := []string{target}
		scoped, err := buildDeployGroups("dev", filterEntitiesByTarget(full, targets), "cp-dev")
		if err != nil {
			t.Fatal(err)
		}
		selected := cluster.SelectManifestsByGroup(manifestGroupStream, targets)
		got := clustersDeployedTo(targetedK8sGroups(selected, fullGroups, scoped, full))
		if strings.Join(got, ",") != want {
			t.Errorf("--target %s dispatches to %v, want [%s]", target, got, want)
		}
	}
}

// TestTargetedDeployKeepsTheFullTopologyForScoping: the dispatched group for
// a single-cluster target must still be scoped. With the target-filtered
// groups as the topology there is one cluster, clusterScopeForGroups returns
// nil, and the group applies the WHOLE selected stream — including objects
// the router sends elsewhere.
func TestTargetedDeployKeepsTheFullTopologyForScoping(t *testing.T) {
	full := renderFixture(t, twoClusterContract)
	fullGroups, err := buildDeployGroups("dev", full, "cp-dev")
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{"workspace-proxy"}
	scoped, err := buildDeployGroups("dev", filterEntitiesByTarget(full, targets), "cp-dev")
	if err != nil {
		t.Fatal(err)
	}
	dispatch := targetedK8sGroups(cluster.SelectManifestsByGroup(manifestGroupStream, targets), fullGroups, scoped, full)
	build := applyOptsBuilderFromContext(applyOptsContext{
		Groups: dispatch, Entities: filterEntitiesByTarget(full, targets),
		Topology: fullGroups, TopologyEntities: full,
	})
	for _, g := range dispatch {
		if scope := build(g).ClusterScope; scope == nil {
			t.Errorf("group %s dispatched unscoped; a targeted deploy lost its routing", g.Cluster)
		}
	}
}

// TestRestrictSecretsToDeployedClusters pins the D4 narrowing rule.
func TestRestrictSecretsToDeployedClusters(t *testing.T) {
	in := []cluster.RequiredSecret{
		{Name: "hub-only", Contexts: []string{"hub"}},
		{Name: "both", Contexts: []string{"daemon", "hub"}},
		{Name: "unattributed"},
	}
	got := restrictSecretsToDeployedClusters(in, []string{"daemon"})
	names := map[string][]string{}
	for _, rs := range got {
		names[rs.Name] = rs.Contexts
	}
	if _, ok := names["hub-only"]; ok {
		t.Errorf("a Secret only the hub consumes was kept for a daemon-only deploy: %+v", got)
	}
	if c := names["both"]; len(c) != 1 || c[0] != "daemon" {
		t.Errorf("both: contexts %v, want [daemon]", c)
	}
	if c := names["unattributed"]; len(c) != 1 || c[0] != "daemon" {
		t.Errorf("unattributed: contexts %v, want [daemon] (conservative, but only where this deploy writes)", c)
	}
	if got := restrictSecretsToDeployedClusters(in, nil); len(got) != len(in) {
		t.Errorf("no deployed clusters must leave the set untouched, got %+v", got)
	}
}
