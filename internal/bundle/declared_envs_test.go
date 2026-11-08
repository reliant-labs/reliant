package bundle

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// fixtureDigest is buildFixture()'s bundle digest, recorded from forge main
// (849c37b43) BEFORE the declared-env catalog and the shape-placement fields
// existed. A build that sets none of the new parts — and a build for a reader
// that names neither feature — must still produce exactly these bytes: that
// is what "no existing bundle digest changes" means, checked rather than
// claimed.
const fixtureDigest = "sha256:d619ae8951a19b4a1d98b360c2d0cfc2d01710f305e012f33b4faefdb48e24fa"

func fixtureCatalog() []release.DeclaredEnv {
	return []release.DeclaredEnv{
		{Name: "dev", Lifecycle: release.DeclaredLifecycleLocal, Destination: release.DeclaredDestinationCompose},
		{Name: "prod", Destination: release.DeclaredDestinationCluster, ControlPlaneKind: string(release.EnvSelfManaged)},
		{Name: "wip", RenderError: release.DeclaredEnvRenderFailed},
	}
}

func TestBuildDigestIsUnchangedWithoutTheNewParts(t *testing.T) {
	if got := mustBuild(t, buildFixture()).Digest; got != fixtureDigest {
		t.Fatalf("buildFixture()'s digest moved: %s, want %s.\n\n"+
			"A bundle that carries none of the new optional parts must be byte-identical to one built before they existed; "+
			"a change here re-digests every recorded bundle on the next build", got, fixtureDigest)
	}
}

func TestBuildDeclaredEnvsAreAFeature(t *testing.T) {
	in := buildFixture()
	in.DeclaredEnvs = fixtureCatalog()

	t.Run("a reader without the feature gets the old bytes", func(t *testing.T) {
		old := in
		old.Reader = &release.BundleReader{Features: withoutFeature(release.BundleFeatureDeclaredEnvs)}
		b := mustBuild(t, old)
		if b.Doc.DeclaredEnvs != nil {
			t.Fatalf("the catalog reached a reader that does not decode it: %+v", b.Doc.DeclaredEnvs)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(b.ConfigBlob(), &top); err != nil {
			t.Fatal(err)
		}
		if _, ok := top["declared_envs"]; ok {
			t.Fatal("the document carries a declared_envs key, even empty, for a reader that refuses it")
		}
		if b.Digest != fixtureDigest {
			t.Fatalf("withholding the catalog must leave the bundle exactly as before: %s, want %s", b.Digest, fixtureDigest)
		}
	})

	t.Run("a reader with the feature gets it sealed in", func(t *testing.T) {
		withCatalog := in
		withCatalog.Reader = &release.BundleReader{Features: release.BundleDocFeatures()}
		b := mustBuild(t, withCatalog)
		if len(b.Doc.DeclaredEnvs) != 3 || b.Doc.DeclaredEnvs[1].ControlPlaneKind != string(release.EnvSelfManaged) {
			t.Fatalf("catalog = %+v", b.Doc.DeclaredEnvs)
		}
		if b.Digest == fixtureDigest {
			t.Fatal("the catalog is inside the document, so the digest must cover it")
		}
		if b.Doc.ConfigDigest != mustBuild(t, buildFixture()).Doc.ConfigDigest {
			t.Fatal("the catalog describes the project, not this env's render: it must not move the config digest")
		}
		// The document a control plane decodes is the one sealed.
		doc, err := release.DecodeBundleDoc(b.ConfigBlob())
		if err != nil {
			t.Fatalf("the sealed document does not decode: %v", err)
		}
		if len(doc.DeclaredEnvs) != 3 {
			t.Fatalf("decoded catalog = %+v", doc.DeclaredEnvs)
		}
		if again := mustBuild(t, withCatalog); again.Digest != b.Digest {
			t.Fatalf("a catalog bundle must be deterministic: %s vs %s", again.Digest, b.Digest)
		}
	})

	t.Run("an invalid catalog fails the build", func(t *testing.T) {
		bad := in
		bad.DeclaredEnvs = []release.DeclaredEnv{{Name: "/home/me/proj/deploy/kcl/prod"}}
		if _, err := Build(t.Context(), bad); err == nil {
			t.Fatal("a catalog naming a path was sealed")
		}
	})
}

func TestBuildShapePlacementIsAFeature(t *testing.T) {
	in := buildFixture()
	in.Shape.HostInfra = true
	for i := range in.Shape.Workloads {
		in.Shape.Workloads[i].Unit = release.DefaultDeliveryUnit
	}
	if len(in.Shape.Workloads) == 0 {
		t.Fatal("the fixture must declare a workload for its unit to be recorded")
	}

	old := in
	old.Reader = &release.BundleReader{Features: withoutFeature(release.BundleFeatureShapePlacement)}
	if b := mustBuild(t, old); b.Digest != fixtureDigest || b.Doc.Shape.HostInfra || b.Doc.Shape.Workloads[0].Unit != "" {
		t.Fatalf("a reader without shape-placement must get the old bundle: digest %s host_infra %v unit %q",
			b.Digest, b.Doc.Shape.HostInfra, b.Doc.Shape.Workloads[0].Unit)
	}

	b := mustBuild(t, in)
	if !b.Doc.Shape.HostInfra || b.Doc.Shape.Workloads[0].Unit != release.DefaultDeliveryUnit {
		t.Fatalf("the nil reader decodes every feature: %+v", b.Doc.Shape.Workloads)
	}
	if b.Digest == fixtureDigest {
		t.Fatal("the shape is inside the document, so the digest must cover its new fields")
	}
	if b.Doc.ConfigDigest != mustBuild(t, buildFixture()).Doc.ConfigDigest {
		t.Fatal("who applies a workload changes nobody's configuration: the config digest must not move")
	}
}

// withoutFeature is every feature this forge writes except one: a control
// plane exactly one release behind on it.
func withoutFeature(drop string) []string {
	var out []string
	for _, f := range release.BundleDocFeatures() {
		if f != drop {
			out = append(out, f)
		}
	}
	return out
}
