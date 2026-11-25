package deploytarget

// Mirroring a third-party image into the platform's registry.
//
// The control plane pulls only from its own registry, so a hosted workload
// whose image is someone else's (`docker.io/traefik/whoami:v1.10.3`, or an
// image CI pushed to ghcr.io) cannot run as declared. forge copies it, at
// release time, to `<push base>/<its path>` (hostedimage.MirrorRepository) and
// the release pins the copy BY DIGEST — so the release ledger stays digest-
// based, a later re-push of the upstream tag changes nothing, and the control
// plane admits it exactly as it admits an image forge built.
//
// The copy is registry to registry (oras.Copy): the manifest bytes move
// verbatim, so the mirrored digest IS the upstream digest, and every platform
// of a multi-arch index comes along. No docker daemon is involved.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
)

// MirroredImage is what a mirror produced: the digest the release pins and
// the platforms the image carries (empty for a single-platform manifest whose
// platform the index does not state).
type MirroredImage struct {
	Digest    string
	Platforms []string
}

// MirrorTag is the tag a mirrored digest is kept under in the destination:
// derived from the digest (`sha256-<hex>`), so each mirrored digest holds its
// own tag. Re-mirroring a moved upstream tag adds a tag rather than moving
// one, which keeps every digest an earlier release pins referenced — an
// untagged manifest is what a registry garbage collector deletes, and a
// rollback to it would then pull nothing.
func MirrorTag(digest string) string {
	return strings.Replace(digest, ":", "-", 1)
}

// MirrorImage copies source (a full reference: host, path, and a tag or
// digest; no tag means `latest`) to the destination repository, and returns
// the digest it was copied at. Credentials for both ends come from the ambient
// docker credential store — the one the build's `docker login` to the
// platform registry wrote, and anonymous for a public source.
func MirrorImage(ctx context.Context, source, destination string) (MirroredImage, error) {
	srcRepo, srcRef := splitMirrorSource(source)
	src, err := newRegistryRepository("mirror source", srcRepo)
	if err != nil {
		return MirroredImage{}, err
	}
	dst, err := newRegistryRepository("mirror destination", destination)
	if err != nil {
		return MirroredImage{}, err
	}
	return mirrorImage(ctx, src, srcRef, dst)
}

// mirrorImage is the transport-agnostic half, so it runs against any oras
// target (in-memory stores in tests).
func mirrorImage(ctx context.Context, src oras.ReadOnlyTarget, srcRef string, dst oras.Target) (MirroredImage, error) {
	desc, err := src.Resolve(ctx, srcRef)
	if err != nil {
		return MirroredImage{}, fmt.Errorf("resolve %s: %w", srcRef, err)
	}
	// Copy the graph under the RESOLVED descriptor, so what lands is exactly
	// what was resolved even if the upstream tag moves mid-copy.
	if err := oras.CopyGraph(ctx, src, dst, desc, oras.DefaultCopyGraphOptions); err != nil {
		return MirroredImage{}, fmt.Errorf("copy %s: %w", desc.Digest, err)
	}
	if err := dst.Tag(ctx, desc, MirrorTag(desc.Digest.String())); err != nil {
		return MirroredImage{}, fmt.Errorf("tag %s: %w", desc.Digest, err)
	}
	platforms, err := descriptorPlatforms(ctx, src, desc)
	if err != nil {
		return MirroredImage{}, err
	}
	return MirroredImage{Digest: desc.Digest.String(), Platforms: platforms}, nil
}

// descriptorPlatforms is the `os/arch[/variant]` set an index declares. A
// single manifest states none here, and "" is what the release then records:
// the platform guard reads the registry when a release recorded nothing.
func descriptorPlatforms(ctx context.Context, src content.Fetcher, desc ocispec.Descriptor) ([]string, error) {
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, "application/vnd.docker.distribution.manifest.list.v2+json":
	default:
		return nil, nil
	}
	raw, err := content.FetchAll(ctx, src, desc)
	if err != nil {
		return nil, fmt.Errorf("read index %s: %w", desc.Digest, err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("parse index %s: %w", desc.Digest, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range index.Manifests {
		p := m.Platform
		if p == nil || p.OS == "" || p.OS == "unknown" {
			// Attestation manifests carry unknown/unknown.
			continue
		}
		s := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			s += "/" + p.Variant
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// splitMirrorSource splits a full reference into its repository and the tag
// or digest to resolve. A reference with neither is `latest`, as docker reads
// it.
func splitMirrorSource(source string) (repository, ref string) {
	if at := strings.LastIndex(source, "@"); at >= 0 {
		return HostedImageRepository(source), source[at+1:]
	}
	repo := HostedImageRepository(source)
	if repo != source {
		return repo, source[len(repo)+1:]
	}
	return repo, "latest"
}
