package bundle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// collectingPusher keeps what it was pushed, so a test can assert that the
// bytes which arrived are the bytes that were asked for.
type collectingPusher struct {
	got   map[string][]byte
	order []string
	fail  error
}

func newCollectingPusher() *collectingPusher {
	return &collectingPusher{got: map[string][]byte{}}
}

func (c *collectingPusher) Push(_ context.Context, desc v1.Descriptor, r io.Reader) error {
	if c.fail != nil {
		return c.fail
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	c.got[desc.Digest.String()] = data
	c.order = append(c.order, desc.MediaType)
	return nil
}

// TestRepublish_MovesTheEXACTBytesTheDigestNames is the property that lets a
// reconciled deploy publish a recorded bundle at all.
//
// Republishing is NOT rebuilding. If the deploy re-rendered instead, the bytes
// Flux fetched would be a function of the checkout at deploy time — a moved
// sibling repo, a different forge, a dirty tree — and the digest the ledger
// recorded would describe something else. Here the digest is an INPUT, so the
// artifact that lands is byte-identical to the one the ledger named.
func TestRepublish_MovesTheEXACTBytesTheDigestNames(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	src := newMemStore(built)
	dst := newCollectingPusher()

	got, err := Republish(context.Background(), src, dst, built.Digest)
	if err != nil {
		t.Fatalf("Republish: %v", err)
	}
	if got != built.Digest {
		t.Fatalf("Republish returned %s, want the digest it was asked for (%s)", got, built.Digest)
	}

	// Every blob the bundle consists of arrived, byte for byte.
	for _, bl := range built.blobs {
		arrived, ok := dst.got[bl.desc.Digest.String()]
		if !ok {
			t.Errorf("%s (%s) was never pushed", bl.desc.MediaType, bl.desc.Digest)
			continue
		}
		if !bytes.Equal(arrived, bl.data) {
			t.Errorf("%s arrived with different bytes than the source held", bl.desc.MediaType)
		}
	}
}

// TestRepublish_BlobsBeforeTheManifest pins the ordering. A manifest naming a
// blob the registry does not hold is a dangling artifact — resolvable,
// recordable, unfetchable — and an OCIRepository pointed at one fails with a
// not-found on a digest that demonstrably exists in the record.
func TestRepublish_BlobsBeforeTheManifest(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	dst := newCollectingPusher()
	if _, err := Republish(context.Background(), newMemStore(built), dst, built.Digest); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	last := dst.order[len(dst.order)-1]
	if last != v1.MediaTypeImageManifest {
		t.Errorf("push order = %v; the manifest must go LAST, after everything it names", dst.order)
	}
}

// TestRepublish_IsIdempotent pins that re-publishing identical bytes is free.
// A deploy republishes on every run, so an `already exists` that was treated
// as a failure would make the ordinary case red.
func TestRepublish_IsIdempotent(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	src := newMemStore(built)
	for i := 0; i < 3; i++ {
		if _, err := Republish(context.Background(), src, newCollectingPusher(), built.Digest); err != nil {
			t.Fatalf("republish %d: %v", i, err)
		}
	}
}

// TestRepublish_RefusesWhatIsNotABundle pins that the ARTIFACT has to say what
// it is. A repository layout is a convention, not a guarantee, so a reference
// resolving to something else must be refused rather than republished as
// though a reconciler could apply it.
func TestRepublish_RefusesWhatIsNotABundle(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	src := patchedManifest(t, built, func(m *v1.Manifest) { m.ArtifactType = "application/vnd.something.else" })
	_, err := Republish(context.Background(), src, newCollectingPusher(), src.manifest.Digest.String())
	if !errors.Is(err, ErrNotBundle) {
		t.Fatalf("Republish error = %v, want ErrNotBundle", err)
	}
}

// TestRepublish_RefusesANonCanonicalDigest pins that a tag cannot be
// republished. The whole point is that the digest is an input the bytes are
// verified against; a tag names mutable content and could not serve that.
func TestRepublish_RefusesANonCanonicalDigest(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	for _, ref := range []string{"v1.2.0", "latest", "sha256:abc", ""} {
		_, err := Republish(context.Background(), newMemStore(built), newCollectingPusher(), ref)
		if err == nil || !strings.Contains(err.Error(), "canonical") {
			t.Errorf("Republish(%q) error = %v, want a refusal naming the canonical form", ref, err)
		}
	}
}

// TestRepublish_FailsOnCorruptedSourceBytes is why every byte is verified on
// the way through. The local layout is a DIRECTORY a human can edit, so
// "Flux applies the bytes the ledger recorded" has to be checked rather than
// assumed — and the check is what turns a silent substitution into a refusal.
func TestRepublish_FailsOnCorruptedSourceBytes(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	src := newMemStore(built)
	// Corrupt the LAYER's bytes while leaving its descriptor's digest
	// alone — exactly what an edited file in the layout looks like.
	for d, bl := range src.blobs {
		if bl.desc.MediaType != v1.MediaTypeImageManifest && bl.desc.MediaType != "application/vnd.forge.bundle.config.v1+json" {
			corrupted := append([]byte(nil), bl.data...)
			corrupted[len(corrupted)-1] ^= 0xff
			src.blobs[d] = blob{desc: bl.desc, data: corrupted}
			break
		}
	}
	_, err := Republish(context.Background(), src, newCollectingPusher(), built.Digest)
	if err == nil {
		t.Fatal("Republish succeeded over corrupted source bytes; the digest verification is what makes " +
			"\"the recorded bytes\" and \"the applied bytes\" one claim")
	}
	if !strings.Contains(err.Error(), "digest verification") {
		t.Errorf("error = %v; it should name the verification that failed", err)
	}
}

// TestRepublish_PropagatesAPushFailure pins that a registry refusal is an
// error rather than a silent partial publish: a pointer written at a bundle
// that did not fully arrive would fail to fetch later, far from the cause.
func TestRepublish_PropagatesAPushFailure(t *testing.T) {
	t.Parallel()
	built := mustBuild(t, buildFixture())
	dst := newCollectingPusher()
	dst.fail = errors.New("401 Unauthorized")
	_, err := Republish(context.Background(), newMemStore(built), dst, built.Digest)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Republish error = %v, want the registry's own refusal", err)
	}
}

// TestCopyBlob_DescribesTheBytesItHolds pins that the PUSH descriptor is
// derived from the bytes actually read, not copied from the source's claim.
//
// The two can disagree — a descriptor is a claim about content, and the local
// layout is a directory — and if the push reused the claimed descriptor a
// substituted blob would be written under the digest it was supposed to have.
// Deriving it means a substitution is caught by the read's own verification
// before anything is pushed.
func TestCopyBlob_DescribesTheBytesItHolds(t *testing.T) {
	t.Parallel()
	data := []byte("some layer bytes")
	honest := content.NewDescriptorFromBytes("application/octet-stream", data)
	dst := newCollectingPusher()
	if err := copyBlob(context.Background(), staticResolver{desc: honest, data: data}, dst, honest); err != nil {
		t.Fatalf("copyBlob: %v", err)
	}
	if got := dst.got[honest.Digest.String()]; !bytes.Equal(got, data) {
		t.Errorf("pushed %q under %s, want the bytes read", got, honest.Digest)
	}

	// A descriptor naming a digest the bytes do not hash to is refused by
	// the read, so nothing is pushed under a digest it does not match.
	wrong := honest
	wrong.Digest = content.NewDescriptorFromBytes("application/octet-stream", []byte("different")).Digest
	empty := newCollectingPusher()
	if err := copyBlob(context.Background(), staticResolver{desc: wrong, data: data}, empty, wrong); err == nil {
		t.Error("copyBlob accepted bytes that do not hash to the digest naming them")
	}
	if len(empty.got) != 0 {
		t.Error("bytes were pushed despite failing verification")
	}
}

// staticResolver serves one blob, by descriptor, ignoring the digest asked
// for — so a test can control exactly what the read returns.
type staticResolver struct {
	desc v1.Descriptor
	data []byte
}

func (s staticResolver) Resolve(context.Context, string) (v1.Descriptor, error) { return s.desc, nil }
func (s staticResolver) Fetch(context.Context, v1.Descriptor) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
