package release

import (
	"errors"
	"strings"
	"testing"
)

func testBuildInputs() BuildInputs {
	return BuildInputs{Recipe: "reliant/linux", Sources: []Source{{Repo: "github.com/acme/app", Ref: "v1", Commit: strings.Repeat("a", 40)}}, ContextDigest: SpecDigest([]byte("context")), RecipeDigest: SpecDigest([]byte("recipe")), ConfigDigest: SpecDigest([]byte("config")), Tools: map[string]string{"go": SpecDigest([]byte("binary"))}, Platforms: []string{"linux/arm64", "linux/amd64"}, Outputs: []string{"main", "debug"}, Cacheable: true}
}
func TestBuildInputIdentityAndActualSource(t *testing.T) {
	in := testBuildInputs()
	first, e := BuildInputDigest(in)
	if e != nil {
		t.Fatal(e)
	}
	in.Platforms[0], in.Platforms[1] = in.Platforms[1], in.Platforms[0]
	in.Outputs[0], in.Outputs[1] = in.Outputs[1], in.Outputs[0]
	again, _ := BuildInputDigest(in)
	if again != first {
		t.Fatal("set ordering changed build identity")
	}
	in.Sources[0].Commit = strings.Repeat("b", 40)
	changed, _ := BuildInputDigest(in)
	if changed == first {
		t.Fatal("actual pinned source omitted")
	}
	in.Sources[0].Commit = "main"
	if _, e := BuildInputDigest(in); !errors.Is(e, ErrInvalid) {
		t.Fatal("mutable source accepted")
	}
}
func TestArtifactIdentitySeparatesOriginAndPlatforms(t *testing.T) {
	a := SnapshotArtifact{Kind: KindOCI, Location: "registry.test/image", Content: SpecDigest([]byte("image"))}
	id, e := ArtifactIdentity(a)
	if e != nil {
		t.Fatal(e)
	}
	a.Platforms = []string{"linux/amd64"}
	a.Origin.Release = "other"
	again, e := ArtifactIdentity(a)
	if e != nil || id != again {
		t.Fatal("origin changed byte identity")
	}
	a.Location = "registry.test/other"
	other, _ := ArtifactIdentity(a)
	if id == other {
		t.Fatal("location omitted from identity")
	}
	a.Location = "registry.test/image:latest"
	if _, e := ArtifactIdentity(a); e == nil {
		t.Fatal("tag accepted")
	}
}
func TestSnapshotIntentRecutCannotFlattenBindings(t *testing.T) {
	a := Release{Version: "v1", Artifacts: map[string]Artifact{"image": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: SpecDigest([]byte("image"))}}}, SnapshotIntents: map[string]string{"prod": SpecDigest([]byte("worker old"))}}
	b := a
	b.SnapshotIntents = map[string]string{"prod": SpecDigest([]byte("worker new"))}
	if !errors.Is(CheckRecut(a, b), ErrReleaseConflict) {
		t.Fatal("snapshot binding intent bypassed recut with equal image map")
	}
	if e := CheckRecut(a, a); e != nil {
		t.Fatal(e)
	}
}
func TestSnapshotSealValidation(t *testing.T) {
	s := SnapshotSeal{Release: "v1", Project: "app", Environment: "prod", SnapshotDigest: SpecDigest([]byte("snapshot")), BundleDigest: SpecDigest([]byte("bundle"))}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.BundleDigest = "latest"
	if e := s.Validate(); e == nil {
		t.Fatal("unsealed identity accepted")
	}
}
