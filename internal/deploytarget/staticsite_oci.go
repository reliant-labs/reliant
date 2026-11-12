package deploytarget

// A HOSTED StaticSite release is an OCI artifact.
//
// forge assembles the site exactly as the self-hosted executor does (the
// shared StagePlan), packs the tree into ONE deterministic tar.gz layer, and
// pushes it to the frontend's own declared image reference under the reserved
// `static.v1` segment. The manifest digest IS the release: the ledger records
// it like any image, keyed by that full repository, and promotion binds it.
//
// The StaticSite spec then carries BOTH halves of the address —
// releaseRepository and liveDigest — because the repository is a fact only
// this push knows. The control plane's operator pulls exactly the recorded
// reference and writes it into sites/<org>/<env>/<site>/releases/<digest>/
// before syncing live/. It does not recompose a path from a registry base and
// an org label: that was an independent second derivation of where the bytes
// are, and when it disagreed with this push the operator 404'd on an artifact
// that existed. Whether a caller may push where it claims to have pushed is
// a platform policy, enforced as ADMISSION on the recorded reference instead.
//
// Why this and not a signed upload URL into the platform bucket: the org
// then never holds any bucket credential at all (the bucket's only writer
// stays the operator), there is no second credential type or RPC, and a
// release is content-addressed and immutable by construction instead of "a
// prefix someone wrote to". See .forge/scratch/unify/reports/STATIC.md.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	// StaticSiteArtifactType is the OCI artifactType of a site release.
	StaticSiteArtifactType = "application/vnd.forge.staticsite.v1"
	// StaticSiteLayerMediaType is its single layer: the assembled tree.
	StaticSiteLayerMediaType = "application/vnd.forge.staticsite.layer.v1.tar+gzip"
	// StaticSiteRepositorySegment is the path segment site releases live
	// under inside an org's registry subtree. THE DOT IS LOAD-BEARING: an
	// RFC-1123 build name cannot contain one, so no backend image an org
	// builds can collide with a site release (the control plane's
	// ociregistry.ConfigNamespace uses the same property).
	StaticSiteRepositorySegment = "static.v1"
)

// HostedStaticRepository is the repository a site's releases are pushed to:
// the frontend's OWN declared image reference plus the platform's layout
// segment (`ghcr.io/acme/web` → `ghcr.io/acme/web/static.v1`).
//
// The segment is APPENDED rather than asked for. It exists so the control
// plane can tell a site release from a backend image, which is forge's
// business and not something an author should have to encode — so the
// reference they wrote stays theirs and static.v1 never appears in their KCL.
func HostedStaticRepository(image string) string {
	return strings.TrimSuffix(strings.TrimSpace(image), "/") + "/" + StaticSiteRepositorySegment
}

// BuildStaticSiteTree builds and assembles one frontend with the shared
// StagePlan and returns the assembled directory — the exact tree the
// self-hosted executor would upload.
//
// The tree holds no source map. The StageResult says what the build id was and
// where the extracted maps went, so a caller can file them under it.
func BuildStaticSiteTree(ctx context.Context, projectDir string, fe StaticSiteFrontend) (string, StageResult, error) {
	p := StaticSiteProvider{ProjectDir: projectDir}
	plan, err := p.buildPlan(fe)
	if err != nil {
		return "", StageResult{}, err
	}
	res, err := runStagePlan(ctx, p.runner(), plan.Stage)
	if err != nil {
		return "", StageResult{}, err
	}
	return plan.Stage.StagingDir, res, nil
}

// PackStaticSiteTree packs an assembled tree into a DETERMINISTIC tar.gz:
// entries sorted, zero timestamps and owners, fixed modes, no gzip header
// time. Identical trees therefore produce identical bytes, so re-pushing
// unchanged content yields the same digest and an idempotent release cut.
//
// Only regular files and directories are packed. A symlink is REFUSED rather
// than followed or preserved: followed, it would ship whatever it points at on
// the build machine; preserved, it would ask the platform to materialize a
// link it must never write into a shared bucket.
func PackStaticSiteTree(dir string) ([]byte, error) {
	return packTree(dir, "static site release", "the assembled site is empty; refusing to publish a release that would serve nothing")
}

// packTree is the deterministic tar.gz both the site and the source map
// artifacts are packed with. what names the artifact in the symlink refusal;
// emptyMsg is the whole refusal for an empty tree.
func packTree(dir, what, emptyMsg string) ([]byte, error) {
	type entry struct{ rel, path string }
	var files []entry
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dir || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file (mode %s); a %s carries only files", filepath.ToSlash(rel), info.Mode(), what)
		}
		files = append(files, entry{rel: filepath.ToSlash(rel), path: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", dir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("pack %s: %s", dir, emptyMsg)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		data, rerr := os.ReadFile(f.path)
		if rerr != nil {
			return nil, fmt.Errorf("pack %s: %w", f.rel, rerr)
		}
		hdr := &tar.Header{Name: f.rel, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fixedCreated pins the manifest's created annotation so the manifest — and
// therefore the release digest — depends on the content alone.
const fixedCreated = "1970-01-01T00:00:00Z"

// PushStaticSiteArtifact pushes a packed site to repository and returns the
// manifest digest. Credentials come from the ambient docker credential store
// — the same one `docker push` of a backend image to the same push base uses.
func PushStaticSiteArtifact(ctx context.Context, repository string, layer []byte) (string, error) {
	repo, err := newRegistryRepository("static site", repository)
	if err != nil {
		return "", err
	}
	return pushStaticSite(ctx, repo, layer)
}

// newRegistryRepository opens repository with the ambient docker credential
// store, over plain HTTP for the loopback registries a local k3d publishes.
func newRegistryRepository(what, repository string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("%s repository %q: %w", what, repository, err)
	}
	repo.PlainHTTP = plainHTTPRegistry(repo.Reference.Registry)
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("read docker credentials: %w", err)
	}
	repo.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache(), Credential: credentials.Credential(store)}
	return repo, nil
}

// pushStaticSite is the transport-agnostic half, so it runs against any
// oras target (an in-memory store in tests). The manifest is composed here
// rather than by oras.PackManifest so its digest is a pure function of the
// layer: a re-push of an unchanged site returns the same release digest even
// when the target answers "already exists".
func pushStaticSite(ctx context.Context, target content.Pusher, layer []byte) (string, error) {
	desc, err := pushArtifact(ctx, target, StaticSiteArtifactType, StaticSiteLayerMediaType, nil, layer)
	if err != nil {
		return "", err
	}
	return desc.Digest.String(), nil
}

// pushArtifact pushes a one-layer OCI artifact: the layer blob, the empty
// config, and a manifest whose bytes are a pure function of (artifactType,
// layer media type, annotations, layer). Nothing time- or host-dependent is
// in it, so the same inputs always produce the same manifest digest. The
// manifest descriptor is returned so a caller can tag it.
func pushArtifact(ctx context.Context, target content.Pusher, artifactType, layerMediaType string, annotations map[string]string, layer []byte) (ocispec.Descriptor, error) {
	layerDesc := content.NewDescriptorFromBytes(layerMediaType, layer)
	all := map[string]string{ocispec.AnnotationCreated: fixedCreated}
	for k, v := range annotations {
		all[k] = v
	}
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: artifactType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{layerDesc},
		Annotations:  all,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	manifestDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	manifestDesc.ArtifactType = artifactType
	for _, blob := range []struct {
		desc ocispec.Descriptor
		data []byte
	}{{layerDesc, layer}, {ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data}, {manifestDesc, raw}} {
		if err := target.Push(ctx, blob.desc, bytes.NewReader(blob.data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			return ocispec.Descriptor{}, fmt.Errorf("push %s: %w", blob.desc.MediaType, err)
		}
	}
	return manifestDesc, nil
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
