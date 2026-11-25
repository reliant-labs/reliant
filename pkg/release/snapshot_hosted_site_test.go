package release

import (
	"strings"
	"testing"
)

// hostedSiteSnapshot is a hosted frontend whose frontend slot binds a
// platform-neutral site release, served by a StaticSite record.
func hostedSiteSnapshot(t *testing.T) (Snapshot, []byte) {
	t.Helper()
	src := Source{Repo: "github.com/acme/app", Commit: strings.Repeat("a", 40)}
	s := Snapshot{Schema: SnapshotSchema, Project: "app", Environment: "prod", Source: src, PolicyDigest: SpecDigest([]byte("policy")), Artifacts: map[ArtifactID]SnapshotArtifact{}}
	key := ConsumerKey{Cluster: BundleHostedCluster, Workload: "web"}
	site := SnapshotArtifact{Kind: KindOCI, Location: "registry.test/acme/web/static.v1", Content: SpecDigest([]byte("site")), Origin: ArtifactOrigin{Release: "v1", Source: &src}}
	id, err := ArtifactIdentity(site)
	if err != nil {
		t.Fatal(err)
	}
	s.Artifacts[id] = site
	s.Consumers = []Consumer{{Key: key, Slots: []string{"frontend"}}}
	s.Selected, s.Closure = []ConsumerKey{key}, []ConsumerKey{key}
	s.Bindings = []ConsumerBinding{{Consumer: key, Slot: "frontend", Artifact: id}}
	s.Decisions = []CompositionDecision{{Consumer: key, Action: "build", Reason: "bootstrap"}}
	spec := "apiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: web\nspec:\n  releaseRepository: " + site.Location + "\n  liveDigest: " + site.Content + "\n"
	s.Resources = []SnapshotResource{{Key: ResourceKey{Cluster: BundleHostedCluster, Group: "forge.dev", Kind: "StaticSite", Name: "web"}, Spec: []byte(spec), Digest: SpecDigest([]byte(spec)), Tree: "manifests/hosted", Phase: "rollout", Owners: []ConsumerKey{key}}}
	return s, buildLayer(t, layerEntry{Name: "manifests/hosted/000-staticsite-web.yaml", Body: spec})
}

// A hosted frontend's site release has no os/architecture, and its record
// serves exactly the bound release.
func TestSnapshotHostedSiteReleaseIsPlatformNeutralAndServedByItsRecord(t *testing.T) {
	s, layer := hostedSiteSnapshot(t)
	if err := ValidateSnapshotLayer(s, layer); err != nil {
		t.Fatalf("a hosted site release served by its record must validate: %v", err)
	}
}

func TestSnapshotHostedSiteFailsClosed(t *testing.T) {
	other := SpecDigest([]byte("other"))
	for _, test := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"record serves another digest", func(s *Snapshot) {
			s.Resources[0].Spec = []byte(strings.Replace(string(s.Resources[0].Spec), SpecDigest([]byte("site")), other, 1))
			s.Resources[0].Digest = SpecDigest(s.Resources[0].Spec)
		}},
		{"record serves another repository", func(s *Snapshot) {
			s.Resources[0].Spec = []byte(strings.Replace(string(s.Resources[0].Spec), "registry.test/acme/web/static.v1", "registry.test/acme/elsewhere", 1))
			s.Resources[0].Digest = SpecDigest(s.Resources[0].Spec)
		}},
		{"frontend bound to source, not a site release", func(s *Snapshot) {
			src := s.Source
			git := SnapshotArtifact{Kind: KindGit, Location: src.Repo, Content: src.Commit, Origin: ArtifactOrigin{Release: "v1", Source: &src}}
			id, _ := ArtifactIdentity(git)
			s.Artifacts = map[ArtifactID]SnapshotArtifact{id: git}
			s.Bindings[0].Artifact = id
		}},
		{"hosted frontend without a record", func(s *Snapshot) {
			s.Resources = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := hostedSiteSnapshot(t)
			test.mutate(&s)
			if err := s.Validate(); err != nil {
				return // refused structurally; equally closed
			}
			if err := ValidateSnapshotBindings(s); err == nil {
				t.Fatal("a StaticSite that does not serve its bound site release was admitted")
			}
		})
	}
}

// Platform neutrality is the hosted frontend's alone: the same artifact bound
// to a runtime image slot, or to a non-hosted frontend, needs platforms.
func TestSnapshotPlatformNeutralArtifactOnlyForHostedFrontend(t *testing.T) {
	for _, test := range []struct {
		name string
		key  ConsumerKey
		slot string
	}{
		{"cluster container", ConsumerKey{Cluster: "prod", Workload: "api"}, "container:api"},
		{"forge-published frontend", ConsumerKey{Cluster: "frontend", Workload: "docs"}, "frontend"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := hostedSiteSnapshot(t)
			s.Consumers = append(s.Consumers, Consumer{Key: test.key, Slots: []string{test.slot}})
			s.Selected, s.Closure = append(s.Selected, test.key), append(s.Closure, test.key)
			s.Decisions = append(s.Decisions, CompositionDecision{Consumer: test.key, Action: "build", Reason: "bootstrap"})
			s.Bindings = append(s.Bindings, ConsumerBinding{Consumer: test.key, Slot: test.slot, Artifact: s.Bindings[0].Artifact})
			if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "lacks verified platforms") {
				t.Fatalf("platformless artifact on %s/%s: err = %v", test.key, test.slot, err)
			}
		})
	}
}
