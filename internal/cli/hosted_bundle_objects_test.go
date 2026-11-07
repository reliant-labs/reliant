package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// A hosted env's bundle must CARRY its hosted tiers: they render no object in
// the env's own manifests, so without this the bundle is an empty tree that
// Flux applies as "nothing" and reports Ready.
func TestHostedBundleObjectsCarryTheTiersUnderTheHostedTree(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	objs := hostedObjectsFor(t, e, true)
	if len(objs) == 0 {
		t.Fatal("a hosted env's bundle carries no hosted record")
	}
	for _, o := range objs {
		if len(o.Clusters) != 1 || o.Clusters[0] != release.BundleHostedCluster {
			t.Errorf("%s %s is attributed to %v, want the hosted tree", o.Kind, o.Name, o.Clusters)
		}
		switch o.Kind {
		case "Workload", "ManagedDatabase", "StaticSite":
		default:
			t.Errorf("hosted tree carries a %s; only the three tier kinds may ride it", o.Kind)
		}
	}

	// The records survive the bundle's own parse and land at
	// release.BundleClusterPath(hosted) — the path a Kustomization reads.
	var stream strings.Builder
	if err := writeRenderedStream(&stream, objs); err != nil {
		t.Fatal(err)
	}
	built, err := bundle.Build(t.Context(), bundle.BuildInput{
		Project: "p", Env: "prod", CreatedAt: time.Unix(1700000000, 0).UTC(),
		Shape: bundle.ShapeInput{
			Kind: release.EnvPersistent, Clusters: []string{release.BundleHostedCluster},
			Manifests: stream.String(),
		},
		Provenance: release.Provenance{Repo: "r", Commit: strings.Repeat("c", 40), Tree: strings.Repeat("0", 40), ForgeVersion: "v"},
	})
	if err != nil {
		t.Fatalf("bundle.Build: %v", err)
	}
	var found bool
	for _, tree := range built.Doc.ClusterPaths {
		if tree.Cluster == release.BundleHostedCluster {
			found = true
			if tree.Path != release.BundleClusterPath(release.BundleHostedCluster) || tree.Documents != len(objs) {
				t.Errorf("hosted tree = %+v, want %d documents at %s", tree, len(objs),
					release.BundleClusterPath(release.BundleHostedCluster))
			}
		}
	}
	if !found {
		t.Errorf("the built bundle has no hosted tree: %+v", built.Doc.ClusterPaths)
	}
}

// With no release pinning the artifacts, the records are planned over
// placeholder digests and the caller is TOLD, so a bundle is never sealed over
// them.
func TestHostedBundleObjectsFlagPlaceholderDigests(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	_, placeholder, err := hostedBundleObjects(context.Background(), t.TempDir(), "prod", e)
	if err != nil {
		t.Fatalf("hostedBundleObjects: %v", err)
	}
	if !placeholder {
		t.Error("an unpinned hosted env must report placeholder digests")
	}
}

// An env with nothing hosted adds nothing to its bundle.
func TestHostedBundleObjectsAreEmptyForANonHostedEnv(t *testing.T) {
	e, _ := loadContract(t, "cluster")
	objs, _, err := hostedBundleObjects(context.Background(), t.TempDir(), "prod", e)
	if err != nil || len(objs) != 0 {
		t.Fatalf("objs = %d, err = %v; want none", len(objs), err)
	}
}

// hostedObjectsFor returns the hosted objects for the golden hosted env. When
// pinned, a release is bound in the project's ledger first, so the plan finds
// every artifact it looks up.
func hostedObjectsFor(t *testing.T, e *KCLEntities, pinned bool) []renderedObject {
	t.Helper()
	dir := t.TempDir()
	if pinned {
		group, err := buildHostedGroup("prod", e)
		if err != nil || group == nil {
			t.Fatalf("buildHostedGroup: %v", err)
		}
		resolved := map[string]string{}
		for _, svc := range group.Services {
			w := svc.Hosted
			if w != nil && (w.Tier == deploytarget.HostedTierWorkload || w.Tier == deploytarget.HostedTierStatic) {
				resolved[deploytarget.HostedArtifactKey(svc)] = "sha256:" + strings.Repeat("a", 64)
			}
		}
		if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
			Env: "prod", Release: "v1", Kind: release.KindPromote, Resolved: resolved,
		}, appendGuard{}); err != nil {
			t.Fatalf("bind a release: %v", err)
		}
	}
	objs, _, err := hostedBundleObjects(context.Background(), dir, "prod", e)
	if err != nil {
		t.Fatalf("hostedBundleObjects: %v", err)
	}
	return objs
}
