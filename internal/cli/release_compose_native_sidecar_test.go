package cli

import (
	"context"
	"strings"
	"testing"

	deployv1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionNativeSidecarUsesInitSlot(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	w := WorkloadEntity{Name: "api", Image: "registry.example/api@" + digest, Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod"}}}
	w.Spec.Sidecars = []deployv1.Container{{Name: "proxy", Image: "registry.example/proxy@" + digest, Native: true}, {Name: "log", Image: "registry.example/log@" + digest}}
	e := &KCLEntities{Workloads: []WorkloadEntity{w}}
	consumers, err := compositionConsumers(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 1 || strings.Join(consumers[0].Slots, ",") != "container:api,init:proxy,container:log" {
		t.Fatalf("native sidecar has wrong runtime slot: %+v", consumers)
	}
	prev := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = prev })
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) { return digest, []string{"linux/amd64"}, nil }
	key := consumers[0].Key
	cand := release.Snapshot{Consumers: consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	run := &compositionRun{Entities: e, Selected: []release.ConsumerKey{key}}
	if err := populateCompositionBindings(t.Context(), &cand, run, e, buildOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(cand.Bindings) != 3 {
		t.Fatalf("lost sidecar bindings: %+v", cand.Bindings)
	}
}
