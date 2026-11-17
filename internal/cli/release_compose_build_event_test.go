package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionBuildEventIsPerConsumerArtifactNotSlot(t *testing.T) {
	source := &release.Source{Repo: "example/project", Commit: strings.Repeat("a", 40)}
	origin := release.ArtifactOrigin{Release: "v1", Source: source}
	artifact := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.example/tool", Content: "sha256:" + strings.Repeat("b", 64), Platforms: []string{"linux/amd64"}, Origin: origin}
	id, err := release.ArtifactIdentity(artifact)
	if err != nil {
		t.Fatal(err)
	}
	key := release.ConsumerKey{Cluster: "prod", Workload: "setup"}
	snapshot := release.Snapshot{Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: artifact}, Bindings: []release.ConsumerBinding{{Consumer: key, Slot: "container:setup", Artifact: id}, {Consumer: key, Slot: "init:init", Artifact: id}}}
	recordCompositionBuildEvent(&snapshot, key, id, origin)
	recordCompositionBuildEvent(&snapshot, key, id, origin)
	if len(snapshot.Bindings) != 2 || len(snapshot.BuildEvents) != 1 {
		t.Fatal("same artifact's two runtime slots must preserve bindings with one build event")
	}
	other := key
	other.Workload = "other"
	recordCompositionBuildEvent(&snapshot, other, id, origin)
	if len(snapshot.BuildEvents) != 2 {
		t.Fatal("different consumer must retain its own build event")
	}
	conflict := origin
	conflict.Release = "different"
	recordCompositionBuildEvent(&snapshot, key, id, conflict)
	if len(snapshot.BuildEvents) != 3 {
		t.Fatal("conflicting provenance was silently deduplicated")
	}
}
