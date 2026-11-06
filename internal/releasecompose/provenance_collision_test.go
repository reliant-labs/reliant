package releasecompose

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestIdenticalRebuildPreservesCarriedArtifactOrigin(t *testing.T) {
	base := fixture(t)
	candidate := clone(base)
	id := candidate.Bindings[1].Artifact
	old, _ := json.Marshal(base.Artifacts[id].Origin)
	rebuilt := candidate.Artifacts[id]
	rebuilt.Origin.Release = "v2"
	inputs := release.BuildInputs{Recipe: "reproducible", Sources: []release.Source{base.Source}, ContextDigest: dg("1"), RecipeDigest: dg("2"), ConfigDigest: dg("3"), Tools: map[string]string{"go": dg("4")}, Platforms: []string{"linux/amd64"}, Outputs: []string{"image"}, Cacheable: true}
	inputHash, err := release.BuildInputDigest(inputs)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt.Origin.Receipt = &release.BuildReceipt{Inputs: inputs, InputDigest: inputHash, Outputs: map[string]string{"image": rebuilt.Content}}
	candidate.Artifacts[id] = rebuilt
	out, err := Compose(input(t, base, candidate, base.Consumers[1].Key))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(out.Artifacts[id].Origin)
	if string(got) != string(old) {
		t.Fatal("selected identical output rewrote carried API origin")
	}
	if len(out.BuildEvents) != 1 || out.BuildEvents[0].Consumer != base.Consumers[1].Key || out.BuildEvents[0].Origin.Release != "v2" {
		t.Fatalf("new rebuild attestation lost: %+v", out.BuildEvents)
	}
	if err := release.ValidateSnapshotTransition(&base, out); err != nil {
		t.Fatal(err)
	}
	// A self-consistent malicious catalog overwrite is refused by server recomposition.
	poisoned := clone(out)
	poisoned.Artifacts[id] = rebuilt
	if err := release.ValidateSnapshotTransition(&base, poisoned); err == nil {
		t.Fatal("server accepted carried origin overwrite")
	}
	retried, err := Compose(Input{Base: &base, Candidate: out, BaseRef: out.Base, Selected: out.Selected})
	if err != nil {
		t.Fatal(err)
	}
	if len(retried.BuildEvents) != 1 {
		t.Fatal("retry duplicated build event")
	}
}
