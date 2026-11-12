package bundle

// The registry transport: pushing a bundle, and fetching one back.
//
// Both halves are expressed against NARROW interfaces declared here rather
// than against oras's Target, for the usual reason — the larger the
// interface, the weaker the abstraction, and this package needs three
// operations: push a blob, resolve a reference, read a blob. A registry
// client that grew into this package's public surface would make every future
// caller depend on oras's types to do anything, and would make the in-process
// test registry impossible to substitute.
//
// VERIFICATION IS NOT OPTIONAL AND NOT ADVISORY. Everything this package
// reads from a registry is hashed as it is read and the read FAILS if the
// hash diverges. That is the whole basis on which a bundle can be the record
// of what was shipped: without it, a substituted blob would be applied as
// though it were the bytes the ledger named.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/reliant-labs/forge/pkg/release"
)

// RepositorySegment is the path segment bundles live under, appended to an
// env's image push base: `<image_push_base>/bundle.v1/<env>`.
//
// THE DOT IS LOAD-BEARING, exactly as it is for static sites: an RFC-1123
// build name cannot contain one, so no backend image an org builds can
// collide with a bundle repository.
const RepositorySegment = "bundle.v1"

// Repository is where an env's bundles are pushed.
//
// The segment is APPENDED rather than asked for, so it never appears in
// anyone's KCL: which repository a bundle lands in is forge's layout
// decision, not something an author should have to encode.
func Repository(imagePushBase, env string) string {
	return strings.TrimSuffix(strings.TrimSpace(imagePushBase), "/") + "/" + RepositorySegment + "/" + env
}

// ErrNotBundle is the refusal for an artifact that is not a bundle: a
// reference that resolves to something else is a configuration mistake, not a
// corrupt bundle, and a caller should be able to tell those apart.
var ErrNotBundle = errors.New("forge: artifact is not a forge bundle")

// Pusher is the slice of a registry [Push] needs: one method.
type Pusher interface {
	Push(ctx context.Context, expected ocispec.Descriptor, content io.Reader) error
}

// Resolver is the slice [Fetch] needs — one method for the HEAD and one for
// the pull. Anything that satisfies it works: oras's remote.Repository does,
// and so does the in-memory store the tests use.
type Resolver interface {
	// Resolve maps a reference (tag or digest) to its descriptor. This is
	// the HEAD — it must not transfer the content.
	Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error)
	// Fetch reads the content a descriptor names.
	Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error)
}

// NewRepository builds a registry client for `registry/repo` (no tag — the
// tag or digest is passed to Resolve), authenticated from the ambient docker
// credential store — what a `docker push` to the same host would present.
//
// The transport retries on the registry's own throttling responses. That is a
// default rather than forge policy: a loop that polls a registry WILL
// eventually be rate-limited, and a client that treated a 429 as a hard
// failure would report an env as unobservable for the duration.
//
// A hosted env's bundle does NOT come through here: its credential is the
// control-plane identity, not whatever a store happens to hold for the host
// (see NewRepositoryWithCredential).
func NewRepository(ref string) (*remote.Repository, error) {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("bundle: read docker credentials: %w", err)
	}
	return NewRepositoryWithCredential(ref, credentials.Credential(store))
}

// NewRepositoryWithCredential is NewRepository with the credential supplied by
// the caller instead of read from the docker credential store.
//
// THE STORE IS NEVER CONSULTED. That is the point: a store entry is a
// long-lived value some earlier login left behind, and when it is stale it
// shadows everything fresher — the registry's realm rejects it and the push
// fails with a 401 no amount of correct credentials elsewhere can fix. A
// caller that holds the right identity in memory passes it here and the store
// cannot interfere, in either direction: nothing is read from it and nothing
// is written to it.
//
// The credential is exchanged at the registry's token realm for a short-lived
// access token on the first challenge; that token is what travels with the
// blob and manifest requests.
func NewRepositoryWithCredential(ref string, credential auth.CredentialFunc) (*remote.Repository, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errors.New("bundle: empty registry reference")
	}
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("bundle: parse registry reference %q: %w", ref, err)
	}
	repo.PlainHTTP = plainHTTPRegistry(repo.Reference.Registry)
	repo.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache(), Credential: credential}
	return repo, nil
}

// plainHTTPRegistry reports the registries reached over plain HTTP: the
// loopback and *.localhost names a local k3d registry is published under.
func plainHTTPRegistry(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasPrefix(h, "127.") || h == "host.k3d.internal"
}

// Push writes a built bundle's blobs and manifest to a target, and returns
// the manifest digest.
//
// `already exists` is SUCCESS, not an error. A bundle's digest is a pure
// function of its content, so a re-push of an unchanged render is the same
// artifact: treating the collision as a failure would make an idempotent
// operation look broken and would push callers toward "push once and hope"
// rather than "push whenever unsure".
//
// Blobs go before the manifest, because a manifest that referenced a blob
// the registry did not yet hold would be a dangling artifact — resolvable,
// recordable, and unfetchable. That is doc §13's F-4 shape, created by the
// pusher rather than by a GC bug.
func Push(ctx context.Context, target Pusher, b Bundle) (string, error) {
	if len(b.blobs) == 0 {
		return "", fmt.Errorf("%w: bundle has no blobs; it was not built by Build", release.ErrInvalid)
	}
	for _, bl := range b.blobs {
		// The manifest is pushed last, after everything it names.
		if bl.desc.MediaType == ocispec.MediaTypeImageManifest {
			continue
		}
		if err := pushBlob(ctx, target, bl); err != nil {
			return "", err
		}
	}
	for _, bl := range b.blobs {
		if bl.desc.MediaType != ocispec.MediaTypeImageManifest {
			continue
		}
		if err := pushBlob(ctx, target, bl); err != nil {
			return "", err
		}
	}
	return b.Digest, nil
}

func pushBlob(ctx context.Context, target Pusher, bl blob) error {
	if err := target.Push(ctx, bl.desc, newReader(bl.data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return fmt.Errorf("push %s (%s): %w", bl.desc.MediaType, bl.desc.Digest, err)
	}
	return nil
}

// Fetched is a bundle pulled back from a registry: the verified document plus
// the layer bytes, with the digest it was fetched by.
type Fetched struct {
	// Digest is the OCI manifest digest the reference resolved to, and the
	// digest the received bytes were verified against.
	Digest string
	// Doc is the config blob, strictly decoded.
	Doc release.BundleDoc
	// Manifests is the manifest layer, still packed. Unpack it with
	// [Unpack] — which is where the hostile-input discipline lives, and
	// the reason this is not handed back as a directory.
	Manifests []byte
}

// Fetch resolves a reference and pulls the bundle it names, verifying every
// byte against the digest that named it.
//
// Four refusals, and each one closes a way the record could lie:
//
//   - a reference resolving to something whose artifactType is not a
//     bundle's. A tag can be moved; the repository layout is a convention,
//     not a guarantee, so the ARTIFACT has to say what it is.
//   - a digest that is not canonical `sha256:<64 hex>`. The platform's
//     ledger enforces that form in SQL, so a looser rule here would let
//     forge pull something the server could never have recorded.
//   - content that does not hash to its descriptor's digest. This is the
//     one that matters: it is what makes "the recorded bytes" and "the
//     applied bytes" the same claim.
//   - a manifest, config or layer over its bound. The server bounds the
//     manifest and config at 1 MiB; forge refuses the same sizes.
func Fetch(ctx context.Context, res Resolver, reference string) (Fetched, error) {
	desc, err := res.Resolve(ctx, reference)
	if err != nil {
		return Fetched{}, fmt.Errorf("resolve bundle %q: %w", reference, err)
	}
	digest := desc.Digest.String()
	if !release.ValidDigest(digest) {
		return Fetched{}, fmt.Errorf("%w: bundle %q resolved to %q, not a canonical sha256 digest",
			release.ErrInvalid, reference, digest)
	}
	if desc.MediaType != ocispec.MediaTypeImageManifest {
		return Fetched{}, fmt.Errorf("%w: %s is a %s, not an OCI image manifest",
			ErrNotBundle, reference, desc.MediaType)
	}

	raw, err := readBlob(ctx, res, desc, MaxManifestBytes)
	if err != nil {
		return Fetched{}, fmt.Errorf("fetch bundle manifest %s: %w", digest, err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Fetched{}, fmt.Errorf("%w: bundle manifest %s does not parse: %v", release.ErrInvalid, digest, err)
	}
	if manifest.ArtifactType != release.BundleArtifactType {
		return Fetched{}, fmt.Errorf("%w: %s has artifactType %q, want %q",
			ErrNotBundle, reference, manifest.ArtifactType, release.BundleArtifactType)
	}
	if manifest.Config.MediaType != release.BundleConfigMediaType {
		return Fetched{}, fmt.Errorf("%w: %s config is %q, want %q",
			ErrNotBundle, reference, manifest.Config.MediaType, release.BundleConfigMediaType)
	}

	configBlob, err := readBlob(ctx, res, manifest.Config, MaxManifestBytes)
	if err != nil {
		return Fetched{}, fmt.Errorf("fetch bundle %s config: %w", digest, err)
	}
	doc, err := release.DecodeBundleDoc(configBlob)
	if err != nil {
		return Fetched{}, fmt.Errorf("bundle %s: %w", digest, err)
	}

	if len(manifest.Layers) != 1 {
		return Fetched{}, fmt.Errorf("%w: bundle must carry exactly one manifests layer", ErrNotBundle)
	}
	out := Fetched{Digest: digest, Doc: doc}
	for _, layer := range manifest.Layers {
		data, lerr := readBlob(ctx, res, layer, MaxLayerBytes)
		if lerr != nil {
			return Fetched{}, fmt.Errorf("fetch bundle %s layer %s: %w", digest, layer.MediaType, lerr)
		}
		switch layer.MediaType {
		case release.BundleManifestsLayer:
			out.Manifests = data
		default:
			// Refused rather than ignored. A layer forge does not
			// understand may be the one carrying what a deploy
			// needs, and silently dropping it would apply an
			// incomplete bundle while reporting success.
			return Fetched{}, fmt.Errorf("%w: bundle %s carries an unknown layer %q",
				ErrNotBundle, digest, layer.MediaType)
		}
	}
	if len(out.Manifests) == 0 {
		return Fetched{}, fmt.Errorf("%w: bundle %s carries no manifest layer", ErrNotBundle, digest)
	}
	if doc.Snapshot != nil {
		if err := release.ValidateSnapshotLayer(*doc.Snapshot, out.Manifests); err != nil {
			return Fetched{}, err
		}
	}
	return out, nil
}

// readBlob reads one descriptor's content, bounded by max and verified by
// HASHING THE BYTES IT RECEIVED.
//
// THE ORDER OF THE THREE CHECKS IS THE DESIGN:
//
//  1. the DECLARED size, before anything is transferred. A bomb that
//     honestly declares itself should cost nothing to reject.
//  2. the RECEIVED size, on the read side, bounded by a LimitReader. A
//     descriptor's Size is a claim — the shape this catches is a blob that
//     under-declares and then streams forever, which check 1 cannot see.
//  3. the digest, over exactly the bytes that will be returned.
//
// The verification hashes the received bytes HERE rather than through a
// streaming verifier, because a streaming verifier's Verify() reports
// "trailing data" the moment a bound truncates the read — so an oversized
// blob would be reported as a hash mismatch, and an operator chasing a
// corruption that was really a size refusal would be debugging the wrong
// thing. Hashing what we hold keeps each refusal's cause in its own message.
func readBlob(ctx context.Context, res Resolver, desc ocispec.Descriptor, max int64) ([]byte, error) {
	if desc.Size > max {
		return nil, fmt.Errorf("%w: %s declares %d bytes, over the %d-byte bound",
			ErrUnsafeArchive, desc.MediaType, desc.Size, max)
	}
	rc, err := res.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	// +1 so a blob that exceeds the bound while under-declaring its size
	// is distinguishable from one that is exactly at the bound.
	data, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes (its descriptor claimed %d)",
			ErrUnsafeArchive, desc.MediaType, max, desc.Size)
	}
	if got := content.NewDescriptorFromBytes(desc.MediaType, data).Digest; got != desc.Digest {
		return nil, fmt.Errorf("%s failed digest verification: received bytes hash to %s, not the %s its descriptor named",
			desc.MediaType, got, desc.Digest)
	}
	return data, nil
}

// newReader is bytes.NewReader, named so the push path reads as "push these
// bytes" rather than importing bytes into every call site.
func newReader(data []byte) io.Reader { return bytes.NewReader(data) }
