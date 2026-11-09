package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionNativeOpenBaoAtomicConsumer(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	provider := &OpenBaoEntity{Name: "store", Namespace: "secrets", Cluster: "prod", ReleaseOwner: "openbao-secrets-store", ReleaseInputs: map[string]string{"container:openbao": "registry.example/openbao@" + digest, "container:converge": "registry.example/converge@" + digest, "init:cloud-sql-proxy": "registry.example/proxy@" + digest}}
	entities := &KCLEntities{OpenBao: provider}
	consumers, err := compositionConsumers(entities)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 1 || len(consumers[0].Slots) != 3 || consumers[0].Key.Workload != provider.ReleaseOwner || !consumers[0].DependenciesDeclared {
		t.Fatalf("provider not one explicit atomic consumer: %+v", consumers)
	}
	candidate := release.Snapshot{Consumers: consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	run := &compositionRun{Entities: entities, Selected: []release.ConsumerKey{consumers[0].Key}}
	prev := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = prev })
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) { return digest, []string{"linux/amd64"}, nil }
	if err := populateCompositionBindings(t.Context(), &candidate, run, entities, buildOptions{release: "v1"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(candidate.Bindings) != 3 {
		t.Fatalf("missing native provider slot: %+v", candidate.Bindings)
	}
	entities.ReleaseManifestConsumers = []releaseManifestConsumer{{Name: provider.ReleaseOwner, Cluster: "prod", ReleaseInputs: map[string]string{"container:other": "registry.example/other"}}}
	if _, err := compositionConsumers(entities); err == nil {
		t.Fatal("native provider/authored manifest collision accepted")
	}
	entities.ReleaseManifestConsumers = nil
	delete(provider.ReleaseInputs, "container:converge")
	if _, err := compositionConsumers(entities); err == nil {
		t.Fatal("incomplete provider image contract accepted")
	}
}
