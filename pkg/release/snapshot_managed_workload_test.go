package release

import (
	"fmt"
	"strings"
	"testing"
)

func managedWorkloadSnapshot(t *testing.T) (Snapshot, []byte) {
	t.Helper()
	s := Snapshot{Schema: SnapshotSchema, Project: "app", Environment: "prod", Source: Source{Repo: "github.com/acme/app", Commit: strings.Repeat("a", 40)}, PolicyDigest: SpecDigest([]byte("policy")), Artifacts: map[ArtifactID]SnapshotArtifact{}}
	var entries []layerEntry
	for i, name := range []string{"api", "worker"} {
		key := ConsumerKey{Cluster: "hosted", Workload: name}
		content := SpecDigest([]byte(name))
		artifact := SnapshotArtifact{Kind: KindOCI, Location: "registry.test/shared", Content: content, Platforms: []string{"linux/amd64"}, Origin: ArtifactOrigin{Release: "v1", External: &ExternalArtifact{Verifier: "registry/v1", DescriptorDigest: content}}}
		id, err := ArtifactIdentity(artifact)
		if err != nil {
			t.Fatal(err)
		}
		s.Artifacts[id] = artifact
		s.Consumers = append(s.Consumers, Consumer{Key: key, Slots: []string{"container:" + name}, DependenciesDeclared: true})
		s.Selected = append(s.Selected, key)
		s.Closure = append(s.Closure, key)
		s.Bindings = append(s.Bindings, ConsumerBinding{Consumer: key, Slot: "container:" + name, Artifact: id})
		s.Decisions = append(s.Decisions, CompositionDecision{Consumer: key, Action: "build", Reason: "bootstrap"})
		spec := fmt.Sprintf("apiVersion: forge.dev/v1alpha1\nkind: Workload\nmetadata:\n  name: %s\n  namespace: app\nspec:\n  image: registry.test/shared@%s\n", name, content)
		s.Resources = append(s.Resources, SnapshotResource{Order: i, Key: ResourceKey{Cluster: "hosted", Group: "forge.dev", Kind: "Workload", Namespace: "app", Name: name}, Spec: []byte(spec), Digest: SpecDigest([]byte(spec)), Tree: "manifests/hosted", Phase: "rollout", Owners: []ConsumerKey{key}})
		entries = append(entries, layerEntry{Name: fmt.Sprintf("manifests/hosted/%03d-workload-%s.yaml", i, name), Body: spec})
	}
	return s, buildLayer(t, entries...)
}

func TestSnapshotManagedWorkloadMixedRepositoryBindings(t *testing.T) {
	s, layer := managedWorkloadSnapshot(t)
	if err := ValidateSnapshotLayer(s, layer); err != nil {
		t.Fatal(err)
	}
	if s.Bindings[0].Artifact == s.Bindings[1].Artifact {
		t.Fatal("fixture must have distinct same-repository artifacts")
	}
	s.Bindings[1].Artifact = s.Bindings[0].Artifact
	if err := ValidateSnapshotLayer(s, layer); err == nil {
		t.Fatal("same-repository digest flattening admitted")
	}
}

func TestSnapshotManagedWorkloadFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"wrong owner", func(s *Snapshot) { s.Resources[0].Owners = []ConsumerKey{s.Consumers[1].Key} }},
		{"shared owner", func(s *Snapshot) { s.Resources[0].Owners = append(s.Resources[0].Owners, s.Consumers[1].Key) }},
		{"wrong primary name", func(s *Snapshot) { s.Bindings[0].Slot = "container:other" }},
		{"unsupported init", func(s *Snapshot) {
			s.Bindings = append(s.Bindings, ConsumerBinding{Consumer: s.Consumers[0].Key, Slot: "init:setup", Artifact: s.Bindings[0].Artifact})
		}},
		{"undeclared sidecar", func(s *Snapshot) {
			s.Resources[0].Spec = append(s.Resources[0].Spec, []byte("  sidecars:\n    - name: hidden\n      image: registry.test/shared@"+SpecDigest([]byte("hidden"))+"\n")...)
		}},
		{"foreign CR group", func(s *Snapshot) {
			s.Resources[0].Spec = []byte(strings.Replace(string(s.Resources[0].Spec), "forge.dev/v1alpha1", "other.dev/v1alpha1", 1))
			s.Resources[0].Key.Group = "other.dev"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := managedWorkloadSnapshot(t)
			test.mutate(&s)
			if err := ValidateSnapshotBindings(s); err == nil {
				t.Fatal("ambiguous/unsupported managed binding admitted")
			}
		})
	}
}
