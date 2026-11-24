package release

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
)

const (
	sharedShop = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	coverShop  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	sharedEdge = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// A release cut with a cover variant: the shop image's shared digest (what
// staging ran and prod will run) and, beside it, its cover build. The edge
// image has no cover build.
func coverRelease() Release {
	return Release{Version: "v1.4.0", Artifacts: map[string]Artifact{
		"ghcr.io/acme/shop":       {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sharedShop}},
		"ghcr.io/acme/shop#cover": {Kind: KindOCI, Mode: ModeVariant, Digests: map[string]string{CoverVariant: coverShop}},
		"ghcr.io/acme/edge":       {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: sharedEdge}},
	}}
}

// The rule the cover variant lives by: a release that CARRIES a cover build
// promotes to prod, and prod resolves the shared digest. Never refused for
// carrying it.
func TestCoverVariant_ReleaseWithCoverPromotesToProdOnShared(t *testing.T) {
	r := coverRelease()
	if err := r.Validate(); err != nil {
		t.Fatalf("a release with shared + cover is valid: %v", err)
	}
	prod := r.PinsFor("")
	want := map[string]string{"ghcr.io/acme/shop": sharedShop, "ghcr.io/acme/edge": sharedEdge}
	if !maps.Equal(prod, want) {
		t.Errorf("prod pins = %v, want the shared digests %v", prod, want)
	}
	if !maps.Equal(prod, r.SharedDigests()) {
		t.Errorf("a persistent env's pins must be exactly SharedDigests (what an older forge pins)")
	}
	if err := r.CheckPins("", prod); err != nil {
		t.Errorf("promoting a release that carries a cover variant to prod was refused: %v", err)
	}
	// The promotion an older code path writes is the same: NewPromotion pins
	// the shared digests.
	if p := NewPromotion("prod", r, KindPromote); !maps.Equal(p.Resolved, want) {
		t.Errorf("NewPromotion resolved %v, want %v", p.Resolved, want)
	}
}

// What IS refused: a cover digest placed on a persistent env, however it got
// into the pin set.
func TestCoverVariant_ForcedCoverDigestOnProdIsRefused(t *testing.T) {
	r := coverRelease()
	forced := map[string]string{"ghcr.io/acme/shop": coverShop, "ghcr.io/acme/edge": sharedEdge}
	err := r.CheckPins("", forced)
	if !errors.Is(err, ErrVariantOnPersistentEnv) || !strings.Contains(err.Error(), "ghcr.io/acme/shop@"+coverShop) {
		t.Fatalf("err = %v, want ErrVariantOnPersistentEnv naming the cover digest", err)
	}
	// The same digest under another repository's key is still the cover build.
	if err := r.CheckPins("", map[string]string{"ghcr.io/acme/elsewhere": coverShop}); !errors.Is(err, ErrVariantOnPersistentEnv) {
		t.Errorf("a cover digest under another key must still be refused, got %v", err)
	}
}

// A preview runs the cover build where there is one, and the shared digest
// for everything else.
func TestCoverVariant_PreviewRunsTheCoverBuild(t *testing.T) {
	r := coverRelease()
	preview := r.PinsFor(CoverVariant)
	want := map[string]string{"ghcr.io/acme/shop": coverShop, "ghcr.io/acme/edge": sharedEdge}
	if !maps.Equal(preview, want) {
		t.Errorf("preview pins = %v, want %v", preview, want)
	}
	if err := r.CheckPins(CoverVariant, preview); err != nil {
		t.Errorf("a preview's own pins were refused: %v", err)
	}
	if got := r.VariantDigests(CoverVariant); !maps.Equal(got, map[string]string{"ghcr.io/acme/shop": coverShop}) {
		t.Errorf("VariantDigests = %v", got)
	}
	// The release itself still holds the shared artifact: the variant rides
	// beside it, never in its place.
	if d, ok := r.Artifacts["ghcr.io/acme/shop"].SharedDigest(); !ok || d != sharedShop {
		t.Errorf("the shared artifact changed: %q %v", d, ok)
	}
}

func TestVariantArtifactName(t *testing.T) {
	name := VariantArtifactName("localhost:5050/acme/shop", CoverVariant)
	if name != "localhost:5050/acme/shop#cover" {
		t.Fatalf("name = %q", name)
	}
	if repo, v, ok := SplitVariantArtifactName(name); !ok || repo != "localhost:5050/acme/shop" || v != CoverVariant {
		t.Errorf("split = %q %q %v", repo, v, ok)
	}
	for _, n := range []string{"ghcr.io/acme/shop", "#cover", "shop#"} {
		if _, _, ok := SplitVariantArtifactName(n); ok {
			t.Errorf("%q is not a variant artifact name", n)
		}
	}
}

// A variant artifact must be a variant image whose only digest is under its
// own variant: anything else under that name would be pinned by no one.
func TestCoverVariant_ValidateShape(t *testing.T) {
	for name, a := range map[string]Artifact{
		"shared under a variant name": {Kind: KindOCI, Mode: ModeShared, Digests: map[string]string{SharedVariant: coverShop}},
		"wrong key":                   {Kind: KindOCI, Mode: ModeVariant, Digests: map[string]string{"debug": coverShop}},
		"two keys":                    {Kind: KindOCI, Mode: ModeVariant, Digests: map[string]string{CoverVariant: coverShop, "debug": sharedShop}},
	} {
		r := coverRelease()
		r.Artifacts["ghcr.io/acme/shop#cover"] = a
		if err := r.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
}

// An older forge decodes the release and reads only its shared digests: the
// variant artifact is a shape it already knows (mode variant), so nothing in
// it is new to a strict decoder either.
func TestCoverVariant_OlderReaderSeesOnlyShared(t *testing.T) {
	raw, err := json.Marshal(coverRelease())
	if err != nil {
		t.Fatal(err)
	}
	var back Release
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(back.SharedDigests(), map[string]string{"ghcr.io/acme/shop": sharedShop, "ghcr.io/acme/edge": sharedEdge}) {
		t.Errorf("SharedDigests after a round trip = %v", back.SharedDigests())
	}
}
