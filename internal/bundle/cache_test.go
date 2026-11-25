package bundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// THE LOCAL LAYOUT ROUND TRIP. A `local` env's bundle is written here even
// when its control plane is reachable, because it is not SHIPPED (doc §4.3) —
// so this path has to be as trustworthy as the registry one, and it is the
// same path: the layout is a [Resolver], so Fetch reads a local bundle
// through exactly the verification a registry bundle goes through.
func TestLocalLayoutRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "oci")
	layout, err := NewLocalLayout(root)
	if err != nil {
		t.Fatalf("NewLocalLayout: %v", err)
	}
	built := mustBuild(t, buildFixture())

	digest, err := layout.Write(ctx, built)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if digest != built.Digest {
		t.Fatalf("Write returned %s, want %s", digest, built.Digest)
	}

	// It IS an OCI image layout, not a forge-shaped directory. That is
	// what lets `oras`, `crane` or a registry push read it without forge.
	for _, name := range []string{"oci-layout", "index.json", "blobs"} {
		if _, serr := os.Stat(filepath.Join(root, name)); serr != nil {
			t.Errorf("the layout has no %s: %v", name, serr)
		}
	}

	// Read back BY DIGEST…
	byDigest, err := Fetch(ctx, layout, digest)
	if err != nil {
		t.Fatalf("Fetch by digest: %v", err)
	}
	if byDigest.Doc.Env != built.Doc.Env || byDigest.Digest != digest {
		t.Errorf("fetched %s/%s", byDigest.Digest, byDigest.Doc.Env)
	}
	if byDigest.Doc.ConfigDigest != built.Doc.ConfigDigest {
		t.Error("the config digest did not survive the layout")
	}

	// …and BY ENV TAG, which is what makes "the newest bundle for this
	// env" addressable without the ledger having to hold a pointer.
	byTag, err := Fetch(ctx, layout, built.Doc.Env)
	if err != nil {
		t.Fatalf("Fetch by env tag: %v", err)
	}
	if byTag.Digest != digest {
		t.Errorf("the %s tag resolves to %s, want %s", built.Doc.Env, byTag.Digest, digest)
	}

	// The reference a ledger records names BOTH halves: a digest alone
	// does not say which machine's layout holds the blobs, and a file
	// ledger is machine-scoped.
	ref := layout.Reference(digest)
	if !strings.Contains(ref, root) || !strings.Contains(ref, digest) {
		t.Errorf("Reference(%s) = %q, which does not name both the layout and the digest", digest, ref)
	}
}

// Writing the same bundle twice is one artifact, and the env tag moves to the
// newest. Re-running `forge env up` must not accumulate copies of an
// unchanged render.
func TestLocalLayoutWriteIsIdempotentAndRetagsTheEnv(t *testing.T) {
	ctx := context.Background()
	layout, err := NewLocalLayout(filepath.Join(t.TempDir(), "oci"))
	if err != nil {
		t.Fatal(err)
	}
	built := mustBuild(t, buildFixture())
	first, err := layout.Write(ctx, built)
	if err != nil {
		t.Fatal(err)
	}
	again, err := layout.Write(ctx, built)
	if err != nil {
		t.Fatalf("re-writing identical content must succeed: %v", err)
	}
	if again != first {
		t.Fatalf("the same render produced two digests: %s and %s", first, again)
	}

	// A CHANGED render is a second bundle, and the env tag follows it —
	// while the first stays addressable by digest, because the ledger
	// still references it.
	changed := buildFixture()
	changed.Shape.Manifests = strings.Replace(changed.Shape.Manifests, "replicas: 3", "replicas: 5", 1)
	second, err := layout.Write(ctx, mustBuild(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a changed render did not produce a new digest")
	}
	tagged, err := Fetch(ctx, layout, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if tagged.Digest != second {
		t.Errorf("the env tag points at %s, want the newest %s", tagged.Digest, second)
	}
	if _, err := Fetch(ctx, layout, first); err != nil {
		t.Errorf("the superseded bundle is no longer addressable by digest: %v", err)
	}
}

func TestNewLocalLayoutRefusesAnEmptyDirectory(t *testing.T) {
	if _, err := NewLocalLayout("  "); err == nil {
		t.Fatal("an empty layout directory must be refused")
	}
}

// ─── The cache ──────────────────────────────────────────────────────────────

// A DIGEST NAMES BYTES, which is what lets the cache skip every freshness
// check: if a reference still resolves to a digest already on disk, the
// cached bytes ARE the current bytes.
//
// Stats is how that is asserted. A test that inferred the hit path from
// TIMING would pass against an implementation that pulled every time, on a
// fast enough machine — the flaky-and-vacuous shape this project has been
// bitten by before.
func TestCacheSecondGetIsAHeadWithNoPull(t *testing.T) {
	ctx := context.Background()
	built := mustBuild(t, buildFixture())
	store := newMemStore(built)
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	first, stats, err := cache.Get(ctx, store, built.Digest)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if !stats.Resolved || !stats.Pulled {
		t.Fatalf("the first Get must resolve AND pull; got %+v", stats)
	}
	if stats.Digest != built.Digest {
		t.Errorf("stats digest %s, want %s", stats.Digest, built.Digest)
	}
	if len(first.ManifestFiles) == 0 {
		t.Fatal("the cache entry lists no manifest files")
	}

	second, stats, err := cache.Get(ctx, store, built.Digest)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !stats.Resolved {
		t.Error("the hot path must still resolve: it is what proves the bundle is still in the registry")
	}
	if stats.Pulled {
		t.Error("the second Get transferred content; an unchanged digest must cost one HEAD")
	}
	if second.Digest != first.Digest || second.Doc.ConfigDigest != first.Doc.ConfigDigest {
		t.Error("the cache hit returned a different bundle")
	}
	// The hit must list the same files, in the same order: that order is
	// the apply order.
	if strings.Join(second.ManifestFiles, ",") != strings.Join(first.ManifestFiles, ",") {
		t.Errorf("the cache hit changed the apply order:\n %v\n %v", first.ManifestFiles, second.ManifestFiles)
	}
}

func TestCacheUnpacksTheManifestsOnDisk(t *testing.T) {
	built := mustBuild(t, buildFixture())
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cached, _, err := cache.Get(context.Background(), newMemStore(built), built.Digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range cached.ManifestFiles {
		data, rerr := os.ReadFile(filepath.Join(cached.Dir, rel))
		if rerr != nil {
			t.Fatalf("%s: %v", rel, rerr)
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", rel)
		}
		if strings.Contains(string(data), canaryValue) {
			t.Fatalf("%s holds a Secret value on disk", rel)
		}
	}
	// The cache entry is SELF-DESCRIBING: the document sits beside the
	// manifests, so an entry can be read without the registry.
	if _, err := os.Stat(filepath.Join(cached.Dir, docFileName)); err != nil {
		t.Errorf("the cache entry carries no %s: %v", docFileName, err)
	}
	// Keyed by digest, split on the algorithm — so the tree never holds a
	// colon in a path component.
	if !strings.Contains(cached.Dir, filepath.Join("sha256", strings.TrimPrefix(built.Digest, "sha256:"))) {
		t.Errorf("cache dir %q is not keyed by the split digest", cached.Dir)
	}
}

// A CORRUPT cache entry is an error, not a miss. Silently re-pulling over it
// would hide the corruption forever, and this is the one place that would
// notice.
func TestCacheRefusesACorruptEntry(t *testing.T) {
	ctx := context.Background()
	built := mustBuild(t, buildFixture())
	store := newMemStore(built)
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cached, _, err := cache.Get(ctx, store, built.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(filepath.Join(cached.Dir, docFileName), []byte("{not json"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	if _, _, err := cache.Get(ctx, store, built.Digest); err == nil {
		t.Fatal("a corrupt cache entry must be reported, not silently re-pulled")
	}
}

// A non-canonical digest must never become a cache path. The platform's
// ledger enforces the same form in SQL.
func TestCacheRefusesANonCanonicalDigest(t *testing.T) {
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"", "sha256:xyz", "latest", "../../escape"} {
		if _, perr := cache.pathFor(d); !errors.Is(perr, release.ErrInvalid) {
			t.Errorf("pathFor(%q): want release.ErrInvalid, got %v", d, perr)
		}
	}
}

// Everything an unpacked bundle holds is under the manifests prefix, and
// ManifestFiles names all of it. There is no second tree: a bundle carries one
// layer, because that is the one Flux applies.
func TestCacheUnpacksOnlyTheManifestTree(t *testing.T) {
	built := mustBuild(t, buildFixture())
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cached, _, err := cache.Get(context.Background(), newMemStore(built), built.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(cached.ManifestFiles) == 0 {
		t.Fatal("the manifest tree unpacked empty")
	}
	for _, rel := range cached.ManifestFiles {
		if !strings.HasPrefix(rel, manifestsPrefix+"/") {
			t.Errorf("unpacked entry %s is outside %s/", rel, manifestsPrefix)
		}
		if _, serr := os.Stat(filepath.Join(cached.Dir, rel)); serr != nil {
			t.Errorf("ManifestFiles names %s, which is not on disk: %v", rel, serr)
		}
	}
}
