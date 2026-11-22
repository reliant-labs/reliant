package sbomoci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// Where [Fetch] found an SBOM's bytes.
const (
	// SourceLedger is the machine ledger's content-addressed SBOM store.
	SourceLedger = "ledger"
	// SourceReferrer is the OCI referrer beside the artifact in its registry.
	SourceReferrer = "referrer"
)

// ErrNoSBOM is an artifact whose release record names no SBOM bytes: none
// was recorded, or its SBOMRef says unavailable.
var ErrNoSBOM = errors.New("the release records no sbom for this artifact")

// ErrSBOMUnavailable is a recorded SBOM that no copy could be read back
// from: not in the ledger, and no referrer holding those exact bytes.
var ErrSBOMUnavailable = errors.New("no copy of the recorded sbom could be read")

// maxSBOMBytes bounds what a referrer layer may claim before it is read. A
// CycloneDX document of a large image is a few MB; this is far past any real
// one and still refuses a registry that names an absurd size.
const maxSBOMBytes = 256 << 20

// maxManifestBytes bounds a referrer manifest read.
const maxManifestBytes = 4 << 20

// Fetch returns the bytes of artifact's recorded SBOM and where they came
// from ([SourceLedger] or [SourceReferrer]), verified against
// artifact.SBOM.Digest whatever the source.
//
// The release record is the index; a registry is never asked "what is
// attached" before the ledger is asked for the digest the record names:
//
//  1. the machine ledger copy, by digest (store may be nil: skipped);
//  2. the referrer the record names (SBOMRef.Referrer), once it proves to be
//     an SBOM referrer of subject: in subject's repository, an OCI image or
//     artifact manifest of type [ArtifactType] whose subject is subject's
//     digest, with a layer of that digest;
//  3. else subject's referrers of type [ArtifactType], each held to the same
//     check, filtered to the one holding that digest.
//
// subject is `<repository>@sha256:<hex>`. Empty skips the registry: with
// nothing to check a recorded referrer against it is not followed, and only
// the ledger copy can serve. A referrer that fails the check is never read
// past its manifest, so nothing it holds reaches the ledger.
//
// Bytes read from a referrer are cached into store, so the next read — and
// every scan — needs no network. A ledger copy that no longer matches its
// digest is passed over and repaired by that cache write.
func Fetch(ctx context.Context, store *ledgerfile.Store, artifact release.Artifact, subject string) ([]byte, string, error) {
	return FetchWith(ctx, store, artifact, subject, RegistryOptions{})
}

// FetchWith is [Fetch] with explicit registry options.
func FetchWith(ctx context.Context, store *ledgerfile.Store, artifact release.Artifact, subject string, opts RegistryOptions) ([]byte, string, error) {
	ref := artifact.SBOM
	if !ref.HasSBOM() {
		return nil, "", ErrNoSBOM
	}
	want, err := digest.Parse(ref.Digest)
	if err != nil || want.Algorithm() != digest.SHA256 {
		return nil, "", fmt.Errorf("recorded sbom digest %q is not a sha256 digest", ref.Digest)
	}

	var misses []error
	if store != nil {
		data, err := store.GetSBOM(ref.Digest)
		if err == nil {
			return data, SourceLedger, nil
		}
		misses = append(misses, fmt.Errorf("ledger: %w", err))
	}

	data, err := fetchFromRegistry(ctx, want, ref.Referrer, subject, opts)
	if err != nil {
		misses = append(misses, err)
		return nil, "", fmt.Errorf("%w (%s): %w", ErrSBOMUnavailable, want, errors.Join(misses...))
	}
	if store != nil {
		// A failed cache write loses nothing: the bytes are verified and in
		// hand, and the referrer still holds them.
		_, _ = store.PutSBOM(data)
	}
	return data, SourceReferrer, nil
}

// errUnverifiableReferrer is a recorded referrer Fetch was given no subject
// to check against: it is not followed.
var errUnverifiableReferrer = errors.New("not followed: no subject was given to check it against, so nothing proves it describes this artifact")

// fetchFromRegistry reads the SBOM with digest want from the named referrer,
// else by discovering subject's referrers.
//
// A recorded referrer is a claim the release record makes, and the record
// may be wrong or forged: it is read only once it proves to be an SBOM
// referrer of subject (see [checkReferrer]), so it can never make forge read
// and cache an unrelated object as this artifact's SBOM. With no subject
// there is nothing to check it against, and it is not followed at all.
func fetchFromRegistry(ctx context.Context, want digest.Digest, referrer, subject string, opts RegistryOptions) ([]byte, error) {
	if subject == "" {
		if referrer != "" {
			return nil, fmt.Errorf("referrer %s: %w", referrer, errUnverifiableReferrer)
		}
		return nil, errors.New("the record names no referrer and no subject was given to look one up by")
	}
	subjectRef, subjectDigest, err := parseSubject(subject)
	if err != nil {
		return nil, err
	}
	var misses []error
	if referrer != "" {
		data, err := fetchReferrer(ctx, want, referrer, subjectRef, subjectDigest, opts)
		if err == nil {
			return data, nil
		}
		misses = append(misses, fmt.Errorf("referrer %s: %w", referrer, err))
	}
	data, err := discover(ctx, want, subjectRef, subjectDigest, opts)
	if err == nil {
		return data, nil
	}
	misses = append(misses, fmt.Errorf("referrers of %s: %w", subject, err))
	return nil, errors.Join(misses...)
}

// fetchReferrer reads `<repository>@sha256:<hex>`, a referrer manifest that
// must live in the subject's own repository, and returns its layer holding
// want.
func fetchReferrer(ctx context.Context, want digest.Digest, referrer string, subject registry.Reference, subjectDigest digest.Digest, opts RegistryOptions) ([]byte, error) {
	ref, d, err := parseSubject(referrer)
	if err != nil {
		return nil, err
	}
	if ref.Registry != subject.Registry || ref.Repository != subject.Repository {
		return nil, fmt.Errorf("it is in repository %s/%s, not the subject's %s/%s", ref.Registry, ref.Repository, subject.Registry, subject.Repository)
	}
	repo, err := openRepository(ref.Registry+"/"+ref.Repository, opts)
	if err != nil {
		return nil, err
	}
	desc, err := repo.Resolve(ctx, d.String())
	if err != nil {
		return nil, err
	}
	return sbomFromManifest(ctx, repo, desc, want, subjectDigest)
}

// discover finds, among subject's SBOM referrers, the one holding want: by
// its AnnotationSBOMDigest first, then (a referrer another tool attached,
// which carries no such annotation) by reading each manifest's layers.
//
// Every candidate passes the same [checkReferrer] a recorded referrer does: a
// listing is the registry's (or, on a tag-fallback registry, any writer's)
// word, not proof.
func discover(ctx context.Context, want digest.Digest, ref registry.Reference, d digest.Digest, opts RegistryOptions) ([]byte, error) {
	repo, err := openRepository(ref.Registry+"/"+ref.Repository, opts)
	if err != nil {
		return nil, err
	}
	subjectDesc, err := resolveSubject(ctx, repo, d)
	if err != nil {
		return nil, err
	}
	listed, err := listReferrers(ctx, repo, subjectDesc)
	if err != nil {
		return nil, err
	}
	var annotated, others []ocispec.Descriptor
	for _, r := range listed {
		switch r.Annotations[AnnotationSBOMDigest] {
		case want.String():
			annotated = append(annotated, r)
		case "":
			others = append(others, r)
		}
	}
	var misses []error
	for _, r := range append(annotated, others...) {
		data, err := sbomFromManifest(ctx, repo, r, want, d)
		if err == nil {
			return data, nil
		}
		misses = append(misses, fmt.Errorf("%s: %w", r.Digest, err))
	}
	if len(misses) == 0 {
		return nil, fmt.Errorf("none of its %d sbom referrer(s) holds %s", len(listed), want)
	}
	return nil, errors.Join(misses...)
}

// mediaTypeArtifactManifest is the OCI artifact manifest of the image-spec
// v1.1 release candidates, which some registries and tools still write. It
// lists its content in blobs, not layers.
const mediaTypeArtifactManifest = "application/vnd.oci.artifact.manifest.v1+json"

// referrerManifestDoc is what an SBOM referrer manifest must say about
// itself: either manifest shape, decoded once.
type referrerManifestDoc struct {
	MediaType    string               `json:"mediaType"`
	ArtifactType string               `json:"artifactType"`
	Config       *ocispec.Descriptor  `json:"config"`
	Layers       []ocispec.Descriptor `json:"layers"`
	Blobs        []ocispec.Descriptor `json:"blobs"`
	Subject      *ocispec.Descriptor  `json:"subject"`
}

// checkReferrer refuses a manifest that is not an SBOM referrer of the
// subject with digest subjectDigest, and returns the descriptors that hold
// its content. mediaType is the manifest's own, else the one the registry
// served it under.
func checkReferrer(m referrerManifestDoc, mediaType string, subjectDigest digest.Digest) ([]ocispec.Descriptor, error) {
	if m.MediaType != "" {
		mediaType = m.MediaType
	}
	var held []ocispec.Descriptor
	artifactType := m.ArtifactType
	switch mediaType {
	case ocispec.MediaTypeImageManifest:
		held = m.Layers
		// OCI 1.1: an image manifest with no artifactType is typed by its
		// config, unless the config is the empty descriptor.
		if artifactType == "" && m.Config != nil && m.Config.MediaType != ocispec.MediaTypeEmptyJSON {
			artifactType = m.Config.MediaType
		}
	case mediaTypeArtifactManifest:
		held = m.Blobs
	default:
		return nil, fmt.Errorf("it is a %q, not an OCI image or artifact manifest", mediaType)
	}
	if artifactType != ArtifactType {
		return nil, fmt.Errorf("its artifact type is %q, not %s", artifactType, ArtifactType)
	}
	if m.Subject == nil {
		return nil, errors.New("it names no subject")
	}
	if m.Subject.Digest != subjectDigest {
		return nil, fmt.Errorf("its subject is %s, not %s", m.Subject.Digest, subjectDigest)
	}
	return held, nil
}

// sbomFromManifest reads the manifest desc names, refuses it unless
// [checkReferrer] accepts it as an SBOM referrer of subjectDigest, and
// returns the bytes of its layer with digest want, verified by
// content.ReadAll against the layer descriptor (digest and size) and then
// against want itself.
func sbomFromManifest(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor, want, subjectDigest digest.Digest) ([]byte, error) {
	if desc.Size > maxManifestBytes {
		return nil, fmt.Errorf("manifest %s is %d bytes, over the %d a referrer manifest may be", desc.Digest, desc.Size, maxManifestBytes)
	}
	rc, err := repo.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	raw, err := content.ReadAll(rc, desc)
	_ = rc.Close()
	if err != nil {
		return nil, err
	}
	var manifest referrerManifestDoc
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", desc.Digest, err)
	}
	layers, err := checkReferrer(manifest, desc.MediaType, subjectDigest)
	if err != nil {
		return nil, fmt.Errorf("manifest %s is not an sbom referrer of %s: %w", desc.Digest, subjectDigest, err)
	}
	for _, layer := range layers {
		if layer.Digest != want {
			continue
		}
		if layer.Size > maxSBOMBytes {
			return nil, fmt.Errorf("sbom layer %s is %d bytes, over the %d forge reads", layer.Digest, layer.Size, maxSBOMBytes)
		}
		lrc, err := repo.Fetch(ctx, layer)
		if err != nil {
			return nil, err
		}
		data, err := content.ReadAll(lrc, layer)
		_ = lrc.Close()
		if err != nil {
			return nil, err
		}
		if got := digest.FromBytes(data); got != want {
			return nil, fmt.Errorf("sbom layer read back as %s, want %s", got, want)
		}
		return data, nil
	}
	return nil, fmt.Errorf("manifest %s has no layer %s", desc.Digest, want)
}
