// Package sbomoci keeps an OCI artifact's SBOM next to it, in the same
// registry, as an OCI 1.1 referrer — and reads it back.
//
// A release records each artifact's SBOM by digest (release.SBOMRef). The
// bytes live where the artifact's runtime keeps them: always in the machine
// ledger's content-addressed store, and, for an artifact that IS an OCI
// artifact (an image, a hosted static site), also beside it in its registry,
// so the SBOM travels with the image across mirrors and promotions (`oras cp
// -r`, `crane cp` carry referrers) under the registry's own access control and
// retention.
//
// THE REFERRER is a plain OCI 1.1 artifact manifest: artifactType
// application/vnd.cyclonedx+json, one layer holding the SBOM bytes, the empty
// config, and `subject` = the artifact's manifest descriptor. It is not
// signed here; whoever can sign signs that referrer's digest beside it.
//
// REGISTRIES DIFFER ONLY ON READ. Every registry probed accepts a manifest
// with `subject`; some (GAR, Docker Hub, zot) index it behind the referrers
// API, others (GHCR, distribution, Reliant's hosted registry) do not, and the
// writer maintains the OCI tag-schema fallback index (`sha256-<hex>` tag)
// itself. oras-go's auto mode picks per registry — probe the API, fall back
// to the tag — so nothing here names a registry. Two oras-go behaviours on
// the fallback path are designed around (phase 0, forge#2468):
//
//   - After moving the fallback tag, oras-go deletes the previous index. A
//     hosted tenant token cannot DELETE, so the second attach to a subject
//     failed AFTER the tag had moved: a false failure. SkipReferrersGC is set
//     on every repository; the orphaned untagged index is the registry GC's.
//   - Updating the fallback index is an unconditional read-modify-write, so
//     concurrent attaches to one subject lose index entries (2 or 6 at once
//     listed 1 or 2, every trial). Attaches are serialized per subject inside
//     this process, and every attach verifies by listing the subject's
//     referrers afterwards, re-pushing its manifest when it is missing —
//     idempotent, and the cure for a writer in another process that raced it.
//
// GAR answers `"manifests": null` for a subject with no referrers; oras-go
// v2.7.0 decodes that as an empty list, and the tests hold it to that.
package sbomoci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	// ArtifactType is the referrer manifest's artifactType, and the media type
	// of its one layer: a CycloneDX JSON document.
	ArtifactType = "application/vnd.cyclonedx+json"

	// AnnotationSBOMDigest names the SBOM bytes' sha256 on the referrer
	// manifest. A referrers listing carries a manifest's annotations but not
	// its layers, so this is what lets a reader (and an idempotent re-attach)
	// pick the referrer holding a given SBOM without fetching every manifest.
	AnnotationSBOMDigest = "dev.forge.sbom.digest"
	// AnnotationGenerator names the tool that wrote the SBOM ("syft@v1.57.2").
	AnnotationGenerator = "dev.forge.sbom.generator"
	// AnnotationRelease names the forge release the SBOM was recorded for.
	AnnotationRelease = "dev.forge.release"

	// bundleRepositorySegment is the reserved path segment a forge config
	// bundle repository carries (internal/bundle.RepositorySegment; a test
	// holds the two equal). The hosted registry's sigguard refuses any manifest
	// with `subject` there from a tenant token, and an SBOM describes an
	// artifact a bundle deploys, never the bundle itself.
	bundleRepositorySegment = "bundle.v1"

	// verifyAttempts bounds the verify-and-reattach loop: list the subject's
	// referrers, re-push when ours is missing. Each re-push re-reads the
	// fallback index, so one more attempt than the writers racing it is
	// enough; a registry that drops the manifest every time is reported.
	verifyAttempts = 5
)

// ErrSkipped is an Attach that deliberately attached nothing. The ledger copy
// of the SBOM is unaffected. [SkipReason] says why, in words fit for
// release.SBOMRef.Reason.
var ErrSkipped = errors.New("sbom referrer not attached")

// skipError carries the reason of an [ErrSkipped].
type skipError struct{ reason string }

func (e *skipError) Error() string        { return ErrSkipped.Error() + ": " + e.reason }
func (e *skipError) Is(target error) bool { return target == ErrSkipped }

// SkipReason returns why err skipped attachment, when it is an [ErrSkipped].
func SkipReason(err error) (string, bool) {
	var s *skipError
	if errors.As(err, &s) {
		return s.reason, true
	}
	return "", false
}

// AttachOptions shapes one attach. The zero value is the default: the
// ambient docker credential store, plain HTTP only for loopback registries,
// and no informational annotations.
type AttachOptions struct {
	Registry RegistryOptions
	// Generator, Release and Created become the referrer's
	// dev.forge.sbom.generator, dev.forge.release and
	// org.opencontainers.image.created annotations; an empty value is left
	// out. They are inputs to the manifest's digest, so pass values fixed for
	// the release (its cut time, not time.Now) and a re-attach of the same
	// SBOM writes the same manifest.
	Generator string
	Release   string
	Created   time.Time
}

// Attach stores sbom as an OCI 1.1 referrer of subject and returns the
// referrer manifest's reference, `<repository>@sha256:<hex>` in subject's
// repository — the value release.SBOMRef.Referrer records.
//
// subject is `<repository>@sha256:<hex>`: the image's or static-site
// artifact's manifest, by digest. A tag is refused, because a referrer binds a
// digest and a tag can move between the lookup and the push.
//
// Attaching an SBOM the subject already carries (same bytes, found by
// [AnnotationSBOMDigest]) is a no-op that returns the existing referrer. A
// subject in a forge bundle repository (`…/bundle.v1/…`) is not attached to:
// the error is an [ErrSkipped] and [SkipReason] says why.
func Attach(ctx context.Context, subject string, sbom []byte, opts AttachOptions) (referrer string, err error) {
	if len(bytes.TrimSpace(sbom)) == 0 {
		return "", errors.New("sbom is empty")
	}
	if !json.Valid(sbom) {
		return "", errors.New("sbom is not JSON: a referrer of type " + ArtifactType + " holds a CycloneDX JSON document")
	}
	ref, subjectDigest, err := parseSubject(subject)
	if err != nil {
		return "", err
	}
	if inBundleRepository(ref.Repository) {
		return "", &skipError{reason: fmt.Sprintf("%s is a forge bundle repository (%s/), which takes no referrers; the SBOM is kept in the release ledger", ref.Registry+"/"+ref.Repository, bundleRepositorySegment)}
	}

	unlock := subjectLocks.lock(ref.Registry + "/" + ref.Repository + "@" + subjectDigest.String())
	defer unlock()

	repo, err := openRepository(ref.Registry+"/"+ref.Repository, opts.Registry)
	if err != nil {
		return "", err
	}
	subjectDesc, err := resolveSubject(ctx, repo, subjectDigest)
	if err != nil {
		return "", err
	}
	repoName := ref.Registry + "/" + ref.Repository
	sbomDigest := digest.FromBytes(sbom)

	// Already attached? This listing is also oras-go's referrers-API probe,
	// so the push below takes the path the registry supports.
	existing, err := listReferrers(ctx, repo, subjectDesc)
	if err != nil {
		return "", fmt.Errorf("list the referrers of %s: %w", subject, err)
	}
	for _, d := range existing {
		if d.Annotations[AnnotationSBOMDigest] == sbomDigest.String() {
			return repoName + "@" + d.Digest.String(), nil
		}
	}

	layer := content.NewDescriptorFromBytes(ArtifactType, sbom)
	manifestDesc, manifestJSON, err := referrerManifest(subjectDesc, layer, sbomDigest, opts)
	if err != nil {
		return "", err
	}
	for _, blob := range []struct {
		desc ocispec.Descriptor
		data []byte
	}{{layer, sbom}, {ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data}} {
		if err := pushBlob(ctx, repo, blob.desc, blob.data); err != nil {
			return "", fmt.Errorf("push the sbom referrer of %s: %w", subject, err)
		}
	}

	// Push, then verify the subject lists it; re-push (which re-reads and
	// re-writes the fallback index) when it does not.
	for attempt := 1; ; attempt++ {
		if err := repo.Push(ctx, manifestDesc, bytes.NewReader(manifestJSON)); err != nil {
			return "", fmt.Errorf("push the sbom referrer of %s: %w", subject, err)
		}
		listed, err := listReferrers(ctx, repo, subjectDesc)
		if err != nil {
			return "", fmt.Errorf("verify the sbom referrer of %s: %w", subject, err)
		}
		for _, d := range listed {
			if d.Digest == manifestDesc.Digest {
				return repoName + "@" + manifestDesc.Digest.String(), nil
			}
		}
		if attempt >= verifyAttempts {
			return "", fmt.Errorf("the sbom referrer %s of %s was pushed %d times but %s never listed it among the subject's referrers",
				manifestDesc.Digest, subject, attempt, ref.Registry)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
		}
	}
}

// referrerManifest composes the referrer: bytes that are a pure function of
// (subject, sbom, options), so the same attach always yields the same digest.
func referrerManifest(subject, layer ocispec.Descriptor, sbomDigest digest.Digest, opts AttachOptions) (ocispec.Descriptor, []byte, error) {
	annotations := map[string]string{AnnotationSBOMDigest: sbomDigest.String()}
	if g := strings.TrimSpace(opts.Generator); g != "" {
		annotations[AnnotationGenerator] = g
	}
	if r := strings.TrimSpace(opts.Release); r != "" {
		annotations[AnnotationRelease] = r
	}
	if !opts.Created.IsZero() {
		annotations[ocispec.AnnotationCreated] = opts.Created.UTC().Format(time.RFC3339)
	}
	// The subject descriptor carries only what identifies the manifest; the
	// registry's other descriptor fields (annotations, platform) are not part
	// of the binding.
	subj := ocispec.Descriptor{MediaType: subject.MediaType, Digest: subject.Digest, Size: subject.Size}
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{layer},
		Subject:      &subj,
		Annotations:  annotations,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	desc.ArtifactType = ArtifactType
	desc.Annotations = annotations
	return desc, raw, nil
}

func pushBlob(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor, data []byte) error {
	exists, err := repo.Exists(ctx, desc)
	if err != nil {
		return fmt.Errorf("check blob %s: %w", desc.Digest, err)
	}
	if exists {
		return nil
	}
	if err := repo.Push(ctx, desc, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("push blob %s: %w", desc.Digest, err)
	}
	return nil
}

// listReferrers lists subject's SBOM referrers through oras-go's auto mode:
// the referrers API when the registry has it, the tag-schema index otherwise.
func listReferrers(ctx context.Context, repo *remote.Repository, subject ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	var out []ocispec.Descriptor
	err := repo.Referrers(ctx, subject, ArtifactType, func(page []ocispec.Descriptor) error {
		out = append(out, page...)
		return nil
	})
	return out, err
}

func inBundleRepository(repository string) bool {
	for _, seg := range strings.Split(repository, "/") {
		if seg == bundleRepositorySegment {
			return true
		}
	}
	return false
}
