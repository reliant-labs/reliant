package cli

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// compositionPrimarySlot separates output-producing slots from declared image
// uses. Manifest containers and workload inputs never produce build outputs.
func compositionPrimarySlot(entities *KCLEntities, key release.ConsumerKey, slot string) bool {
	if slot == "frontend" {
		return true
	}
	for _, w := range entities.Workloads {
		if w.Name == key.Workload {
			return slot == "build-only" || slot == "container:"+w.Name
		}
	}
	return false
}

// compositionInputArtifact binds an explicit auxiliary image use to a unique
// declared build-only producer's already verified primary binding. This is NOT
// repository-based build deduplication: the producer declaration, its exact
// consumer slot and invocation receipt (or captured base) must all exist.
func compositionInputArtifact(candidate *release.Snapshot, entities *KCLEntities, key release.ConsumerKey, slot, ref string) (release.SnapshotArtifact, bool, error) {
	if compositionPrimarySlot(entities, key, slot) {
		return release.SnapshotArtifact{}, false, nil
	}
	repository := imageRepository(ref)
	var producers []release.ConsumerKey
	for _, w := range entities.Workloads {
		if w.Runtime.Type != RuntimeBuildOnly || w.Build.Type == "" || imageRepository(w.Image) != repository {
			continue
		}
		producers = append(producers, release.ConsumerKey{Cluster: "build-only", Workload: w.Name})
	}
	if len(producers) == 0 {
		return release.SnapshotArtifact{}, false, nil // genuinely external declared image
	}
	if len(producers) != 1 {
		return release.SnapshotArtifact{}, false, fmt.Errorf("auxiliary input %s/%s has ambiguous declared producers", key.Workload, slot)
	}
	var artifact release.SnapshotArtifact
	found := false
	for _, binding := range candidate.Bindings {
		if binding.Consumer != producers[0] || binding.Slot != "build-only" {
			continue
		}
		a, exists := candidate.Artifacts[binding.Artifact]
		if !exists || a.Kind != release.KindOCI || a.Location != repository || !release.ValidDigest(a.Content) || (a.Origin.Receipt == nil && a.Origin.Source == nil) {
			return artifact, false, fmt.Errorf("auxiliary input %s/%s lacks verified producer provenance", key.Workload, slot)
		}
		if found && artifact.Content != a.Content {
			return artifact, false, fmt.Errorf("auxiliary input %s/%s has conflicting producer outputs", key.Workload, slot)
		}
		artifact, found = a, true
	}
	if !found {
		return artifact, false, fmt.Errorf("auxiliary input %s/%s requires selected or carried producer %s", key.Workload, slot, producers[0].Workload)
	}
	if at := strings.IndexByte(ref, '@'); at >= 0 && ref[at+1:] != artifact.Content {
		return artifact, false, fmt.Errorf("auxiliary input %s/%s pinned digest differs from producer output", key.Workload, slot)
	}
	return artifact, true, nil
}
