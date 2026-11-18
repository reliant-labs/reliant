package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionAuxiliaryInputsUseVerifiedProducerBindings(t *testing.T) {
	const repo = "registry.example/platform/workspace-base"
	key := release.ConsumerKey{Cluster: "build-only", Workload: "workspace-base"}
	producer := WorkloadEntity{Name: key.Workload, Image: repo, Runtime: RuntimeEntity{Type: RuntimeBuildOnly}, Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{Dockerfile: "Dockerfile"}}}
	entities := &KCLEntities{Workloads: []WorkloadEntity{producer}, ReleaseManifestConsumers: []releaseManifestConsumer{{Name: "prepull", Cluster: "daemon", ReleaseInputs: map[string]string{"container:prepull": repo + ":prod"}}}}
	artifact := release.SnapshotArtifact{Kind: release.KindOCI, Location: repo, Content: "sha256:" + strings.Repeat("a", 64), Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", Source: &release.Source{Repo: "github.com/acme/platform", Commit: strings.Repeat("b", 40)}}}
	id, err := release.ArtifactIdentity(artifact)
	if err != nil {
		t.Fatal(err)
	}
	candidate := release.Snapshot{Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: artifact}, Bindings: []release.ConsumerBinding{{Consumer: key, Slot: "build-only", Artifact: id}}}
	for _, slot := range []string{"input:workspace-base", "container:prepull"} {
		got, bound, err := compositionInputArtifact(&candidate, entities, release.ConsumerKey{Cluster: "daemon", Workload: "prepull"}, slot, repo+":prod")
		if err != nil || !bound || got.Content != artifact.Content || got.Origin.Source != artifact.Origin.Source {
			t.Fatalf("%s lost exact producer provenance: %+v %v %v", slot, got, bound, err)
		}
	}
	previous := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = previous })
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		return "", nil, fmt.Errorf("unexpected mutable tag lookup: %s", ref)
	}
	run := &compositionRun{Entities: entities}
	if err := addSelectedCompositionBinding(context.Background(), &candidate, run, entities, buildOptions{}, nil, release.ConsumerKey{Cluster: "daemon", Workload: "prepull"}, "container:prepull"); err != nil {
		t.Fatalf("manifest input must reuse verified output, not inspect :prod: %v", err)
	}
	if candidate.Bindings[len(candidate.Bindings)-1].Artifact != id {
		t.Fatal("input created different artifact instead of exact producer binding")
	}
}

func TestCompositionAuxiliaryInputBeforeProducerAndCarriedBase(t *testing.T) {
	const repo = "registry.example/base"
	producer := WorkloadEntity{Name: "base", Image: repo, Runtime: RuntimeEntity{Type: RuntimeBuildOnly}, Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{Dockerfile: "Dockerfile"}}}
	entities := &KCLEntities{Workloads: []WorkloadEntity{producer}, ReleaseManifestConsumers: []releaseManifestConsumer{{Name: "prepull", Cluster: "daemon", ReleaseInputs: map[string]string{"container:prepull": repo + ":prod"}}}}
	pkey := release.ConsumerKey{Cluster: "build-only", Workload: "base"}
	mkey := release.ConsumerKey{Cluster: "daemon", Workload: "prepull"}
	digest := "sha256:" + strings.Repeat("a", 64)
	prev := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = prev })
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		if ref != repo+"@"+digest {
			return "", nil, fmt.Errorf("not invocation-pinned: %s", ref)
		}
		return digest, []string{"linux/amd64"}, nil
	}
	consumers := []release.Consumer{{Key: mkey, Slots: []string{"container:prepull"}}, {Key: pkey, Slots: []string{"build-only"}}}
	candidate := release.Snapshot{Consumers: consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}, Source: release.Source{Repo: "github.com/acme/platform", Commit: strings.Repeat("b", 40)}}
	run := &compositionRun{Entities: entities, Selected: []release.ConsumerKey{mkey, pkey}}
	results := []buildResult{{kind: "docker", workload: "base", repository: repo, digest: digest}}
	if err := populateCompositionBindings(context.Background(), &candidate, run, entities, buildOptions{release: "v1"}, results); err != nil {
		t.Fatal(err)
	}
	if len(candidate.Artifacts) != 1 || len(candidate.Bindings) != 2 || candidate.Bindings[0].Artifact != candidate.Bindings[1].Artifact {
		t.Fatal("auxiliary input did not bind producer identity")
	}
	base := candidate
	candidate = release.Snapshot{Consumers: consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	run.BaseSnapshot = &base
	run.Selected = []release.ConsumerKey{mkey}
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) {
		return "", nil, fmt.Errorf("carried producer must not be resolved again")
	}
	if err := populateCompositionBindings(context.Background(), &candidate, run, entities, buildOptions{release: "v2"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(candidate.Artifacts) != 1 || candidate.Artifacts[candidate.Bindings[0].Artifact].Origin.Release != "v1" {
		t.Fatal("carried producer provenance changed")
	}
}

func TestCompositionAuxiliaryInputRejectsMissingAmbiguousAndConflictingProducer(t *testing.T) {
	const repo = "registry.example/base"
	producer := WorkloadEntity{Name: "base", Image: repo, Runtime: RuntimeEntity{Type: RuntimeBuildOnly}, Build: BuildConfigEntity{Type: "docker"}}
	entities := &KCLEntities{Workloads: []WorkloadEntity{producer}}
	consumer := release.ConsumerKey{Cluster: "prod", Workload: "controller"}
	candidate := release.Snapshot{Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	if _, _, err := compositionInputArtifact(&candidate, entities, consumer, "input:base", repo+":prod"); err == nil {
		t.Fatal("missing selected or carried producer was silently adopted")
	}
	other := producer
	other.Name = "other"
	entities.Workloads = append(entities.Workloads, other)
	if _, _, err := compositionInputArtifact(&candidate, entities, consumer, "input:base", repo); err == nil {
		t.Fatal("ambiguous declared producers were accepted")
	}
	entities.Workloads = entities.Workloads[:1]
	artifact := release.SnapshotArtifact{Kind: release.KindOCI, Location: repo, Content: "sha256:" + strings.Repeat("a", 64), Origin: release.ArtifactOrigin{Source: &release.Source{Commit: strings.Repeat("b", 40)}}}
	id, _ := release.ArtifactIdentity(artifact)
	candidate.Artifacts[id] = artifact
	candidate.Bindings = []release.ConsumerBinding{{Consumer: release.ConsumerKey{Cluster: "build-only", Workload: "base"}, Slot: "build-only", Artifact: id}}
	if _, _, err := compositionInputArtifact(&candidate, entities, consumer, "input:base", repo+"@sha256:"+strings.Repeat("c", 64)); err == nil {
		t.Fatal("conflicting declared immutable digest was ignored")
	}
	artifact.Origin.Source = nil
	candidate.Artifacts[id] = artifact
	if _, _, err := compositionInputArtifact(&candidate, entities, consumer, "input:base", repo); err == nil {
		t.Fatal("unproven producer artifact was accepted")
	}
}
