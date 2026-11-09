package release

import (
	"strings"
	"testing"
)

// rawDeploymentSnapshot is one authored raw Deployment whose container is bound
// to an external image the consumer declared as `repo:<tag>@sha256:X`.
func rawDeploymentSnapshot(t *testing.T, tag, image string) Snapshot {
	t.Helper()
	content := SpecDigest([]byte("nats"))
	artifact := SnapshotArtifact{Kind: KindOCI, Location: "docker.io/library/nats", Content: content, Platforms: []string{"linux/amd64"}, Origin: ArtifactOrigin{Release: "v1", External: &ExternalArtifact{Verifier: "registry/v1", DescriptorDigest: content}}}
	id, err := ArtifactIdentity(artifact)
	if err != nil {
		t.Fatal(err)
	}
	key := ConsumerKey{Cluster: "hub", Workload: "nats-cluster"}
	image = strings.ReplaceAll(image, "DIGEST", content)
	spec := "apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: nats-cluster\n  namespace: cp\nspec:\n  template:\n    spec:\n      containers:\n        - name: nats\n          image: " + image + "\n"
	return Snapshot{
		Artifacts: map[ArtifactID]SnapshotArtifact{id: artifact},
		Consumers: []Consumer{{Key: key, Slots: []string{"container:nats"}}},
		Bindings:  []ConsumerBinding{{Consumer: key, Slot: "container:nats", Artifact: id, Tag: tag}},
		Resources: []SnapshotResource{{Key: ResourceKey{Cluster: "hub", Group: "apps", Kind: "StatefulSet", Namespace: "cp", Name: "nats-cluster"}, Spec: []byte(spec), Owners: []ConsumerKey{key}}},
	}
}

func TestBindingReferenceKeepsTheDeclaredTag(t *testing.T) {
	a := SnapshotArtifact{Kind: KindOCI, Location: "host:5000/team/nats", Content: "sha256:" + strings.Repeat("a", 64)}
	if got, want := BindingReference(a, ConsumerBinding{Tag: "2.10.24-alpine"}), "host:5000/team/nats:2.10.24-alpine@sha256:"+strings.Repeat("a", 64); got != want {
		t.Fatalf("tagged = %s, want %s", got, want)
	}
	if got, want := BindingReference(a, ConsumerBinding{}), "host:5000/team/nats@sha256:"+strings.Repeat("a", 64); got != want {
		t.Fatalf("untagged = %s, want %s", got, want)
	}
}

// Sealing a reference the consumer already pinned must render it byte-for-byte:
// a container carrying the declared `repo:tag@sha256:X` is the binding, and the
// bare form is not (it would change the pod template and roll the workload).
func TestSnapshotBindingsCompareTheDeclaredReference(t *testing.T) {
	if err := ValidateSnapshotBindings(rawDeploymentSnapshot(t, "2.10.24-alpine", "docker.io/library/nats:2.10.24-alpine@DIGEST")); err != nil {
		t.Fatalf("declared tagged reference refused: %v", err)
	}
	if err := ValidateSnapshotBindings(rawDeploymentSnapshot(t, "2.10.24-alpine", "docker.io/library/nats@DIGEST")); err == nil {
		t.Fatal("a tagged binding admitted a container that dropped the tag")
	}
	if err := ValidateSnapshotBindings(rawDeploymentSnapshot(t, "", "docker.io/library/nats@DIGEST")); err != nil {
		t.Fatalf("untagged binding refused its bare reference: %v", err)
	}
	if err := ValidateSnapshotBindings(rawDeploymentSnapshot(t, "", "docker.io/library/nats:2.10.24-alpine@DIGEST")); err == nil {
		t.Fatal("an untagged binding admitted an invented tag")
	}
}

func TestValidImageTag(t *testing.T) {
	for tag, want := range map[string]bool{"2.10.24-alpine": true, "v1.7.72": true, "latest": true, "_x": true, "": false, "-x": false, ".x": false, "a/b": false, "a@b": false, strings.Repeat("a", 129): false} {
		if got := ValidImageTag(tag); got != want {
			t.Errorf("ValidImageTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
