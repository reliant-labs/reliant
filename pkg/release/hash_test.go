package release

import "testing"

func shapeWith(objects ...ShapeObject) Shape {
	return Shape{Kind: EnvSelfManaged, Objects: objects}
}

func cdObj(name, hash, configHash string) ShapeObject {
	return ShapeObject{Cluster: "gke", Kind: "Deployment", Namespace: "ns", Name: name, Hash: hash, ConfigHash: configHash}
}

const (
	hashA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestConfigDigestIgnoresObjectOrder(t *testing.T) {
	forward, err := ConfigDigest(shapeWith(cdObj("api", hashA, hashB), cdObj("web", hashB, hashC)))
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := ConfigDigest(shapeWith(cdObj("web", hashB, hashC), cdObj("api", hashA, hashB)))
	if err != nil {
		t.Fatal(err)
	}
	if forward != reversed {
		t.Fatalf("config digest depends on render order:\n %s\n %s", forward, reversed)
	}
	if !ValidDigest(forward) {
		t.Fatalf("%q is not a canonical digest", forward)
	}
}

// The whole reason config_digest exists: a promotion must not move it.
func TestConfigDigestIsBlindToImageOnlyChanges(t *testing.T) {
	before, err := ConfigDigest(shapeWith(cdObj("api", hashA, hashC)))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ConfigDigest(shapeWith(cdObj("api", hashB, hashC)))
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("config digest moved on an images-only change:\n %s\n %s", before, after)
	}
}

func TestConfigDigestMovesOnAConfigChange(t *testing.T) {
	base, err := ConfigDigest(shapeWith(cdObj("api", hashA, hashB)))
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]Shape{
		"a changed body":    shapeWith(cdObj("api", hashA, hashC)),
		"a renamed object":  shapeWith(cdObj("api-v2", hashA, hashB)),
		"an added object":   shapeWith(cdObj("api", hashA, hashB), cdObj("web", hashC, hashC)),
		"a removed object":  shapeWith(),
		"a moved namespace": shapeWith(ShapeObject{Cluster: "gke", Kind: "Deployment", Namespace: "other", Name: "api", Hash: hashA, ConfigHash: hashB}),
		"a moved cluster":   shapeWith(ShapeObject{Cluster: "eks", Kind: "Deployment", Namespace: "ns", Name: "api", Hash: hashA, ConfigHash: hashB}),
	} {
		got, err := ConfigDigest(changed)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got == base {
			t.Errorf("%s did not move the config digest", name)
		}
	}
}

// An object that binds no release image has no ConfigHash to record, and its
// Hash already is its config hash. Dropping it would make the digest total
// over only part of the render.
func TestConfigDigestCoversObjectsWithNoConfigHash(t *testing.T) {
	withNone, err := ConfigDigest(shapeWith(cdObj("cm", hashA, "")))
	if err != nil {
		t.Fatal(err)
	}
	spelled, err := ConfigDigest(shapeWith(cdObj("cm", hashA, hashA)))
	if err != nil {
		t.Fatal(err)
	}
	if withNone != spelled {
		t.Fatalf("an absent ConfigHash is not treated as the Hash:\n %s\n %s", withNone, spelled)
	}
	if _, err := ConfigDigest(shapeWith(cdObj("cm", "", ""))); err == nil {
		t.Fatal("an object with no hash at all must refuse, not contribute nothing")
	}
}

func TestConfigDigestOfAnEmptyRender(t *testing.T) {
	got, err := ConfigDigest(shapeWith())
	if err != nil {
		t.Fatal(err)
	}
	if !ValidDigest(got) {
		t.Fatalf("an env that renders no objects still has a config digest; got %q", got)
	}
}
