package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestSnapshotApplyRefusesPermissiveRolloutBeforePromotion(t *testing.T) {
	for _, mode := range []cluster.RolloutMode{cluster.RolloutSkip, cluster.RolloutWarn} {
		t.Run(string(mode), func(t *testing.T) {
			err := executeSnapshotPromotion(context.Background(), release.Release{}, envLedger{}, promoteOptions{Follow: &promoteFollowOptions{clientDeploy: deployOptions{rollout: cluster.RolloutPolicy{Mode: mode}}}}, release.Plan{}, "base", nil, &release.Snapshot{})
			if err == nil || !strings.Contains(err.Error(), "strict rollout") {
				t.Fatalf("permissive mode reached promotion: %v", err)
			}
		})
	}
}
func TestSnapshotApplyRefusesCrossClusterDeliveryBeforePromotion(t *testing.T) {
	for _, units := range [][]release.DeliveryUnit{nil, {{Name: "app", After: nil}}, {{Name: "app", After: []string{"base"}}}} {
		t.Run(fmt.Sprintf("units-%v", units), func(t *testing.T) {
			s := implicitDeliveryFixture(units)
			_, trees, err := release.SnapshotDelivery(s)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tree := range trees {
				if tree.Unit == "app" && len(tree.DependsOn) > 0 {
					found = true
				}
			}
			if !found {
				t.Fatal("fixture lacks resolved implicit base edge")
			}
			writes := &compositionAppendBinding{}
			old := snapshotClusterApply
			t.Cleanup(func() { snapshotClusterApply = old })
			actuated := false
			snapshotClusterApply = func(context.Context, release.Snapshot, string, cluster.ApplyOpts) error { actuated = true; return nil }
			err = executeSnapshotPromotion(context.Background(), release.Release{}, envLedger{Bindings: writes}, promoteOptions{Follow: &promoteFollowOptions{}}, release.Plan{}, "base", nil, &s)
			if err == nil || !strings.Contains(err.Error(), "delivery graphs") || writes.last.Env != "" || actuated {
				t.Fatalf("resolved cross-cluster graph reached append/actuator: %v writes=%+v actuator=%v", err, writes.last, actuated)
			}
		})
	}
}
func implicitDeliveryFixture(units []release.DeliveryUnit) release.Snapshot {
	s := release.Snapshot{DeliveryUnits: units}
	for _, input := range []struct{ cluster, unit string }{{"z-substrate", "base"}, {"a-hub", "app"}} {
		body := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\n  namespace: app\n  labels:\n    forge.dev/delivery-unit: " + input.unit + "\n")
		s.Resources = append(s.Resources, release.SnapshotResource{Key: release.ResourceKey{Cluster: input.cluster, Kind: "ConfigMap", Namespace: "app", Name: "config"}, Spec: body, Phase: "pre", Tree: release.BundleTreePath(input.cluster, input.unit, release.PhasePre)})
	}
	return s
}
func TestCompositionDockerOutputsKeepConsumerAndSharedUnitAssociations(t *testing.T) {
	old := compositionDockerBuilder
	t.Cleanup(func() { compositionDockerBuilder = old })
	calls := 0
	digest := "sha256:" + strings.Repeat("b", 64)
	compositionDockerBuilder = func(_ context.Context, _ *config.ProjectConfig, name, repo string, _ *DockerBuild, _ buildOptions, _, tag string) buildResult {
		calls++
		if tag != "v2" {
			t.Fatalf("effective release tag lost: %s", tag)
		}
		return buildResult{name: name + " (docker)", image: name, repository: repo, digest: digest}
	}
	a := WorkloadEntity{Name: "api", Image: "registry.example/shared", BuildImage: "registry.example/shared:old", Build: BuildConfigEntity{Type: "docker", Unit: "image", Docker: &DockerBuild{}}}
	b := a
	b.Name = "worker"
	results := buildKCLDockerShell(context.Background(), &config.ProjectConfig{}, &KCLEntities{Workloads: []WorkloadEntity{a, b}}, buildOptions{release: "v2", composition: &compositionRun{}}, "amd64", "v2")
	if calls != 1 || len(results) != 2 || results[0].workload != "api" || results[1].workload != "worker" || results[1].digest != digest {
		t.Fatalf("wrong build output association: %+v calls%d", results, calls)
	}
}

func TestCompositionSameArtifactPreservesSelectedBuildEvents(t *testing.T) {
	s := release.Snapshot{Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	id := release.ArtifactID("same-content")
	for _, name := range []string{"api", "worker"} {
		origin := release.ArtifactOrigin{Release: "v2", Receipt: &release.BuildReceipt{InputDigest: name}}
		recordCompositionBuildEvent(&s, release.ConsumerKey{Workload: name}, id, origin)
		s.Artifacts[id] = release.SnapshotArtifact{Origin: origin}
	}
	if len(s.BuildEvents) != 2 || s.BuildEvents[0].Origin.Receipt.InputDigest != "api" || s.BuildEvents[1].Origin.Receipt.InputDigest != "worker" {
		t.Fatal("selected build attestations overwritten by shared artifact identity")
	}
}

func TestCompositionSelectedDockerCannotFallBackToRenderedTag(t *testing.T) {
	key := release.ConsumerKey{Cluster: "prod", Workload: "worker"}
	w := WorkloadEntity{Name: "worker", Image: "registry.example/shared", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{}}, Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod"}}}
	e := &KCLEntities{Workloads: []WorkloadEntity{w}}
	run := &compositionRun{Entities: e, Selected: []release.ConsumerKey{key}, Declaration: release.Snapshot{Consumers: []release.Consumer{{Key: key, Slots: []string{"container:worker"}}}, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}}
	err := finishComposition(context.Background(), &config.ProjectConfig{Name: "p"}, e, buildOptions{composition: run}, nil)
	if err == nil || !strings.Contains(err.Error(), "lacks this invocation") {
		t.Fatalf("rendered mutable fallback remained: %v", err)
	}
}
