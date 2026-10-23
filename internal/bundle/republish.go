package bundle

// Republishing a bundle: moving bytes that already exist from one store to
// another, without re-rendering them.
//
// WHY THIS EXISTS. A machine-ledger env's bundle is written into the ledger's
// own OCI LAYOUT — a directory — because a local env's bundle is not shipped.
// That is the right default and it holds right up until a reconciler has to
// fetch it: Flux cannot read a directory on a developer's laptop. So a
// bundle is published to a registry before anything points a reconciler at it.
//
// REPUBLISHING IS NOT REBUILDING, and the distinction is the whole safety
// property. [Build] renders; this copies. If the deploy re-rendered instead,
// the bytes Flux fetched would be a function of the checkout at DEPLOY time —
// a moved sibling repo, a different forge, a dirty tree — and the digest the
// ledger recorded would describe something else. Here the digest is an INPUT:
// the bytes are read by it, verified against it, and written under it, so the
// artifact Flux applies is byte-identical to the one the ledger named or the
// operation fails.
//
// IT IS IDEMPOTENT, because a bundle is content-addressed and a registry
// treats an existing blob as success ([Push] says why). So a deploy may
// republish on every run without cutting a second record of the same bundle,
// and a retry after a lost response costs nothing.

import (
	"context"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/reliant-labs/forge/pkg/release"
)

// Republish copies the bundle at `digest` from `src` to `dst` and returns the
// digest it wrote.
//
// EVERY BYTE IS VERIFIED ON THE WAY THROUGH, by the same [readBlob] the
// registry fetch path uses: each blob is hashed as it is read and the read
// fails if the hash diverges. That is not belt-and-braces over a local
// directory — it is what makes the claim "Flux applies the bytes the ledger
// recorded" true rather than assumed, and the local layout is a directory a
// human can edit.
//
// The returned digest is the one actually written, and a caller should pin its
// pointer to THAT rather than to the digest it asked for. They are equal — the
// manifest is read by digest and pushed unmodified — and returning it keeps
// the caller from having to take that on trust.
func Republish(ctx context.Context, src Resolver, dst Pusher, digest string) (string, error) {
	if !release.ValidDigest(digest) {
		return "", fmt.Errorf("%w: republish needs a canonical sha256 digest, got %q", release.ErrInvalid, digest)
	}
	desc, err := src.Resolve(ctx, digest)
	if err != nil {
		return "", fmt.Errorf("resolve bundle %s in its local layout: %w", digest, err)
	}
	if desc.MediaType != ocispec.MediaTypeImageManifest {
		return "", fmt.Errorf("%w: %s is a %s, not an OCI image manifest", ErrNotBundle, digest, desc.MediaType)
	}
	raw, err := readBlob(ctx, src, desc, MaxManifestBytes)
	if err != nil {
		return "", fmt.Errorf("read bundle manifest %s: %w", digest, err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("%w: bundle manifest %s does not parse: %v", release.ErrInvalid, digest, err)
	}
	if manifest.ArtifactType != release.BundleArtifactType {
		return "", fmt.Errorf("%w: %s has artifactType %q, want %q",
			ErrNotBundle, digest, manifest.ArtifactType, release.BundleArtifactType)
	}

	// Blobs BEFORE the manifest, for the reason [Push] states: a manifest
	// naming a blob the registry does not hold is a dangling artifact —
	// resolvable, recordable, unfetchable — and an OCIRepository pointed at
	// one fails on a digest that demonstrably exists in the record.
	for _, blobDesc := range append([]ocispec.Descriptor{manifest.Config}, manifest.Layers...) {
		if err := copyBlob(ctx, src, dst, blobDesc); err != nil {
			return "", fmt.Errorf("republish bundle %s: %w", digest, err)
		}
	}
	if err := pushBlob(ctx, dst, blob{desc: desc, data: raw}); err != nil {
		return "", fmt.Errorf("republish bundle %s manifest: %w", digest, err)
	}
	return desc.Digest.String(), nil
}

// copyBlob reads one blob, verified, and writes it.
//
// Held in memory rather than streamed, which is sound for a bundle and
// deliberate: the bound is [MaxLayerBytes], a bundle is small by design (doc
// §4.1 measures control-plane prod at 246 KB), and verifying a digest requires
// having hashed every byte anyway. Streaming would mean either pushing
// unverified bytes or buffering them regardless.
func copyBlob(ctx context.Context, src Resolver, dst Pusher, desc ocispec.Descriptor) error {
	data, err := readBlob(ctx, src, desc, MaxLayerBytes)
	if err != nil {
		return fmt.Errorf("read %s (%s): %w", desc.MediaType, desc.Digest, err)
	}
	// readBlob already verified the digest; re-describing the bytes is how
	// the PUSH descriptor is derived from what we hold rather than from
	// what we were told, so the two cannot disagree.
	return pushBlob(ctx, dst, blob{desc: content.NewDescriptorFromBytes(desc.MediaType, data), data: data})
}
