package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func applySnapshotFixture(t *testing.T) release.Snapshot {
	t.Helper()
	d := "sha256:" + strings.Repeat("a", 64)
	a := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.example/reliant", Content: d, Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", Source: &release.Source{Repo: "example/reliant", Commit: strings.Repeat("b", 40)}}}
	id, err := release.ArtifactIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	k := release.ConsumerKey{Cluster: "prod", Workload: "worker"}
	spec := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: worker\n  namespace: prod\nspec:\n  template:\n    spec:\n      containers:\n      - name: worker\n        image: registry.example/reliant@" + d + "\n")
	return release.Snapshot{Schema: release.SnapshotSchema, Project: "p", Environment: "prod", Source: release.Source{Repo: "example/p", Commit: strings.Repeat("b", 40)}, PolicyDigest: d, Consumers: []release.Consumer{{Key: k, Slots: []string{"container:worker"}, DependenciesDeclared: true}}, Bindings: []release.ConsumerBinding{{Consumer: k, Slot: "container:worker", Artifact: id}}, Selected: []release.ConsumerKey{k}, Closure: []release.ConsumerKey{k}, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: a}, Resources: []release.SnapshotResource{{Key: release.ResourceKey{Cluster: "prod", Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "worker"}, Spec: spec, Digest: release.SpecDigest(spec), Tree: release.BundleClusterPath("prod"), Phase: "rollout", Owners: []release.ConsumerKey{k}}}, Decisions: []release.CompositionDecision{{Consumer: k, Action: "build", Reason: "fixture"}}}
}
func TestSnapshotApplyUsesRecordedBytesWithoutKCL(t *testing.T) {
	s := applySnapshotFixture(t)
	var seen string
	err := ApplySnapshot(context.Background(), s, "prod", ApplyOpts{MainK: "missing.k", Context: "prod", Env: "prod", DryRun: true, OnStream: func(stream string) { seen = stream }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, string(s.Resources[0].Spec)) {
		t.Fatal("recorded workload bytes changed")
	}
}
func TestSnapshotApplyRejectsUnrelatedActionsBeforeWriting(t *testing.T) {
	s := applySnapshotFixture(t)
	for _, opts := range []ApplyOpts{{Context: "wrong", Env: "prod"}, {Context: "prod", Env: "prod", Targets: []string{"worker"}}, {Context: "prod", Env: "prod", Prune: true}, {Context: "prod", Env: "prod", HelmCharts: []HelmChartSpec{{Name: "other"}}}} {
		if err := ApplySnapshot(context.Background(), s, "prod", opts); err == nil {
			t.Fatal("accepted unsafe snapshot apply")
		}
	}
}
