package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestRepositoryAppendsTheReservedSegment(t *testing.T) {
	for base, want := range map[string]string{
		"ghcr.io/acme":  "ghcr.io/acme/bundle.v1/prod",
		"ghcr.io/acme/": "ghcr.io/acme/bundle.v1/prod",
		" ghcr.io/acme": "ghcr.io/acme/bundle.v1/prod",
	} {
		if got := Repository(base, "prod"); got != want {
			t.Errorf("Repository(%q) = %q, want %q", base, got, want)
		}
	}
	// THE DOT IS LOAD-BEARING: an RFC-1123 build name cannot contain one,
	// so no backend image an org builds can collide with this subtree.
	if !strings.Contains(RepositorySegment, ".") {
		t.Error("the bundle segment must contain a dot, or a build name could collide with it")
	}
}

// Push against an in-process oras target, then read the same bundle back
// through Fetch. The round trip is the test: a push whose bytes Fetch refuses
// would be a build that fails at the last step, and neither half proves that
// alone.
func TestPushAndFetchRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	built := mustBuild(t, buildFixture())

	digest, err := Push(ctx, store, built)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if digest != built.Digest {
		t.Fatalf("Push returned %s, want the built digest %s", digest, built.Digest)
	}

	// Re-pushing is IDEMPOTENT. A bundle's digest is a pure function of
	// its content, so `already exists` is success — otherwise an
	// unavoidable re-push (a retried deploy) would look like a failure.
	if again, aerr := Push(ctx, store, built); aerr != nil || again != digest {
		t.Fatalf("re-push: %s, %v — a re-push of identical content must succeed", again, aerr)
	}

	fetched, err := Fetch(ctx, newMemStore(built), digest)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetched.Digest != digest {
		t.Errorf("fetched digest %s, want %s", fetched.Digest, digest)
	}
	if fetched.Doc.Env != "prod" || fetched.Doc.Project != "shop" {
		t.Errorf("fetched document identity %s/%s", fetched.Doc.Project, fetched.Doc.Env)
	}
	if fetched.Doc.ConfigDigest != built.Doc.ConfigDigest {
		t.Error("the config digest did not survive the round trip")
	}
	layer, _ := built.Layer(release.BundleManifestsLayer)
	if !bytes.Equal(fetched.Manifests, layer) {
		t.Error("the manifest layer did not survive the round trip byte-for-byte")
	}
}

// The manifest must be pushed AFTER the blobs it names. A manifest referencing
// a blob the registry does not hold is resolvable, recordable and
// unfetchable — doc §13's F-4, created by the pusher rather than by a GC bug.
func TestPushWritesBlobsBeforeTheManifest(t *testing.T) {
	built := mustBuild(t, buildFixture())
	rec := &recordingPusher{}
	if _, err := Push(context.Background(), rec, built); err != nil {
		t.Fatal(err)
	}
	last := rec.order[len(rec.order)-1]
	if last != ocispec.MediaTypeImageManifest {
		t.Fatalf("push order %v ends with %s; the manifest must go last", rec.order, last)
	}
	for _, mt := range rec.order[:len(rec.order)-1] {
		if mt == ocispec.MediaTypeImageManifest {
			t.Fatalf("the manifest was pushed before a blob it names: %v", rec.order)
		}
	}
}

func TestPushRefusesAnUnbuiltBundle(t *testing.T) {
	if _, err := Push(context.Background(), memory.New(), Bundle{Digest: "sha256:x"}); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("a Bundle not produced by Build must be refused; got %v", err)
	}
}

// ─── Fetch refusals ─────────────────────────────────────────────────────────

// A TAMPERED BLOB is the refusal everything else rests on. Without it a
// substituted layer would be applied as though it were the bytes the ledger
// recorded, and content addressing would be decoration.
func TestFetchRefusesATamperedBlob(t *testing.T) {
	ctx := context.Background()
	built := mustBuild(t, buildFixture())
	_, err := Fetch(ctx, &tamperingResolver{
		Resolver: newMemStore(built),
		corrupt:  release.BundleManifestsLayer,
	}, built.Digest)
	if err == nil {
		t.Fatal("Fetch accepted a layer whose bytes did not hash to its descriptor")
	}
	if !strings.Contains(err.Error(), "verification") {
		t.Errorf("the refusal should name the verification failure: %v", err)
	}

	// The same guard on the CONFIG blob, which is where the shape and the
	// provenance live — the half a ledger records.
	if _, cerr := Fetch(ctx, &tamperingResolver{
		Resolver: newMemStore(built),
		corrupt:  release.BundleConfigMediaType,
	}, built.Digest); cerr == nil {
		t.Fatal("Fetch accepted a tampered config blob")
	}
}

// The platform's ledger enforces canonical `sha256:<64 hex>` in SQL, so a
// looser rule here would let forge pull something the server could never have
// recorded.
func TestFetchRefusesANonCanonicalDigest(t *testing.T) {
	for name, d := range map[string]string{
		"uppercase hex": "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"short hex":     "sha256:abcd",
		"another algo":  "sha512:" + strings.Repeat("a", 64),
		"no prefix":     strings.Repeat("a", 64),
	} {
		_, err := Fetch(context.Background(), fixedResolver{desc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.Digest(d),
			Size:      10,
		}}, "ref")
		if !errors.Is(err, release.ErrInvalid) {
			t.Errorf("%s (%s): want release.ErrInvalid, got %v", name, d, err)
		}
	}
}

// An oversized layer is refused from its DESCRIPTOR, before any bytes move: a
// bomb that declares its size should not cost a download to reject.
func TestFetchRefusesAnOversizedLayer(t *testing.T) {
	ctx := context.Background()
	built := mustBuild(t, buildFixture())
	inflated := patchedManifest(t, built, func(m *ocispec.Manifest) {
		for i := range m.Layers {
			if m.Layers[i].MediaType == release.BundleManifestsLayer {
				m.Layers[i].Size = MaxLayerBytes + 1
			}
		}
	})
	_, err := Fetch(ctx, inflated, built.Digest)
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive for an oversized layer, got %v", err)
	}
	if !strings.Contains(err.Error(), "bound") {
		t.Errorf("the refusal should say which bound was exceeded: %v", err)
	}
}

// A blob that UNDER-DECLARES its size must still be refused, on the read
// side. A descriptor's Size is a claim, not a fact.
func TestFetchRefusesABlobThatUnderDeclaresItsSize(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), int(MaxManifestBytes)+1024)
	_, err := Fetch(context.Background(), oversizedBodyResolver{
		desc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    content.NewDescriptorFromBytes("", huge).Digest,
			Size:      10, // the lie
		},
		body: huge,
	}, "ref")
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("want ErrUnsafeArchive for an under-declared blob, got %v", err)
	}
}

// A tag can be moved, and the repository layout is a convention rather than a
// guarantee — so the ARTIFACT has to say what it is.
func TestFetchRefusesAForeignArtifact(t *testing.T) {
	ctx := context.Background()
	foreign := ocispec.Manifest{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: "application/vnd.forge.staticsite.v1",
		Config:       ocispec.DescriptorEmptyJSON,
	}
	raw, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	if _, err := Fetch(ctx, blobResolver{desc: desc, body: raw}, "ref"); !errors.Is(err, ErrNotBundle) {
		t.Fatalf("want ErrNotBundle for a static-site artifact, got %v", err)
	}
}

// An unknown layer is REFUSED, not ignored: it may be the one carrying what a
// deploy needs, and dropping it would apply an incomplete bundle while
// reporting success.
func TestFetchRefusesAnUnknownLayer(t *testing.T) {
	ctx := context.Background()
	built := mustBuild(t, buildFixture())
	relabeled := patchedManifest(t, built, func(m *ocispec.Manifest) {
		for i := range m.Layers {
			if m.Layers[i].MediaType == release.BundleManifestsLayer {
				m.Layers[i].MediaType = "application/vnd.someone.else.v1.tar+gzip"
			}
		}
	})
	_, err := Fetch(ctx, relabeled, built.Digest)
	if !errors.Is(err, ErrNotBundle) {
		t.Fatalf("want ErrNotBundle for an unrecognised layer, got %v", err)
	}
}

// ─── doubles ────────────────────────────────────────────────────────────────

// memStore serves a built bundle's own blobs, resolving by digest. It stands
// in for a registry: Push already put the bytes in, so the double only has to
// hand back the descriptor the registry's HEAD would.
type memStore struct {
	blobs    map[digest.Digest]blob
	manifest ocispec.Descriptor
}

func newMemStore(b Bundle) *memStore {
	s := &memStore{blobs: map[digest.Digest]blob{}}
	for _, bl := range b.blobs {
		s.blobs[bl.desc.Digest] = bl
		if bl.desc.MediaType == ocispec.MediaTypeImageManifest {
			s.manifest = bl.desc
		}
	}
	return s
}

func (m *memStore) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return m.manifest, nil
}

func (m *memStore) Fetch(_ context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	bl, ok := m.blobs[target.Digest]
	if !ok {
		return nil, errors.New("no such blob: " + target.Digest.String())
	}
	return io.NopCloser(bytes.NewReader(bl.data)), nil
}

type recordingPusher struct{ order []string }

func (r *recordingPusher) Push(_ context.Context, desc ocispec.Descriptor, content io.Reader) error {
	if _, err := io.Copy(io.Discard, content); err != nil {
		return err
	}
	r.order = append(r.order, desc.MediaType)
	return nil
}

// fixedResolver resolves to one descriptor and refuses to fetch, so a test
// about the resolve step cannot accidentally depend on the fetch.
type fixedResolver struct{ desc ocispec.Descriptor }

func (f fixedResolver) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return f.desc, nil
}

func (f fixedResolver) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, errors.New("this test must not reach the fetch")
}

// blobResolver serves one blob by descriptor.
type blobResolver struct {
	desc ocispec.Descriptor
	body []byte
}

func (b blobResolver) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return b.desc, nil
}

func (b blobResolver) Fetch(_ context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	if target.Digest == b.desc.Digest {
		return io.NopCloser(bytes.NewReader(b.body)), nil
	}
	return io.NopCloser(bytes.NewReader(ocispec.DescriptorEmptyJSON.Data)), nil
}

// oversizedBodyResolver serves a body much larger than its descriptor claims.
type oversizedBodyResolver struct {
	desc ocispec.Descriptor
	body []byte
}

func (o oversizedBodyResolver) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return o.desc, nil
}

func (o oversizedBodyResolver) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.body)), nil
}

// tamperingResolver flips a byte in one media type's blob on the way out.
type tamperingResolver struct {
	Resolver
	corrupt string
}

func (tr *tamperingResolver) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := tr.Resolver.Fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	if target.MediaType != tr.corrupt {
		return rc, nil
	}
	defer func() { _ = rc.Close() }()
	data, rerr := io.ReadAll(rc)
	if rerr != nil {
		return nil, rerr
	}
	if len(data) == 0 {
		return nil, errors.New("nothing to tamper with")
	}
	data[len(data)/2] ^= 0xff
	return io.NopCloser(bytes.NewReader(data)), nil
}

// patchedManifest re-serves a built bundle with its OCI manifest mutated and
// its manifest descriptor RE-DERIVED, so the artifact stays self-consistent.
//
// Re-deriving is the point. A double that patched the manifest bytes while
// keeping the original digest would be testing the digest verification all
// over again, and every one of these tests would pass for the wrong reason —
// green on a Fetch that had lost the check they are actually about.
func patchedManifest(t *testing.T, b Bundle, patch func(*ocispec.Manifest)) *memStore {
	t.Helper()
	var manifest ocispec.Manifest
	if err := json.Unmarshal(b.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	patch(&manifest)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)

	store := newMemStore(b)
	delete(store.blobs, store.manifest.Digest)
	store.blobs[desc.Digest] = blob{desc: desc, data: raw}
	store.manifest = desc
	return store
}
